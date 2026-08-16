package outputfsm

import (
	"strings"
	"testing"
)

func TestDecodeOpenNVPack(t *testing.T) {
	data := []byte(`apiVersion: opennv.io/v1alpha1
kind: OutputFSM
metadata:
  id: show_version
  title: Test OS show version
  description: Synthetic fixture
  license: Apache-2.0
  synthetic: true
  category: system
spec:
  platform: test_os
  platform_aliases: [test]
  command: show version
  aliases: [sh version]
  variables:
    hostname: {source: inventory.hostname}
    command: {literal: show version}
    uptime:
      generator: {name: uptime_human, source: inventory.system.uptime_seconds}
  template: |
    ${hostname}#${command}
    uptime ${uptime}
  result_schema:
    - {path: system.uptime_seconds, type: integer, source: inventory.system.uptime_seconds}
  fixtures:
    - name: healthy
      description: synthetic
      inventory:
        hostname: demo
        system: {uptime_seconds: 90061}
      expected_output: |
        demo#show version
        uptime 1d 01:01:01
      expected_result:
        system: {uptime_seconds: 90061}
`)
	definition, err := Decode(data)
	if err != nil {
		t.Fatal(err)
	}
	if definition.Metadata.Name != "test-os-show-version" || definition.Spec.Platform != "test_os" {
		t.Fatalf("unexpected converted definition %#v", definition)
	}
	if definition.Spec.Template != "{{ hostname }}#{{ command }}\nuptime {{ uptime }}\n" {
		t.Fatalf("unexpected converted template %q", definition.Spec.Template)
	}
	if definition.Spec.Variables["uptime"].Generator.Kind != "uptime_human" {
		t.Fatalf("generator was not converted: %#v", definition.Spec.Variables["uptime"])
	}
}

func TestDecodeOpenNVPackRejectsUnsafeTemplate(t *testing.T) {
	data := []byte(strings.ReplaceAll(`apiVersion: opennv.io/v1alpha1
kind: OutputFSM
metadata: {id: test, title: test, description: test, license: Apache-2.0, synthetic: true, category: test}
spec:
  platform: test_os
  command: show test
  aliases: []
  variables: {value: {literal: ok}}
  template: "$((shell))"
  result_schema: [{path: test.value, type: string, source: inventory.test.value}]
  fixtures: [{name: test, description: test, inventory: {test: {value: ok}}, expected_output: "", expected_result: {}}]
`, "\t", "  "))
	if _, err := Decode(data); err == nil || !strings.Contains(err.Error(), "unsupported dollar") {
		t.Fatalf("expected unsafe-template error, got %v", err)
	}
}

func TestConvertDollarTemplateEscapesLiteralDollar(t *testing.T) {
	converted, err := convertDollarTemplate("cost $$5 for ${item} or $other")
	if err != nil {
		t.Fatal(err)
	}
	if converted != "cost $5 for {{ item }} or {{ other }}" {
		t.Fatalf("unexpected converted template %q", converted)
	}
}
