package builtins

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"sync"

	"github.com/docker/docker-agent/pkg/hooks"
)

// LoopDetector is the registered name of the loop_detector builtin.
const LoopDetector = "loop_detector"

// defaultLoopDetectorThreshold is the consecutive-identical-call count
// at which the detector trips when no explicit threshold is configured.
// Five matches the historical default of the inline tool-loop detector
// previously baked into pkg/runtime.
const defaultLoopDetectorThreshold = 5

// loopDetectorBuiltin is the post_tool_use builtin that terminates the
// run when the model issues the same tool call (name + canonical
// arguments) `threshold` times in a row.
//
// State is per-session, keyed by [hooks.Input.SessionID], so concurrent
// runs on a shared runtime can't cross-contaminate each other's
// counters. The state map is bounded by the number of *active* sessions
// rather than all-time sessions \u2014 the runtime calls `clearSession` from
// session_end so the entry is dropped when the run finishes.
//
// Semantics differ from the previous inline detector in one specific
// way: signatures are tracked **per call**, not per batch. For the
// common stuck-agent case (the model repeatedly emits a single tool
// call with identical arguments) the trip point is unchanged. A model
// stuck in an alternating multi-tool batch like `[A, B] [A, B] [A, B]`
// is no longer flagged because each B resets A's counter; users who
// hit that pattern should rely on `max_iterations` (interactive UX) or
// the new max_iterations builtin (hard stop) instead.
//
// Polling tools listed in `args` after the threshold (e.g.
// `view_background_agent`) are silently ignored: a polling call
// neither increments nor resets the counter, so a looping model can't
// evade detection by sneaking a single polling call between identical
// stuck calls.
type loopDetectorBuiltin struct {
	mu     sync.Mutex
	states map[string]*loopDetectorState // keyed by SessionID
}

type loopDetectorState struct {
	lastSignature string
	consecutive   int
}

func newLoopDetector() *loopDetectorBuiltin {
	return &loopDetectorBuiltin{states: map[string]*loopDetectorState{}}
}

// hook is registered as the [hooks.BuiltinFunc] for
// [hooks.EventPostToolUse]. Args layout: `[threshold, exempt1, exempt2, ...]`
// where `threshold` is an optional positive integer (defaults to
// [defaultLoopDetectorThreshold] when missing or invalid) and the
// remaining strings are tool names to exempt from detection.
func (d *loopDetectorBuiltin) hook(_ context.Context, in *hooks.Input, args []string) (*hooks.Output, error) {
	if in == nil || in.SessionID == "" || in.ToolName == "" {
		// Defensive: post_tool_use always carries SessionID and
		// ToolName today. Skipping unbounded, unkeyed events keeps
		// the state map from filling with anonymous entries.
		return nil, nil
	}

	threshold, exempt := parseLoopDetectorArgs(args)

	if _, ok := exempt[in.ToolName]; ok {
		// Polling-style tools never count toward the consecutive
		// total and never reset it: see the type-level comment for
		// why visibility-zero (rather than counter-reset) is the
		// right behaviour.
		return nil, nil
	}

	sig := in.ToolName + "\x00" + canonicalToolInput(in.ToolInput)

	d.mu.Lock()
	state, ok := d.states[in.SessionID]
	if !ok {
		state = &loopDetectorState{}
		d.states[in.SessionID] = state
	}
	if sig == state.lastSignature {
		state.consecutive++
	} else {
		state.lastSignature = sig
		state.consecutive = 1
	}
	tripped := state.consecutive >= threshold
	count := state.consecutive
	d.mu.Unlock()

	if !tripped {
		return nil, nil
	}

	slog.Warn("loop_detector tripped",
		"tool", in.ToolName, "consecutive", count,
		"threshold", threshold, "session_id", in.SessionID)

	reason := fmt.Sprintf(
		"Agent terminated: detected %d consecutive identical calls to %s. "+
			"This indicates a degenerate loop where the model is not making progress.",
		count, in.ToolName)

	// "block" is the post_tool_use deny verdict: aggregate() turns it
	// into Result.Allowed=false, which the runtime translates into
	// the standard Error / notification / on_error fan-out before
	// terminating the run.
	return &hooks.Output{
		Decision: hooks.DecisionBlockValue,
		Reason:   reason,
	}, nil
}

// clearSession drops a session's state entry, called from a
// session_end hook so long-running runtimes don't accumulate
// per-session entries indefinitely.
func (d *loopDetectorBuiltin) clearSession(sessionID string) {
	d.mu.Lock()
	delete(d.states, sessionID)
	d.mu.Unlock()
}

// parseLoopDetectorArgs splits builtin args into (threshold, exempt
// tool name set). An invalid or non-positive threshold falls back to
// [defaultLoopDetectorThreshold] silently \u2014 a misconfigured YAML
// shouldn't disable the detector entirely.
func parseLoopDetectorArgs(args []string) (int, map[string]struct{}) {
	threshold := defaultLoopDetectorThreshold
	exempt := map[string]struct{}{}

	if len(args) == 0 {
		return threshold, exempt
	}
	if n, err := strconv.Atoi(args[0]); err == nil && n > 0 {
		threshold = n
	}
	for _, name := range args[1:] {
		if name != "" {
			exempt[name] = struct{}{}
		}
	}
	return threshold, exempt
}
