// Package engine resolves OutputFSM definitions and renders deterministic
// synthetic CLI output from inventory documents.
package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/open-nv/clisynth/pkg/inventory"
	"github.com/open-nv/clisynth/pkg/outputfsm"
)

const (
	DefaultMaxConcurrency = 64
	DefaultStarlarkSteps  = 100_000
	MaxBatchRequests      = 1_000_000
)

type Config struct {
	MaxConcurrency   int
	StarlarkMaxSteps uint64
}

type Engine struct {
	registry         *Registry
	maxConcurrency   int
	starlarkMaxSteps uint64
}

type RenderRequest struct {
	Platform string         `json:"platform,omitempty" yaml:"platform,omitempty"`
	Command  string         `json:"command" yaml:"command"`
	Device   map[string]any `json:"device" yaml:"device"`
	Seed     int64          `json:"seed,omitempty" yaml:"seed,omitempty"`
}

type RenderResult struct {
	Definition string         `json:"definition"`
	Platform   string         `json:"platform"`
	Command    string         `json:"command"`
	DeviceID   string         `json:"deviceId"`
	Output     string         `json:"output"`
	Variables  map[string]any `json:"variables,omitempty"`
	Parsed     map[string]any `json:"parsed,omitempty"`
}

type BatchRequest struct {
	Requests    []RenderRequest `json:"requests" yaml:"requests"`
	Concurrency int             `json:"concurrency,omitempty" yaml:"concurrency,omitempty"`
}

type BatchResult struct {
	Results []BatchItem  `json:"results"`
	Summary BatchSummary `json:"summary"`
}

type BatchItem struct {
	Index  int           `json:"index"`
	Result *RenderResult `json:"result,omitempty"`
	Error  *ResultError  `json:"error,omitempty"`
}

type ResultError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type BatchSummary struct {
	Total     int `json:"total"`
	Succeeded int `json:"succeeded"`
	Failed    int `json:"failed"`
}

func New(registry *Registry, config Config) (*Engine, error) {
	if registry == nil {
		return nil, errors.New("registry is required")
	}
	if config.MaxConcurrency == 0 {
		config.MaxConcurrency = DefaultMaxConcurrency
	}
	if config.MaxConcurrency < 1 || config.MaxConcurrency > 4096 {
		return nil, errors.New("max concurrency must be between 1 and 4096")
	}
	if config.StarlarkMaxSteps == 0 {
		config.StarlarkMaxSteps = DefaultStarlarkSteps
	}
	return &Engine{registry: registry, maxConcurrency: config.MaxConcurrency, starlarkMaxSteps: config.StarlarkMaxSteps}, nil
}

func (e *Engine) Registry() *Registry { return e.registry }

func (e *Engine) Render(ctx context.Context, request RenderRequest) (RenderResult, error) {
	if err := ctx.Err(); err != nil {
		return RenderResult{}, &Error{Code: CodeCanceled, Message: "render canceled", Cause: err}
	}
	if len(request.Device) == 0 {
		return RenderResult{}, &Error{Code: CodeInvalidRequest, Message: "device must not be empty"}
	}
	if strings.TrimSpace(request.Command) == "" {
		return RenderResult{}, &Error{Code: CodeInvalidRequest, Message: "command is required"}
	}
	platform := request.Platform
	if platform == "" {
		if value, ok := inventory.Get(request.Device, "platform"); ok {
			platform = fmt.Sprint(value)
		}
	}
	if strings.TrimSpace(platform) == "" {
		return RenderResult{}, &Error{Code: CodeInvalidRequest, Message: "platform is required in the request or device document"}
	}

	compiled, err := e.registry.resolveCompiled(platform, request.Command)
	if err != nil {
		return RenderResult{}, err
	}
	definition := compiled.definition
	deviceID := inventory.Identity(request.Device)
	effectiveDevice := mergeDocuments(definition.FixtureDefaults, request.Device)
	effectiveRequest := request
	effectiveRequest.Device = effectiveDevice
	variables, err := e.resolveVariables(effectiveRequest, definition, deviceID)
	if err != nil {
		return RenderResult{}, err
	}
	for index, condition := range definition.Spec.Conditions {
		matches, conditionErr := evalCondition(ctx, compiled.conditions[index], effectiveDevice, variables, request.Seed, e.starlarkMaxSteps)
		if conditionErr != nil {
			return RenderResult{}, &Error{Code: CodeProcessing, Message: fmt.Sprintf("condition %d failed", index), Cause: conditionErr}
		}
		assignments := condition.Else
		if matches {
			assignments = condition.Set
		}
		for name, value := range assignments {
			resolved, resolveErr := resolveAssignment(effectiveDevice, value)
			if resolveErr != nil {
				return RenderResult{}, &Error{Code: CodeInventoryField, Message: fmt.Sprintf("condition %d variable %q", index, name), Cause: resolveErr}
			}
			variables[name] = resolved
		}
	}
	if compiled.processor != nil {
		variables, err = runProcessor(ctx, compiled.processor, effectiveDevice, variables, request.Seed, e.starlarkMaxSteps)
		if err != nil {
			return RenderResult{}, &Error{Code: CodeProcessing, Message: "starlark processor failed", Cause: err}
		}
	}
	output, err := outputfsm.ReplaceTemplateTokens(definition.Spec.Template, variables)
	if err != nil {
		return RenderResult{}, &Error{Code: CodeProcessing, Message: "render template", Cause: err}
	}
	parsed, err := normalizedResult(definition.ResultSchema, effectiveDevice)
	if err != nil {
		return RenderResult{}, &Error{Code: CodeInventoryField, Message: "build normalized result", Cause: err}
	}
	return RenderResult{
		Definition: definition.Metadata.Name,
		Platform:   definition.Spec.Platform,
		Command:    definition.Spec.Command,
		DeviceID:   deviceID,
		Output:     output,
		Variables:  variables,
		Parsed:     parsed,
	}, nil
}

