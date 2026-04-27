package builtins_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/docker/docker-agent/pkg/hooks"
	"github.com/docker/docker-agent/pkg/hooks/builtins"
)

// TestRegisterInstallsAllBuiltins pins the public contract of [Register]:
// every name documented in the package constants must be resolvable on
// the registry after registration. If a future change adds or renames a
// builtin without updating Register, this test fails.
func TestRegisterInstallsAllBuiltins(t *testing.T) {
	t.Parallel()

	r := hooks.NewRegistry()
	require.NoError(t, builtins.Register(r))

	for _, name := range []string{
		builtins.AddDate,
		builtins.AddEnvironmentInfo,
		builtins.AddPromptFiles,
	} {
		fn, ok := r.LookupBuiltin(name)
		assert.True(t, ok, "builtin %q must be registered", name)
		assert.NotNil(t, fn, "builtin %q must have a non-nil function", name)
	}
}

// TestAddDateReturnsTodaysDate verifies the date builtin emits a
// turn_start AdditionalContext containing today's ISO date. It does NOT
// verify the exact "Today's date: " prefix — that's a UX detail, but we
// keep the assertion loose-but-meaningful by anchoring on the date.
func TestAddDateReturnsTodaysDate(t *testing.T) {
	t.Parallel()

	fn := lookup(t, builtins.AddDate)

	out, err := fn(t.Context(), &hooks.Input{SessionID: "s"}, nil)
	require.NoError(t, err)
	require.NotNil(t, out)
	require.NotNil(t, out.HookSpecificOutput)
	assert.Equal(t, hooks.EventTurnStart, out.HookSpecificOutput.HookEventName,
		"add_date must target turn_start, not session_start")
	assert.Contains(t, out.HookSpecificOutput.AdditionalContext, time.Now().Format("2006-01-02"))
}

// TestAddEnvironmentInfoUsesInputCwd verifies that the env-info builtin
// reads its working directory from the Input (not from os.Getwd) and
// emits a session_start AdditionalContext that reflects that path. We
// assert on the Cwd appearing verbatim rather than the full env block
// format, to stay stable across cosmetic tweaks to GetEnvironmentInfo.
func TestAddEnvironmentInfoUsesInputCwd(t *testing.T) {
	t.Parallel()

	fn := lookup(t, builtins.AddEnvironmentInfo)

	cwd := t.TempDir()
	out, err := fn(t.Context(), &hooks.Input{SessionID: "s", Cwd: cwd}, nil)
	require.NoError(t, err)
	require.NotNil(t, out)
	require.NotNil(t, out.HookSpecificOutput)
	assert.Equal(t, hooks.EventSessionStart, out.HookSpecificOutput.HookEventName,
		"add_environment_info must target session_start, not turn_start")
	assert.Contains(t, out.HookSpecificOutput.AdditionalContext, cwd,
		"env info must reflect the Input's Cwd, not os.Getwd")
}

// TestAddEnvironmentInfoNoCwdIsNoop documents the safety behavior: with
// an empty Cwd the builtin contributes nothing rather than fabricating
// info from os.Getwd or "<unknown>". Returning a nil Output is a valid
// successful no-op per the BuiltinFunc contract.
func TestAddEnvironmentInfoNoCwdIsNoop(t *testing.T) {
	t.Parallel()

	fn := lookup(t, builtins.AddEnvironmentInfo)

	out, err := fn(t.Context(), &hooks.Input{SessionID: "s"}, nil)
	require.NoError(t, err)
	assert.Nil(t, out)

	out, err = fn(t.Context(), nil, nil)
	require.NoError(t, err)
	assert.Nil(t, out)
}

// TestAddPromptFilesReadsFromCwd verifies that add_prompt_files reads
// each file named in args (relative to Input.Cwd) and joins their
// contents into the turn_start AdditionalContext.
func TestAddPromptFilesReadsFromCwd(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	const promptBody = "Project guidelines: prefer Go."
	require.NoError(t, os.WriteFile(filepath.Join(dir, "PROMPT.md"), []byte(promptBody), 0o600))

	fn := lookup(t, builtins.AddPromptFiles)

	out, err := fn(t.Context(), &hooks.Input{SessionID: "s", Cwd: dir}, []string{"PROMPT.md"})
	require.NoError(t, err)
	require.NotNil(t, out)
	require.NotNil(t, out.HookSpecificOutput)
	assert.Equal(t, hooks.EventTurnStart, out.HookSpecificOutput.HookEventName,
		"add_prompt_files must target turn_start, not session_start")
	assert.Contains(t, out.HookSpecificOutput.AdditionalContext, promptBody)
}

// TestAddPromptFilesMissingFileIsTolerated documents that a missing
// prompt file is logged-and-skipped, not an error: surviving files
// still contribute, and an args list with only missing files yields a
// nil Output rather than a hard failure. This matches the original
// inline loop's silent-skip behavior.
func TestAddPromptFilesMissingFileIsTolerated(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	const promptBody = "still here"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "OK.md"), []byte(promptBody), 0o600))

	fn := lookup(t, builtins.AddPromptFiles)

	// One missing + one good: the good one survives.
	out, err := fn(t.Context(), &hooks.Input{SessionID: "s", Cwd: dir}, []string{"MISSING.md", "OK.md"})
	require.NoError(t, err)
	require.NotNil(t, out)
	assert.Contains(t, out.HookSpecificOutput.AdditionalContext, promptBody)
}

// TestAddPromptFilesNoArgsIsNoop pins the early-return behavior: with
// no args (or empty Cwd, or nil Input) the builtin does nothing rather
// than returning an empty AdditionalContext that would still register
// as a contribution.
func TestAddPromptFilesNoArgsIsNoop(t *testing.T) {
	t.Parallel()

	fn := lookup(t, builtins.AddPromptFiles)

	cases := []struct {
		name string
		in   *hooks.Input
		args []string
	}{
		{"nil input", nil, []string{"PROMPT.md"}},
		{"empty cwd", &hooks.Input{SessionID: "s"}, []string{"PROMPT.md"}},
		{"empty args", &hooks.Input{SessionID: "s", Cwd: t.TempDir()}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			out, err := fn(t.Context(), tc.in, tc.args)
			require.NoError(t, err)
			assert.Nil(t, out)
		})
	}
}

// lookup registers the builtins on a fresh Registry and returns the
// named BuiltinFunc, failing the test if it isn't present. Centralising
// the boilerplate keeps the per-builtin tests focused on behavior.
func lookup(t *testing.T, name string) hooks.BuiltinFunc {
	t.Helper()
	r := hooks.NewRegistry()
	require.NoError(t, builtins.Register(r))
	fn, ok := r.LookupBuiltin(name)
	require.True(t, ok, "builtin %q must be registered", name)
	require.NotNil(t, fn)
	return fn
}
