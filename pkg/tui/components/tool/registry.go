package tool

import (
	"sync"

	"github.com/docker/docker-agent/pkg/tui/core/layout"
	"github.com/docker/docker-agent/pkg/tui/service"
	"github.com/docker/docker-agent/pkg/tui/types"
)

// ComponentBuilder is a function that creates a tool component.
type ComponentBuilder func(
	msg *types.Message,
	sessionState service.SessionStateReader,
) layout.Model

// Registration pairs tool identifiers with their component builder.
// Tools with the same visual representation share a builder.
type Registration struct {
	Names   []string         // Tool names or category prefixes (e.g., "category:api")
	Builder ComponentBuilder // Factory function to create the component
}

// Registry manages tool component builders.
type Registry struct {
	mu       sync.RWMutex
	builders map[string]ComponentBuilder
}

func NewRegistry() *Registry {
	return &Registry{
		builders: make(map[string]ComponentBuilder),
	}
}

// Register adds multiple registrations at once.
// Tools with the same visual representation share a builder.
func (r *Registry) Register(registrations []Registration) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, reg := range registrations {
		for _, name := range reg.Names {
			r.builders[name] = reg.Builder
		}
	}
}

func (r *Registry) Get(toolName string) (ComponentBuilder, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	builder, exists := r.builders[toolName]
	return builder, exists
}
