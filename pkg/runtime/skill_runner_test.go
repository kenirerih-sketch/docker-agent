package runtime

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/docker/docker-agent/pkg/agent"
)

// TestApplySkillModelOverride_NotConfigured verifies that the helper
// returns an error (and does not modify the agent state) when the runtime
// has no model switcher configured.
func TestApplySkillModelOverride_NotConfigured(t *testing.T) {
	t.Parallel()

	r := &LocalRuntime{}
	a := agent.New("root", "test")

	restore, err := r.applySkillModelOverride(t.Context(), a, "openai/gpt-4o")
	require.Error(t, err)
	assert.Nil(t, restore)
	assert.False(t, a.HasModelOverride(), "agent override must not be applied on error")
}
