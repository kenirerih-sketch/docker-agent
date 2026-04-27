package builtins_test

import (
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/docker/docker-agent/pkg/hooks"
	"github.com/docker/docker-agent/pkg/hooks/builtins"
)

// TestLoopDetectorTripsAtThreshold verifies the happy path: with a
// threshold of 3, the third consecutive identical call returns a
// block decision; the first two are no-ops.
func TestLoopDetectorTripsAtThreshold(t *testing.T) {
	t.Parallel()

	fn := lookup(t, builtins.LoopDetector)

	in := &hooks.Input{
		SessionID: "s1",
		ToolName:  "read_file",
		ToolUseID: "call-1",
		ToolInput: map[string]any{"path": "a.txt"},
	}

	for i := 1; i <= 2; i++ {
		out, err := fn(t.Context(), in, []string{"3"})
		require.NoErrorf(t, err, "call %d must not error", i)
		assert.Nilf(t, out, "call %d must not trip yet", i)
	}

	out, err := fn(t.Context(), in, []string{"3"})
	require.NoError(t, err)
	require.NotNil(t, out, "third consecutive identical call must trip")
	assert.Equal(t, hooks.DecisionBlockValue, out.Decision,
		"trip must be expressed as a block decision")
	assert.Contains(t, out.Reason, "read_file",
		"reason must name the offending tool")
	assert.Contains(t, out.Reason, "3",
		"reason must include the consecutive count")
}

// TestLoopDetectorResetsOnDifferentArgs documents that changing tool
// arguments resets the consecutive counter. A model varying its inputs
// is making progress, even if it keeps calling the same tool.
func TestLoopDetectorResetsOnDifferentArgs(t *testing.T) {
	t.Parallel()

	fn := lookup(t, builtins.LoopDetector)
	args := []string{"3"}

	mk := func(path string) *hooks.Input {
		return &hooks.Input{
			SessionID: "s1",
			ToolName:  "read_file",
			ToolInput: map[string]any{"path": path},
		}
	}

	// Two calls with a.txt (count=2), then b.txt resets, then back
	// to a.txt resets again. Total of 4 identical-args calls but
	// none are 3-in-a-row, so the detector never trips.
	for _, p := range []string{"a.txt", "a.txt", "b.txt", "a.txt"} {
		out, err := fn(t.Context(), mk(p), args)
		require.NoError(t, err)
		assert.Nil(t, out)
	}
}

// TestLoopDetectorIgnoresKeyOrdering pins the canonicalisation
// contract: arguments differing only by JSON-key order count as the
// SAME signature. This protects against models that re-order keys
// between identical calls (a real failure mode for some providers).
func TestLoopDetectorIgnoresKeyOrdering(t *testing.T) {
	t.Parallel()

	fn := lookup(t, builtins.LoopDetector)

	in1 := &hooks.Input{
		SessionID: "s1",
		ToolName:  "run",
		ToolInput: map[string]any{"cmd": "ls", "cwd": "/tmp"},
	}
	in2 := &hooks.Input{
		SessionID: "s1",
		ToolName:  "run",
		ToolInput: map[string]any{"cwd": "/tmp", "cmd": "ls"},
	}

	out, err := fn(t.Context(), in1, []string{"2"})
	require.NoError(t, err)
	assert.Nil(t, out)

	out, err = fn(t.Context(), in2, []string{"2"})
	require.NoError(t, err)
	require.NotNil(t, out, "key reorder must still match the previous signature")
	assert.Equal(t, hooks.DecisionBlockValue, out.Decision)
}

// TestLoopDetectorExemptToolIsInvisible documents the polling-tool
// contract: an exempt tool name neither increments NOR resets the
// counter. A looping model can't sneak a single polling call between
// identical stuck calls to evade detection.
func TestLoopDetectorExemptToolIsInvisible(t *testing.T) {
	t.Parallel()

	fn := lookup(t, builtins.LoopDetector)
	args := []string{"3", "view_background_job"}

	read := &hooks.Input{
		SessionID: "s1",
		ToolName:  "read_file",
		ToolInput: map[string]any{"path": "a.txt"},
	}
	poll := &hooks.Input{
		SessionID: "s1",
		ToolName:  "view_background_job",
		ToolInput: map[string]any{"job_id": "j1"},
	}

	// Two reads (count=2), one poll (invisible, count stays 2),
	// then one more read (count=3 → trips).
	for _, in := range []*hooks.Input{read, read, poll} {
		out, err := fn(t.Context(), in, args)
		require.NoError(t, err)
		assert.Nil(t, out)
	}
	out, err := fn(t.Context(), read, args)
	require.NoError(t, err)
	require.NotNil(t, out, "polling call must not reset the counter")
	assert.Equal(t, hooks.DecisionBlockValue, out.Decision)
}

