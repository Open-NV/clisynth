package outputfsm

import (
	"strings"
	"testing"
)

func validDefinition() Definition {
	return Definition{
		APIVersion: APIVersion,
		Kind:       Kind,
		Metadata:   Metadata{Name: "test-show-version"},
		Spec: Spec{
			Platform: "test_os",
			Command:  "show version",
			Template: "hostname={{ hostname }} serial={{ serial }} status={{ status }}\n",
			Variables: map[string]Variable{
				"hostname": {Source: "device.name", Required: true},
				"serial":   {Generator: &Generator{Kind: "hex", Length: 8}},
			},
			Conditions: []Condition{{When: `device.get("active", False)`, Set: map[string]any{"status": "up"}, Else: map[string]any{"status": "down"}}},
		},
	}
}

func TestDefinitionValidate(t *testing.T) {
	definition := validDefinition()
	if err := definition.Validate(); err != nil {
		t.Fatalf("valid definition rejected: %v", err)
	}
	definition.Spec.Template += "{{ missing }}"
	if err := definition.Validate(); err == nil || !strings.Contains(err.Error(), "has no variable") {
		t.Fatalf("expected unknown token error, got %v", err)
	}
}

func TestDecodeRejectsUnknownField(t *testing.T) {
	yaml := `apiVersion: clisynth.opennv.io/v1alpha1
kind: OutputFSM
metadata:
  name: strict-test
spec:
  platform: test
  command: show test
  templat: broken
  template: ok
`
	_, err := Decode([]byte(yaml))
	if err == nil || !strings.Contains(err.Error(), "field templat not found") {
		t.Fatalf("expected strict unknown-field error, got %v", err)
	}
}

func TestReplaceTemplateTokensIsLiteral(t *testing.T) {
	result, err := ReplaceTemplateTokens("x={{ value }} raw={{ raw }}", map[string]any{"value": 42, "raw": "{{not_evaluated}}"})
	if err != nil {
		t.Fatal(err)
	}
	if result != "x=42 raw={{not_evaluated}}" {
		t.Fatalf("unexpected result %q", result)
	}
}

func TestGeneratorValidation(t *testing.T) {
	cases := []struct {
		name      string
		generator Generator
		wantError bool
	}{
		{"integer", Generator{Kind: "integer", Min: -10, Max: 10}, false},
		{"bad range", Generator{Kind: "integer", Min: 10, Max: 1}, true},
		{"choice", Generator{Kind: "choice", Values: []any{"a", "b"}}, false},
		{"empty choice", Generator{Kind: "choice"}, true},
		{"hex", Generator{Kind: "hex", Length: 16}, false},
		{"odd mac prefix", Generator{Kind: "mac", Prefix: "abc"}, true},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			err := test.generator.Validate()
			if (err != nil) != test.wantError {
				t.Fatalf("Validate() error=%v, wantError=%v", err, test.wantError)
			}
		})
	}
}
