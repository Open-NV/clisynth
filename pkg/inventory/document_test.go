package inventory

import "testing"

func TestGet(t *testing.T) {
	document := map[string]any{
		"name":      "router-1",
		"site":      map[string]any{"name": "dfw"},
		"addresses": []any{map[string]any{"address": "192.0.2.1"}},
	}
	tests := []struct {
		path string
		want any
		ok   bool
	}{
		{"device.name", "router-1", true},
		{"site.name", "dfw", true},
		{"addresses.0.address", "192.0.2.1", true},
		{"addresses.2.address", nil, false},
	}
	for _, test := range tests {
		got, ok := Get(document, test.path)
		if ok != test.ok || got != test.want {
			t.Errorf("Get(%q) = (%v, %v), want (%v, %v)", test.path, got, ok, test.want, test.ok)
		}
	}
}

func TestIdentityPreference(t *testing.T) {
	if got := Identity(map[string]any{"id": "42", "name": "router"}); got != "42" {
		t.Fatalf("Identity()=%q, want 42", got)
	}
	if got := Identity(map[string]any{"name": "router"}); got != "router" {
		t.Fatalf("Identity()=%q, want router", got)
	}
}
