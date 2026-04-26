package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/docker/docker-agent/pkg/agent"
	"github.com/docker/docker-agent/pkg/model/provider"
	"github.com/docker/docker-agent/pkg/session"
	"github.com/docker/docker-agent/pkg/tools"
	"github.com/docker/docker-agent/pkg/tools/builtin"
)

// handleRunSkill executes a skill as an isolated sub-agent. The skill's
// SKILL.md content (with command expansions) becomes the system prompt, and
// the caller-provided task becomes the implicit user message. The sub-agent
// runs in a child session using the current agent's model and tools, and
// its final response is returned as the tool result.
//
// This implements the `context: fork` behaviour from the SKILL.md
// frontmatter, following the same convention as Claude Code.
func (r *LocalRuntime) handleRunSkill(ctx context.Context, sess *session.Session, toolCall tools.ToolCall, evts chan Event) (*tools.ToolCallResult, error) {
	var params builtin.RunSkillArgs
	if err := json.Unmarshal([]byte(toolCall.Function.Arguments), &params); err != nil {
		return nil, fmt.Errorf("invalid arguments: %w", err)
	}

	st := r.CurrentAgentSkillsToolset()
	if st == nil {
		return tools.ResultError("no skills are available for the current agent"), nil
	}

	skill := st.FindSkill(params.Name)
	if skill == nil {
		return tools.ResultError(fmt.Sprintf("skill %q not found", params.Name)), nil
	}

	if !skill.IsFork() {
		return tools.ResultError(fmt.Sprintf(
			"skill %q is not configured for sub-agent execution (missing context: fork in SKILL.md frontmatter); use read_skill instead",
			params.Name,
		)), nil
	}

	// Load and expand the skill content for the system prompt.
	skillContent, err := st.ReadSkillContent(ctx, params.Name)
	if err != nil {
		return tools.ResultError(fmt.Sprintf("failed to read skill content: %s", err)), nil
	}

	a := r.CurrentAgent()
	ca := a.Name()

	ctx, span := r.startSpan(ctx, "runtime.run_skill", trace.WithAttributes(
		attribute.String("agent", ca),
		attribute.String("skill", params.Name),
		attribute.String("session.id", sess.ID),
	))
	defer span.End()

	slog.Debug("Running skill as sub-agent",
		"agent", ca,
		"skill", params.Name,
		"task", params.Task,
	)

	// If the skill declares a model override, apply it for the duration of
	// the sub-session and restore the previous override when done. The
	// parent agent loop is blocked while the sub-session runs, so this
	// save/restore is safe.
	if skill.Model != "" {
		restore, err := r.applySkillModelOverride(ctx, a, skill.Model)
		if err != nil {
			slog.Warn("Failed to apply skill model override; using default model",
				"agent", ca,
				"skill", params.Name,
				"model", skill.Model,
				"error", err,
			)
		} else {
			defer restore()
		}
	}

	cfg := SubSessionConfig{
		Task:                params.Task,
		SystemMessage:       skillContent,
		ImplicitUserMessage: params.Task,
		AgentName:           ca,
		Title:               "Skill: " + params.Name,
		ToolsApproved:       sess.ToolsApproved,
		ExcludedTools:       []string{builtin.ToolNameRunSkill},
	}

	s := newSubSession(sess, cfg, a)
	return r.runSubSessionForwarding(ctx, sess, s, span, evts, ca)
}

// applySkillModelOverride resolves modelRef and applies it as the model
// override on a, returning a restore func that reinstates the previous
// override (or clears it if none was set). modelRef can be a named model
// from the config, an inline "provider/model" spec, or an inline alloy
// (comma-separated). The runtime must be configured with model switching
// for this to succeed.
func (r *LocalRuntime) applySkillModelOverride(ctx context.Context, a *agent.Agent, modelRef string) (func(), error) {
	if r.modelSwitcherCfg == nil {
		return nil, errors.New("model switching is not configured for this runtime")
	}

	var providers []provider.Provider
	var err error
	if strings.Contains(modelRef, ",") {
		providers, err = r.resolveModelRefs(ctx, modelRef)
	} else {
		var p provider.Provider
		p, err = r.resolveModelRef(ctx, modelRef)
		if err == nil {
			providers = []provider.Provider{p}
		}
	}
	if err != nil {
		return nil, fmt.Errorf("resolve model %q: %w", modelRef, err)
	}

	prev := a.ModelOverrides()
	a.SetModelOverride(providers...)
	slog.Debug("Applied skill model override", "agent", a.Name(), "model", modelRef, "count", len(providers))

	return func() {
		if len(prev) == 0 {
			a.SetModelOverride()
		} else {
			a.SetModelOverride(prev...)
		}
		slog.Debug("Restored skill model override", "agent", a.Name())
	}, nil
}
