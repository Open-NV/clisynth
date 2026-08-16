package engine

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/open-nv/clisynth/pkg/outputfsm"
)

func testDefinition() outputfsm.Definition {
	return outputfsm.Definition{
		APIVersion: outputfsm.APIVersion,
		Kind:       outputfsm.Kind,
		Metadata:   outputfsm.Metadata{Name: "test-show-version", Version: "1.0.0"},
		Spec: outputfsm.Spec{
			Platform:        "test_os",
			PlatformAliases: []string{"test"},
			Command:         "show version",
			CommandAliases:  []string{"sh ver"},
			Variables: map[string]outputfsm.Variable{
				"hostname": {Source: "device.name", Required: true, Transform: "upper"},
				"serial":   {Generator: &outputfsm.Generator{Kind: "hex", Prefix: "SN", Length: 10}},
				"version":  {Source: "device.version", Default: "1.0"},
				"state":    {Default: "unknown"},
				"label":    {Default: ""},
			},
			Conditions: []outputfsm.Condition{{
				When: `device.get("active", False)`,
				Set:  map[string]any{"state": "up"},
				Else: map[string]any{"state": "down"},
			}},
			Starlark: `def process(device, vars, seed):
    vars["label"] = vars["hostname"] + ":" + str(seed)
    return vars
`,
			Template: "{{ hostname }} {{ version }} {{ serial }} {{ state }} {{ label }}\n",
		},
	}
}

func newTestEngine(t testing.TB, definition outputfsm.Definition, config Config) *Engine {
	t.Helper()
	registry, err := NewRegistry([]outputfsm.Definition{definition})
	if err != nil {
		t.Fatal(err)
	}
	renderEngine, err := New(registry, config)
	if err != nil {
		t.Fatal(err)
	}
	return renderEngine
}

func TestRenderDeterministicAndAliasResolution(t *testing.T) {
	renderEngine := newTestEngine(t, testDefinition(), Config{})
	request := RenderRequest{
		Platform: " TEST ",
		Command:  "  SH   VER ",
		Seed:     99,
		Device:   map[string]any{"id": "device-1", "name": "router-1", "version": "2.0", "active": true},
	}
	first, err := renderEngine.Render(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	second, err := renderEngine.Render(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if first.Output != second.Output {
		t.Fatalf("same input was not deterministic:\n%s\n%s", first.Output, second.Output)
	}
	if !strings.Contains(first.Output, "ROUTER-1 2.0 SN") || !strings.Contains(first.Output, "up ROUTER-1:99") {
		t.Fatalf("unexpected output %q", first.Output)
	}
	request.Device["id"] = "device-2"
	third, err := renderEngine.Render(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if first.Variables["serial"] == third.Variables["serial"] {
		t.Fatal("different device identity generated the same serial")
	}
}

func TestRenderMissingRequiredField(t *testing.T) {
	renderEngine := newTestEngine(t, testDefinition(), Config{})
	_, err := renderEngine.Render(context.Background(), RenderRequest{Platform: "test_os", Command: "show version", Device: map[string]any{"id": "1"}})
	var engineErr *Error
	if !errors.As(err, &engineErr) || engineErr.Code != CodeInventoryField {
		t.Fatalf("got %v, want inventory-field engine error", err)
	}
}

func TestRenderBatchPreservesInputOrderAndIsolatesErrors(t *testing.T) {
	renderEngine := newTestEngine(t, testDefinition(), Config{MaxConcurrency: 8})
	requests := make([]RenderRequest, 100)
	for index := range requests {
		requests[index] = RenderRequest{Command: "show version", Device: map[string]any{"id": index, "name": string(rune('a' + index%26)), "platform": "test_os"}}
	}
	requests[50].Command = "show missing"
	result, err := renderEngine.RenderBatch(context.Background(), BatchRequest{Requests: requests, Concurrency: 4})
	if err != nil {
		t.Fatal(err)
	}
	if result.Summary.Total != 100 || result.Summary.Succeeded != 99 || result.Summary.Failed != 1 {
		t.Fatalf("unexpected summary %#v", result.Summary)
	}
	for index, item := range result.Results {
		if item.Index != index {
			t.Fatalf("result %d has index %d", index, item.Index)
		}
	}
	if result.Results[50].Error == nil || result.Results[50].Error.Code != CodeDefinitionNotFound {
		t.Fatalf("unexpected failed item %#v", result.Results[50])
	}
}

func TestStarlarkExecutionLimit(t *testing.T) {
	definition := testDefinition()
	definition.Spec.Starlark = `def process(device, vars, seed):
    total = 0
    for i in range(1000000):
        total += i
    return vars
`
	renderEngine := newTestEngine(t, definition, Config{StarlarkMaxSteps: 100})
	_, err := renderEngine.Render(context.Background(), RenderRequest{Platform: "test_os", Command: "show version", Device: map[string]any{"name": "router"}})
	if err == nil || !strings.Contains(err.Error(), "too many steps") {
		t.Fatalf("expected Starlark step limit error, got %v", err)
	}
}

func TestRegistryRejectsConflictingAlias(t *testing.T) {
	first := testDefinition()
	second := testDefinition()
	second.Metadata.Name = "second"
	_, err := NewRegistry([]outputfsm.Definition{first, second})
	if err == nil || !strings.Contains(err.Error(), "conflicts") {
		t.Fatalf("expected conflict, got %v", err)
	}
}

func TestRegistryRejectsUnsafeOrInvalidProcessor(t *testing.T) {
	tests := []struct {
		name   string
		script string
	}{
		{"top-level execution", "value = 1\ndef process(device, vars, seed):\n    return vars\n"},
		{"load", "load(\"module.star\", \"thing\")\ndef process(device, vars, seed):\n    return vars\n"},
		{"wrong signature", "def process(vars):\n    return vars\n"},
		{"syntax", "def process(device, vars, seed)\n    return vars\n"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			definition := testDefinition()
			definition.Spec.Starlark = test.script
			if _, err := NewRegistry([]outputfsm.Definition{definition}); err == nil {
				t.Fatal("unsafe or invalid processor was accepted")
			}
		})
	}
}
