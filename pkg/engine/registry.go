package engine

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/open-nv/clisynth/pkg/outputfsm"
)

type definitionKey struct {
	platform string
	command  string
}

type DefinitionSummary struct {
	Name              string   `json:"name"`
	Version           string   `json:"version,omitempty"`
	Description       string   `json:"description,omitempty"`
	Platform          string   `json:"platform"`
	PlatformAliases   []string `json:"platformAliases,omitempty"`
	Command           string   `json:"command"`
	CommandAliases    []string `json:"commandAliases,omitempty"`
	TemplateVariables []string `json:"templateVariables"`
}

// Registry is a concurrency-safe immutable-snapshot definition resolver.
type Registry struct {
	mu          sync.RWMutex
	definitions []compiledDefinition
	byKey       map[definitionKey]int
}

func NewRegistry(definitions []outputfsm.Definition) (*Registry, error) {
	registry := &Registry{}
	if err := registry.Replace(definitions); err != nil {
		return nil, err
	}
	return registry, nil
}

// Replace validates a complete new snapshot before atomically publishing it.
func (r *Registry) Replace(definitions []outputfsm.Definition) error {
	if len(definitions) == 0 {
		return errors.New("at least one OutputFSM definition is required")
	}
	copyOfDefinitions := append([]outputfsm.Definition(nil), definitions...)
	sort.Slice(copyOfDefinitions, func(i, j int) bool {
		return copyOfDefinitions[i].Metadata.Name < copyOfDefinitions[j].Metadata.Name
	})
	byKey := make(map[definitionKey]int)
	byName := make(map[string]string)
	compiledDefinitions := make([]compiledDefinition, 0, len(copyOfDefinitions))
	for index := range copyOfDefinitions {
		definition := &copyOfDefinitions[index]
		if err := definition.Validate(); err != nil {
			return fmt.Errorf("definition %q: %w", definition.Metadata.Name, err)
		}
		if source, ok := byName[definition.Metadata.Name]; ok {
			return fmt.Errorf("duplicate definition name %q in %q and %q", definition.Metadata.Name, source, definition.Source)
		}
		byName[definition.Metadata.Name] = definition.Source
		compiled, err := compileDefinition(*definition)
		if err != nil {
			return fmt.Errorf("definition %q: %w", definition.Metadata.Name, err)
		}
		compiledDefinitions = append(compiledDefinitions, compiled)

		platforms := append([]string{definition.Spec.Platform}, definition.Spec.PlatformAliases...)
		commands := append([]string{definition.Spec.Command}, definition.Spec.CommandAliases...)
		for _, platform := range platforms {
			for _, command := range commands {
				key := definitionKey{platform: normalize(platform), command: normalize(command)}
				if existing, ok := byKey[key]; ok {
					return fmt.Errorf("definition key platform=%q command=%q conflicts between %q and %q", platform, command, compiledDefinitions[existing].definition.Metadata.Name, definition.Metadata.Name)
				}
				byKey[key] = index
			}
		}
	}

	r.mu.Lock()
	r.definitions = compiledDefinitions
	r.byKey = byKey
	r.mu.Unlock()
	return nil
}

func (r *Registry) Resolve(platform, command string) (outputfsm.Definition, error) {
	key := definitionKey{platform: normalize(platform), command: normalize(command)}
	r.mu.RLock()
	index, ok := r.byKey[key]
	if !ok {
		r.mu.RUnlock()
		return outputfsm.Definition{}, &Error{
			Code:    CodeDefinitionNotFound,
			Message: fmt.Sprintf("no OutputFSM definition for platform %q and command %q", platform, command),
		}
	}
	definition := r.definitions[index].definition
	r.mu.RUnlock()
	return definition, nil
}

func (r *Registry) resolveCompiled(platform, command string) (compiledDefinition, error) {
	key := definitionKey{platform: normalize(platform), command: normalize(command)}
	r.mu.RLock()
	index, ok := r.byKey[key]
	if !ok {
		r.mu.RUnlock()
		return compiledDefinition{}, &Error{
			Code:    CodeDefinitionNotFound,
			Message: fmt.Sprintf("no OutputFSM definition for platform %q and command %q", platform, command),
		}
	}
	definition := r.definitions[index]
	r.mu.RUnlock()
	return definition, nil
}

func (r *Registry) List() []DefinitionSummary {
	r.mu.RLock()
	summaries := make([]DefinitionSummary, 0, len(r.definitions))
	for _, compiled := range r.definitions {
		definition := compiled.definition
		summaries = append(summaries, DefinitionSummary{
			Name:              definition.Metadata.Name,
			Version:           definition.Metadata.Version,
			Description:       definition.Metadata.Description,
			Platform:          definition.Spec.Platform,
			PlatformAliases:   append([]string(nil), definition.Spec.PlatformAliases...),
			Command:           definition.Spec.Command,
			CommandAliases:    append([]string(nil), definition.Spec.CommandAliases...),
			TemplateVariables: definition.TemplateVariables(),
		})
	}
	r.mu.RUnlock()
	return summaries
}

func (r *Registry) Count() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.definitions)
}

func normalize(value string) string {
	return strings.ToLower(strings.Join(strings.Fields(value), " "))
}
