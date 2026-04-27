// Package builtins contains the stock in-process hook implementations
// shipped with docker-agent: add_date, add_environment_info, and
// add_prompt_files.
//
// They can be referenced explicitly from a hook YAML entry using
// `{type: builtin, command: "<name>"}`. The runtime also auto-injects
// them when the corresponding agent flags (AddDate, AddEnvironmentInfo,
// AddPromptFiles) are set.
//
// Behavioral note: AddDate and AddPromptFiles are registered against
// turn_start so they recompute on every model call (matching the
// original per-turn semantics of session.buildContextSpecificSystemMessages).
// AddEnvironmentInfo is registered against session_start because working
// directory, OS, and arch don't change during a session; snapshotting
// once and persisting via Result.AdditionalContext is cheaper than
// re-computing each turn.
package builtins

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/docker/docker-agent/pkg/hooks"
	"github.com/docker/docker-agent/pkg/session"
)

// Builtin hook names. They match the `command` field of a
// `{type: builtin}` hook entry in YAML.
const (
	AddDate            = "add_date"
	AddEnvironmentInfo = "add_environment_info"
	AddPromptFiles     = "add_prompt_files"
)

// Register installs the stock builtin hooks on r. It is called once
// from the runtime constructor so the builtins are available to every
// executor the runtime constructs, without polluting any process-wide
// state. The only failure modes are programmer errors (empty name or
// nil function), which surface as a constructor error.
func Register(r *hooks.Registry) error {
	pairs := []struct {
		name string
		fn   hooks.BuiltinFunc
	}{
		{AddDate, addDate},
		{AddEnvironmentInfo, addEnvironmentInfo},
		{AddPromptFiles, addPromptFiles},
	}
	for _, p := range pairs {
		if err := r.RegisterBuiltin(p.name, p.fn); err != nil {
			return fmt.Errorf("register %q builtin: %w", p.name, err)
		}
	}
	return nil
}

// addDate returns the current date as additional context. It is
// equivalent to the previous inline `Today's date: ...` system message
// that lived in pkg/session/session.go, lifted into the hook system.
//
// Registered against EventTurnStart so the date is recomputed every
// turn rather than snapshotted at session start, matching the original
// per-turn semantics.
func addDate(_ context.Context, _ *hooks.Input, _ []string) (*hooks.Output, error) {
	return &hooks.Output{
		HookSpecificOutput: &hooks.HookSpecificOutput{
			HookEventName:     hooks.EventTurnStart,
			AdditionalContext: "Today's date: " + time.Now().Format("2006-01-02"),
		},
	}, nil
}

// addEnvironmentInfo returns formatted environment information
// (working directory, git status, OS, arch) as additional context. It
// reuses [session.GetEnvironmentInfo] to keep the format identical to the
// previous inline injection.
//
// The working directory comes from the hook input's Cwd, which the
// runtime populates with its configured working directory. If Cwd is
// empty the hook contributes nothing rather than fabricating misleading
// info.
func addEnvironmentInfo(_ context.Context, in *hooks.Input, _ []string) (*hooks.Output, error) {
	if in == nil || in.Cwd == "" {
		return nil, nil
	}
	return &hooks.Output{
		HookSpecificOutput: &hooks.HookSpecificOutput{
			HookEventName:     hooks.EventSessionStart,
			AdditionalContext: session.GetEnvironmentInfo(in.Cwd),
		},
	}, nil
}

// addPromptFiles reads each filename in args and joins their contents
// into AdditionalContext. It replaces the inline AddPromptFiles loop
// that lived in pkg/session/session.go's
// buildContextSpecificSystemMessages.
//
// Registered against EventTurnStart so the file contents are re-read
// every turn — important because the user may be editing the prompt file
// during the session and expects the model to pick up the latest text.
//
// Read errors are logged at warn level (one log line per failing file)
// but do not fail the hook; surviving files still contribute their
// content. This matches the previous loop's behavior of silently
// skipping unreadable files.
func addPromptFiles(_ context.Context, in *hooks.Input, args []string) (*hooks.Output, error) {
	if in == nil || in.Cwd == "" || len(args) == 0 {
		return nil, nil
	}
	var parts []string
	for _, name := range args {
		additional, err := session.ReadPromptFiles(in.Cwd, name)
		if err != nil {
			slog.Warn("reading prompt file", "file", name, "error", err)
			continue
		}
		parts = append(parts, additional...)
	}
	if len(parts) == 0 {
		return nil, nil
	}
	return &hooks.Output{
		HookSpecificOutput: &hooks.HookSpecificOutput{
			HookEventName:     hooks.EventTurnStart,
			AdditionalContext: strings.Join(parts, "\n\n"),
		},
	}, nil
}