func (e *Engine) resolveVariables(request RenderRequest, definition outputfsm.Definition, deviceID string) (map[string]any, error) {
	variables := make(map[string]any, len(definition.Spec.Variables))
	names := make([]string, 0, len(definition.Spec.Variables))
	for name := range definition.Spec.Variables {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		variable := definition.Spec.Variables[name]
		var value any
		var found bool
		switch {
		case variable.Source != "":
			value, found = inventory.Get(request.Device, variable.Source)
			if !found && variable.Default != nil {
				value, found = variable.Default, true
			}
		case variable.Generator != nil:
			var err error
			if variable.Generator.Source != "" {
				var sourceValue any
				sourceValue, found = inventory.Get(request.Device, variable.Generator.Source)
				if !found {
					return nil, &Error{Code: CodeInventoryField, Message: fmt.Sprintf("generator source %q for variable %q is missing", variable.Generator.Source, name)}
				}
				value, err = pureGenerate(*variable.Generator, sourceValue)
			} else {
				value, err = generate(*variable.Generator, fmt.Sprintf("%d\x00%s\x00%s\x00%s", request.Seed, deviceID, definition.Metadata.Name, name))
			}
			if err != nil {
				return nil, &Error{Code: CodeProcessing, Message: fmt.Sprintf("generate variable %q", name), Cause: err}
			}
			found = true
		default:
			value, found = variable.Default, variable.Default != nil
		}
		if !found {
			if variable.Required {
				return nil, &Error{Code: CodeInventoryField, Message: fmt.Sprintf("required inventory field %q for variable %q is missing", variable.Source, name)}
			}
			value = ""
		}
		transformed, err := transform(value, variable.Transform)
		if err != nil {
			return nil, &Error{Code: CodeProcessing, Message: fmt.Sprintf("transform variable %q", name), Cause: err}
		}
		variables[name] = transformed
	}
	return variables, nil
}

func mergeDocuments(defaults, overrides map[string]any) map[string]any {
	result := make(map[string]any, len(defaults)+len(overrides))
	for key, value := range defaults {
		result[key] = cloneValue(value)
	}
	for key, value := range overrides {
		if overrideMap, ok := value.(map[string]any); ok {
			if defaultMap, exists := result[key].(map[string]any); exists {
				result[key] = mergeDocuments(defaultMap, overrideMap)
				continue
			}
		}
		result[key] = cloneValue(value)
	}
	return result
}

func cloneValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		return mergeDocuments(nil, typed)
	case []any:
		copyOfSlice := make([]any, len(typed))
		for index, item := range typed {
			copyOfSlice[index] = cloneValue(item)
		}
		return copyOfSlice
	default:
		return typed
	}
}

