package main

import (
	"bytes"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func examplePath(t *testing.T, parts ...string) string {
	t.Helper()
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(filename), "..", ".."))
	return filepath.Join(append([]string{root}, parts...)...)
}

func TestValidateListAndRenderCommands(t *testing.T) {
	definitions := examplePath(t, "examples", "definitions")
	inventory := examplePath(t, "examples", "inventory", "router01.json")
	tests := []struct {
		name     string
		args     []string
		contains string
	}{
		{"validate", []string{"validate", "--definitions", definitions}, "validated 2 OutputFSM definitions"},
		{"list", []string{"list", "--definitions", definitions}, "cisco_ios\tshow version"},
		{"render", []string{"render", "--definitions", definitions, "--inventory", inventory, "--command", "show version", "--seed", "42"}, "EDGE-RTR-01 uptime is 92 days"},
		{"version", []string{"version"}, "clisynth dev"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if err := run(test.args, &stdout, &stderr); err != nil {
				t.Fatalf("run failed: %v, stderr=%s", err, stderr.String())
			}
			if !strings.Contains(stdout.String(), test.contains) {
				t.Fatalf("stdout %q does not contain %q", stdout.String(), test.contains)
			}
		})
	}
}

func TestDeploymentEnvironmentDefaults(t *testing.T) {
	t.Setenv("OUTPUTFSM_ROOT", "/packs")
	t.Setenv("CLISYNTH_DEFINITIONS", "/legacy")
	t.Setenv("PORT", "8090")
	t.Setenv("CLISYNTH_ADDR", "")
	if got := definitionsDefault(); got != "/packs" {
		t.Fatalf("definitionsDefault()=%q, want /packs", got)
	}
	if got := addressDefault(); got != ":8090" {
		t.Fatalf("addressDefault()=%q, want :8090", got)
	}
	t.Setenv("CLISYNTH_ADDR", "127.0.0.1:9999")
	if got := addressDefault(); got != "127.0.0.1:9999" {
		t.Fatalf("address override=%q", got)
	}
}
