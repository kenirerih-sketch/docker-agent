package builtins

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"sync"

	"github.com/docker/docker-agent/pkg/hooks"
)

// MaxIterations is the registered name of the max_iterations builtin.
const MaxIterations = "max_iterations"

// maxIterationsBuiltin counts before_llm_call invocations per session
// and signals a terminating verdict once the configured limit is
// exceeded.
//
// This is intentionally a *hard stop*: it has no resume protocol and
// emits no special runtime event. The runtime translates the deny
// verdict into the standard Error / notification / on_error fan-out
// from [LocalRuntime.emitHookDrivenShutdown], same as any other
// hook-driven shutdown. The legacy `agent.MaxIterations` flag, which
// has its own special UX (MaxIterationsReachedEvent + a resume
// dialog), is unchanged and continues to live inline in loop.go;
// this builtin is the way to express "stop after N model calls,
// period" in YAML without that interactive dance.
//
// State is per-session, keyed by [hooks.Input.SessionID]. The runtime
// calls [maxIterationsBuiltin.clearSession] from session_end so a
// long-running shared runtime does not accumulate counters
// indefinitely.
type maxIterationsBuiltin struct {
	mu     sync.Mutex
	counts map[string]int // SessionID -> calls observed
}

func newMaxIterations() *maxIterationsBuiltin {
	return &maxIterationsBuiltin{counts: map[string]int{}}
}

// hook is registered as the [hooks.BuiltinFunc] for
// [hooks.EventBeforeLLMCall]. The single arg is the limit (a positive
// integer). Missing / invalid args make the hook a no-op so a
// misconfigured YAML doesn't accidentally cap a run at zero.
func (b *maxIterationsBuiltin) hook(_ context.Context, in *hooks.Input, args []string) (*hooks.Output, error) {
	if in == nil || in.SessionID == "" {
		return nil, nil
	}
	limit, ok := parseMaxIterationsArgs(args)
	if !ok {
		return nil, nil
	}

	b.mu.Lock()
	b.counts[in.SessionID]++
	count := b.counts[in.SessionID]
	b.mu.Unlock()

	if count <= limit {
		return nil, nil
	}

	slog.Warn("max_iterations tripped",
		"count", count, "limit", limit, "session_id", in.SessionID)

	reason := fmt.Sprintf(
		"Agent terminated: max_iterations builtin reached its limit of %d model call(s).",
		limit)

	return &hooks.Output{
		Decision: hooks.DecisionBlockValue,
		Reason:   reason,
	}, nil
}

// clearSession drops a session's counter, called from a session_end
// hook so a long-running runtime serving many sessions doesn't grow
// the state map without bound.
func (b *maxIterationsBuiltin) clearSession(sessionID string) {
	b.mu.Lock()
	delete(b.counts, sessionID)
	b.mu.Unlock()
}

// parseMaxIterationsArgs returns (limit, true) when args[0] is a
// positive integer, or (0, false) for any other input. The "valid"
// boolean lets the caller distinguish "no limit configured" (no-op)
// from "limit explicitly set to 0" (which would also be a no-op but
// reads as a config error and is logged at debug).
func parseMaxIterationsArgs(args []string) (int, bool) {
	if len(args) == 0 {
		return 0, false
	}
	n, err := strconv.Atoi(args[0])
	switch {
	case err != nil:
		slog.Debug("max_iterations: ignoring non-integer limit", "arg", args[0], "error", err)
		return 0, false
	case n <= 0:
		slog.Debug("max_iterations: ignoring non-positive limit", "limit", n)
		return 0, false
	}
	return n, true
}