func normalizedResult(fields []outputfsm.ResultField, device map[string]any) (map[string]any, error) {
	if len(fields) == 0 {
		return nil, nil
	}
	result := make(map[string]any)
	for _, field := range fields {
		value, ok := inventory.Get(device, field.Source)
		if !ok {
			return nil, fmt.Errorf("result source %q for path %q is missing", field.Source, field.Path)
		}
		if err := setResultPath(result, field.Path, cloneValue(value)); err != nil {
			return nil, err
		}
	}
	return result, nil
}

func setResultPath(document map[string]any, path string, value any) error {
	parts := strings.Split(path, ".")
	if len(parts) < 2 {
		return fmt.Errorf("result path %q must contain at least two segments", path)
	}
	current := document
	for _, part := range parts[:len(parts)-1] {
		if part == "" {
			return fmt.Errorf("result path %q contains an empty segment", path)
		}
		next, ok := current[part]
		if !ok {
			child := make(map[string]any)
			current[part] = child
			current = child
			continue
		}
		child, ok := next.(map[string]any)
		if !ok {
			return fmt.Errorf("result path %q conflicts at %q", path, part)
		}
		current = child
	}
	leaf := parts[len(parts)-1]
	if leaf == "" {
		return fmt.Errorf("result path %q contains an empty segment", path)
	}
	if _, exists := current[leaf]; exists {
		return fmt.Errorf("duplicate result path %q", path)
	}
	current[leaf] = value
	return nil
}

func (e *Engine) RenderBatch(ctx context.Context, request BatchRequest) (BatchResult, error) {
	if len(request.Requests) == 0 {
		return BatchResult{}, &Error{Code: CodeInvalidRequest, Message: "requests must not be empty"}
	}
	if len(request.Requests) > MaxBatchRequests {
		return BatchResult{}, &Error{Code: CodeInvalidRequest, Message: fmt.Sprintf("batch exceeds maximum of %d requests", MaxBatchRequests)}
	}
	concurrency := request.Concurrency
	if concurrency == 0 || concurrency > e.maxConcurrency {
		concurrency = e.maxConcurrency
	}
	if concurrency < 1 {
		return BatchResult{}, &Error{Code: CodeInvalidRequest, Message: "concurrency must be positive"}
	}

	items := make([]BatchItem, len(request.Requests))
	jobs := make(chan int)
	var workers sync.WaitGroup
	for worker := 0; worker < concurrency; worker++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for index := range jobs {
				result, err := e.Render(ctx, request.Requests[index])
				item := BatchItem{Index: index}
				if err != nil {
					item.Error = &ResultError{Code: errorCode(err), Message: err.Error()}
				} else {
					item.Result = &result
				}
				items[index] = item
			}
		}()
	}
	for index := range request.Requests {
		select {
		case jobs <- index:
		case <-ctx.Done():
			close(jobs)
			workers.Wait()
			return BatchResult{}, &Error{Code: CodeCanceled, Message: "batch canceled", Cause: ctx.Err()}
		}
	}
	close(jobs)
	workers.Wait()

	result := BatchResult{Results: items, Summary: BatchSummary{Total: len(items)}}
	for _, item := range items {
		if item.Error == nil {
			result.Summary.Succeeded++
		} else {
			result.Summary.Failed++
		}
	}
	return result, nil
}

func resolveAssignment(device map[string]any, value any) (any, error) {
	if object, ok := value.(map[string]any); ok {
		if path, exists := object["from"]; exists {
			resolved, found := inventory.Get(device, fmt.Sprint(path))
			if !found {
				if fallback, hasDefault := object["default"]; hasDefault {
					return fallback, nil
				}
				return nil, fmt.Errorf("inventory field %q is missing", path)
			}
			return resolved, nil
		}
	}
	return value, nil
}

func transform(value any, operation string) (any, error) {
	switch operation {
	case "":
		return value, nil
	case "string":
		return fmt.Sprint(value), nil
	case "lower":
		return strings.ToLower(fmt.Sprint(value)), nil
	case "upper":
		return strings.ToUpper(fmt.Sprint(value)), nil
	case "trim":
		return strings.TrimSpace(fmt.Sprint(value)), nil
	case "json":
		data, err := json.Marshal(value)
		return string(data), err
	default:
		return nil, fmt.Errorf("unsupported transform %q", operation)
	}
}
