package outputfsm

import (
	"os"
	"path/filepath"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestLoadDirLexicalOrder(t *testing.T) {
	directory := t.TempDir()
	for _, name := range []string{"b", "a"} {
		definition := validDefinition()
		definition.Metadata.Name = name
		definition.Spec.Command = "show " + name
		data, err := yaml.Marshal(definition)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(directory, name+".yaml"), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	definitions, err := LoadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	if len(definitions) != 2 || definitions[0].Metadata.Name != "a" || definitions[1].Metadata.Name != "b" {
		t.Fatalf("unexpected load order: %#v", definitions)
	}
}
