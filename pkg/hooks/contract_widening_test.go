package hooks_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/docker/docker-agent/pkg/hooks"
)

// TestPostToolUseBlockProducesDenyResult pins the contract widening
// for the post_tool_use event: a hook returning decision="block"
// must produce Result.Allowed=false. The runtime relies on this to
// drive its hook-driven shutdown path (loop_detector et al).
//
// The same test would have passed before the widening at the
// executor layer (the aggregate function has always set Allowed=false
// uniformly across events) — pinning it here documents the behavior
// as part of the public contract so a future refactor can't silently
// regress it.
func TestPostToolUseBlockProducesDenyResult(t *testing.T) {
	t.Parallel()

	r := hooks.NewRegistry()
	require.NoError(t, r.RegisterBuiltin("blocker", func(_ context.Context, _ *hooks.Input, _ []string) (*hooks.Output, error) {
		return &hooks.Output{
			Decision: hooks.DecisionBlockValue,
			Reason:   "stop",
		}, nil
	}))

	exec := hooks.NewExecutorWithRegistry(&hooks.Config{
		PostToolUse: []hooks.MatcherConfig{{
			Matcher: "*",
			Hooks: []hooks.Hook{{
				Type:    hooks.HookTypeBuiltin,
				Command: "blocker",
			}},
		}},
	}, t.TempDir(), nil, r)

	res, err := exec.Dispatch(t.Context(), hooks.EventPostToolUse, &hooks.Input{
		SessionID: "s",
		ToolName:  "any",
	})
	require.NoError(t, err)
	require.NotNil(t, res)
	assert.False(t, res.Allowed,
		"post_tool_use block must produce Allowed=false")
	assert.Contains(t, res.Message, "stop",
		"reason must be propagated as the Result message")
}

// TestBeforeLLMCallBlockProducesDenyResult is the symmetric pin for
// before_llm_call: max_iterations and other budget-style builtins
// rely on a block decision producing Allowed=false to stop the run
// before the model is invoked.
func TestBeforeLLMCallBlockProducesDenyResult(t *testing.T) {
	t.Parallel()

	r := hooks.NewRegistry()
	require.NoError(t, r.RegisterBuiltin("blocker", func(_ context.Context, _ *hooks.Input, _ []string) (*hooks.Output, error) {
		return &hooks.Output{
			Decision: hooks.DecisionBlockValue,
			Reason:   "budget exhausted",
		}, nil
	}))

	exec := hooks.NewExecutorWithRegistry(&hooks.Config{
		BeforeLLMCall: []hooks.Hook{{
			Type:    hooks.HookTypeBuiltin,
			Command: "blocker",
		}},
	}, t.TempDir(), nil, r)

	res, err := exec.Dispatch(t.Context(), hooks.EventBeforeLLMCall, &hooks.Input{
		SessionID: "s",
	})
	require.NoError(t, err)
	require.NotNil(t, res)
	assert.False(t, res.Allowed,
		"before_llm_call block must produce Allowed=false")
	assert.Contains(t, res.Message, "budget exhausted")
}

// TestPostToolUseContinueFalseProducesDenyResult documents that the
// continue=false form of the deny verdict produces the same
// Allowed=false outcome as decision="block". This is what allows
// shell-based hooks (which can't emit a structured Decision field)
// to participate in the run-termination contract.
func TestPostToolUseContinueFalseProducesDenyResult(t *testing.T) {
	t.Parallel()

	r := hooks.NewRegistry()
	require.NoError(t, r.RegisterBuiltin("stopper", func(_ context.Context, _ *hooks.Input, _ []string) (*hooks.Output, error) {
		stop := false
		return &hooks.Output{
			Continue:   &stop,
			StopReason: "budget exhausted",
		}, nil
	}))

	exec := hooks.NewExecutorWithRegistry(&hooks.Config{
		PostToolUse: []hooks.MatcherConfig{{
			Matcher: "*",
			Hooks: []hooks.Hook{{
				Type:    hooks.HookTypeBuiltin,
				Command: "stopper",
			}},
		}},
	}, t.TempDir(), nil, r)

	res, err := exec.Dispatch(t.Context(), hooks.EventPostToolUse, &hooks.Input{
		SessionID: "s",
		ToolName:  "any",
	})
	require.NoError(t, err)
	assert.False(t, res.Allowed)
	assert.Contains(t, res.Message, "budget exhausted")
}
