package engine

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"testing"

	"github.com/open-nv/clisynth/pkg/outputfsm"
	"gopkg.in/yaml.v3"
)

type packFixtureFile struct {
	Spec struct {
		Platform string `yaml:"platform"`
		Command  string `yaml:"command"`
		Fixtures []struct {
			Inventory      map[string]any `yaml:"inventory"`
			ExpectedOutput string         `yaml:"expected_output"`
			ExpectedResult map[string]any `yaml:"expected_result"`
		} `yaml:"fixtures"`
	} `yaml:"spec"`
}

func TestSiblingOpenNVPacksAllFixturesRoundTrip(t *testing.T) {
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	packRoot := filepath.Clean(filepath.Join(filepath.Dir(filename), "..", "..", "..", "outputfsm-packs"))
	paths, err := filepath.Glob(filepath.Join(packRoot, "packs", "*", "commands", "*.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) == 0 {
		t.Skip("sibling outputfsm-packs checkout is unavailable")
	}
	sort.Strings(paths)
	definitions, err := outputfsm.LoadDir(packRoot)
	if err != nil {
		t.Fatal(err)
	}
	registry, err := NewRegistry(definitions)
	if err != nil {
		t.Fatal(err)
	}
	renderEngine, err := New(registry, Config{MaxConcurrency: 32})
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) != 160 || registry.Count() != 160 {
		t.Fatalf("pack count mismatch: files=%d registry=%d", len(paths), registry.Count())
	}
	for _, path := range paths {
		path := path
		t.Run(filepath.Base(filepath.Dir(filepath.Dir(path)))+"/"+filepath.Base(path), func(t *testing.T) {
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var packed packFixtureFile
			if err := yaml.Unmarshal(data, &packed); err != nil {
				t.Fatal(err)
			}
			if len(packed.Spec.Fixtures) == 0 {
				t.Fatal("definition has no fixture")
			}
			fixture := packed.Spec.Fixtures[0]
			result, err := renderEngine.Render(context.Background(), RenderRequest{
				Platform: packed.Spec.Platform,
				Command:  packed.Spec.Command,
				Device:   fixture.Inventory,
			})
			if err != nil {
				t.Fatal(err)
			}
			if result.Output != fixture.ExpectedOutput {
				t.Fatalf("output mismatch\ngot:\n%s\nwant:\n%s", result.Output, fixture.ExpectedOutput)
			}
			if !reflect.DeepEqual(result.Parsed, fixture.ExpectedResult) {
				t.Fatalf("parsed mismatch\ngot: %#v\nwant: %#v", result.Parsed, fixture.ExpectedResult)
			}
		})
	}
}