// TestLoopDetectorIsolatesSessions verifies that two concurrent
// sessions don't share counter state. A broken implementation would
// share the consecutive count across sessions and trip earlier than
// each session's own threshold.
func TestLoopDetectorIsolatesSessions(t *testing.T) {
	t.Parallel()

	fn := lookup(t, builtins.LoopDetector)

	mk := func(sessionID string) *hooks.Input {
		return &hooks.Input{
			SessionID: sessionID,
			ToolName:  "read_file",
			ToolInput: map[string]any{"path": "a.txt"},
		}
	}

	// Drive 2 calls in B before any in A: if state were shared, the
	// next call (in A) would already be at count=3 and trip on the
	// first invocation. With per-session state, A's counter starts
	// fresh.
	for i := range 2 {
		out, err := fn(t.Context(), mk("B"), []string{"3"})
		require.NoError(t, err)
		require.Nilf(t, out, "call %d in B should not trip", i+1)
	}

	// 2 calls in A: still count=2 in A, must not trip.
	for i := range 2 {
		out, err := fn(t.Context(), mk("A"), []string{"3"})
		require.NoError(t, err)
		require.Nilf(t, out, "call %d in A should not trip when state is per-session", i+1)
	}

	// Third call in A trips it independently of B's counter.
	out, err := fn(t.Context(), mk("A"), []string{"3"})
	require.NoError(t, err)
	require.NotNil(t, out, "session A's third call must trip independently")

	// Third call in B trips B independently of A's earlier trip.
	out, err = fn(t.Context(), mk("B"), []string{"3"})
	require.NoError(t, err)
	require.NotNil(t, out, "session B's third call must trip independently")
}

// TestLoopDetectorDefaultThresholdWhenArgInvalid documents the lenient
// arg parsing: a missing or invalid threshold falls back to the
// 5-call default, so a misconfigured YAML never silently disables
// the detector.
func TestLoopDetectorDefaultThresholdWhenArgInvalid(t *testing.T) {
	t.Parallel()

	cases := [][]string{
		nil,             // no args at all
		{"abc"},         // not a number
		{"0"},           // not positive
		{"-3"},          // negative
		{"", "exempt1"}, // empty threshold, only exempts
	}
	for _, args := range cases {
		fn := lookup(t, builtins.LoopDetector) // fresh state per case
		in := &hooks.Input{
			SessionID: "s",
			ToolName:  "read_file",
			ToolInput: map[string]any{"path": "a.txt"},
		}
		// 4 identical calls must NOT trip with the default of 5.
		for range 4 {
			out, err := fn(t.Context(), in, args)
			require.NoError(t, err)
			require.Nilf(t, out, "args=%v: must not trip before default 5", args)
		}
		// 5th call trips at the default.
		out, err := fn(t.Context(), in, args)
		require.NoError(t, err)
		require.NotNilf(t, out, "args=%v: must trip at default 5", args)
	}
}

// TestLoopDetectorIgnoresIncompleteInput pins the defensive guard:
// missing SessionID or ToolName produces no state mutation and no
// output. This protects against future changes to dispatch where an
// edge case might fire post_tool_use without these fields populated.
func TestLoopDetectorIgnoresIncompleteInput(t *testing.T) {
	t.Parallel()

	fn := lookup(t, builtins.LoopDetector)

	out, err := fn(t.Context(), nil, []string{"2"})
	require.NoError(t, err)
	assert.Nil(t, out)

	out, err = fn(t.Context(), &hooks.Input{ToolName: "x"}, []string{"2"})
	require.NoError(t, err)
	assert.Nil(t, out)

	out, err = fn(t.Context(), &hooks.Input{SessionID: "s"}, []string{"2"})
	require.NoError(t, err)
	assert.Nil(t, out)
}

// TestLoopDetectorConcurrentCallsAreSafe is a smoke test for the
// builtin's mutex. Many goroutines hammering the same session must
// produce a deterministic trip count without races (run with -race).
func TestLoopDetectorConcurrentCallsAreSafe(t *testing.T) {
	t.Parallel()

	fn := lookup(t, builtins.LoopDetector)
	in := &hooks.Input{
		SessionID: "concurrent",
		ToolName:  "read_file",
		ToolInput: map[string]any{"path": "a.txt"},
	}

	const callers = 50
	var wg sync.WaitGroup
	wg.Add(callers)
	for range callers {
		go func() {
			defer wg.Done()
			_, _ = fn(t.Context(), in, []string{"5"})
		}()
	}
	wg.Wait()
	// We don't assert on a specific output (interleaving is
	// non-deterministic); the test passes if -race doesn't fire.
}
