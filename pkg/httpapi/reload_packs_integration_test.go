package httpapi

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"testing"
)

func TestReloadCompleteSiblingOpenNVPackSnapshot(t *testing.T) {
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
	documents := make([]ReloadDocument, 0, len(paths))
	for _, definitionPath := range paths {
		data, err := os.ReadFile(definitionPath)
		if err != nil {
			t.Fatal(err)
		}
		source, err := filepath.Rel(packRoot, definitionPath)
		if err != nil {
			t.Fatal(err)
		}
		documents = append(documents, ReloadDocument{
			Source: filepath.ToSlash(source),
			YAML:   string(data),
		})
	}
	bundle := completeBundle("sibling-outputfsm-packs", documents...)
	api := reloadAPI(t, Config{})
	response := performReload(t, api, bundle, testReloadToken)
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	var result ReloadResponse
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Count != len(documents) || result.Count != 160 || result.Digest != bundle.Digest {
		t.Fatalf("unexpected response %#v for %d documents", result, len(documents))
	}
}
