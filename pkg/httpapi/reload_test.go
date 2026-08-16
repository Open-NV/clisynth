package httpapi

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/open-nv/clisynth/pkg/engine"
	"github.com/open-nv/clisynth/pkg/outputfsm"
)

const testReloadToken = "test-reload-token-that-is-at-least-32-bytes"

func reloadDefinitionYAML(name, value string) string {
	return fmt.Sprintf(`apiVersion: clisynth.opennv.io/v1alpha1
kind: OutputFSM
metadata:
  name: %s
spec:
  platform: reload_os
  command: show state
  variables:
    state:
      default: %s
  template: |
    state={{ state }}
`, name, value)
}

func reloadAPI(t *testing.T, config Config) *API {
	t.Helper()
	definition, err := outputfsm.Decode([]byte(reloadDefinitionYAML("snapshot-initial", "initial")))
	if err != nil {
		t.Fatal(err)
	}
	registry, err := engine.NewRegistry([]outputfsm.Definition{definition})
	if err != nil {
		t.Fatal(err)
	}
	renderEngine, err := engine.New(registry, engine.Config{MaxConcurrency: 8})
	if err != nil {
		t.Fatal(err)
	}
	config.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	config.ReloadToken = testReloadToken
	api, err := New(renderEngine, config)
	if err != nil {
		t.Fatal(err)
	}
	return api
}

func completeBundle(revision string, documents ...ReloadDocument) ReloadBundle {
	bundle := ReloadBundle{
		APIVersion:    reloadAPIVersion,
		Kind:          reloadKind,
		Revision:      revision,
		Complete:      true,
		ExpectedCount: len(documents),
		Definitions:   documents,
	}
	bundle.Digest = ComputeReloadDigest(bundle.Definitions)
	return bundle
}

func performReload(t *testing.T, api *API, bundle ReloadBundle, token string) *httptest.ResponseRecorder {
	t.Helper()
	payload, err := json.Marshal(bundle)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPut, "/v1/admin/definitions", bytes.NewReader(payload))
	request.Header.Set("Content-Type", "application/json")
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	response := httptest.NewRecorder()
	api.Handler().ServeHTTP(response, request)
	return response
}

func renderReloadState(api *API) (int, string) {
	request := httptest.NewRequest(http.MethodPost, "/v1/render", strings.NewReader(`{"command":"show state","device":{"id":"one","platform":"reload_os"}}`))
	response := httptest.NewRecorder()
	api.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		return response.Code, response.Body.String()
	}
	var result engine.RenderResult
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		return http.StatusInternalServerError, err.Error()
	}
	return response.Code, result.Output
}

func TestReloadRequiresDedicatedBearerToken(t *testing.T) {
	api := reloadAPI(t, Config{})
	bundle := completeBundle("git-auth", ReloadDocument{Source: "packs/reload/show_state.yaml", YAML: reloadDefinitionYAML("snapshot-auth", "auth")})

	for _, test := range []struct {
		name   string
		token  string
		status int
	}{
		{name: "missing", status: http.StatusUnauthorized},
		{name: "wrong", token: strings.Repeat("x", 40), status: http.StatusUnauthorized},
		{name: "valid", token: testReloadToken, status: http.StatusOK},
	} {
		t.Run(test.name, func(t *testing.T) {
			response := performReload(t, api, bundle, test.token)
			if response.Code != test.status {
				t.Fatalf("status=%d body=%s, want %d", response.Code, response.Body.String(), test.status)
			}
			if test.status == http.StatusUnauthorized && response.Header().Get("WWW-Authenticate") == "" {
				t.Fatal("unauthorized response is missing WWW-Authenticate")
			}
		})
	}
}

func TestReloadRouteIsAbsentWhenTokenIsUnset(t *testing.T) {
	definition, err := outputfsm.Decode([]byte(reloadDefinitionYAML("snapshot-disabled", "disabled")))
	if err != nil {
		t.Fatal(err)
	}
	registry, err := engine.NewRegistry([]outputfsm.Definition{definition})
	if err != nil {
		t.Fatal(err)
	}
	renderEngine, err := engine.New(registry, engine.Config{})
	if err != nil {
		t.Fatal(err)
	}
	api, err := New(renderEngine, Config{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPut, "/v1/admin/definitions", strings.NewReader(`{}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	api.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status=%d body=%s, want disabled route", response.Code, response.Body.String())
	}
}

func TestReloadValidBundleAtomicallyReplacesRegistry(t *testing.T) {
	api := reloadAPI(t, Config{})
	document := ReloadDocument{Source: "packs/reload/commands/show_state.yaml", YAML: reloadDefinitionYAML("snapshot-next", "next")}
	bundle := completeBundle("0123456789abcdef", document)

	response := performReload(t, api, bundle, testReloadToken)
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	var result ReloadResponse
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Count != 1 || result.Digest != bundle.Digest || result.Revision != bundle.Revision {
		t.Fatalf("unexpected response %#v", result)
	}
	if status, output := renderReloadState(api); status != http.StatusOK || output != "state=next\n" {
		t.Fatalf("render status=%d output=%q", status, output)
	}
}

func TestReloadInvalidCompiledBundleLeavesRegistryUnchanged(t *testing.T) {
	api := reloadAPI(t, Config{})
	invalid := reloadDefinitionYAML("snapshot-invalid", "invalid") + "  starlark: |\n    def process(:\n"
	bundle := completeBundle(
		"git-invalid",
		ReloadDocument{Source: "packs/reload/commands/good.yaml", YAML: reloadDefinitionYAML("snapshot-good", "good")},
		ReloadDocument{Source: "packs/reload/commands/invalid.yaml", YAML: invalid},
	)

	response := performReload(t, api, bundle, testReloadToken)
	if response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if status, output := renderReloadState(api); status != http.StatusOK || output != "state=initial\n" {
		t.Fatalf("invalid bundle changed registry: status=%d output=%q", status, output)
	}
}

func TestReloadRejectsPartialDuplicateAndMismatchedBundles(t *testing.T) {
	document := ReloadDocument{Source: "packs/reload/commands/show_state.yaml", YAML: reloadDefinitionYAML("snapshot-check", "check")}
	base := completeBundle("git-check", document)
	duplicate := completeBundle("git-duplicate", document, document)
	partial := base
	partial.Complete = false
	wrongCount := base
	wrongCount.ExpectedCount = 2
	wrongDigest := base
	wrongDigest.Digest = "sha256:" + strings.Repeat("0", 64)

	for _, test := range []struct {
		name   string
		bundle ReloadBundle
	}{
		{name: "partial", bundle: partial},
		{name: "wrong count", bundle: wrongCount},
		{name: "wrong digest", bundle: wrongDigest},
		{name: "duplicate source", bundle: duplicate},
	} {
		t.Run(test.name, func(t *testing.T) {
			api := reloadAPI(t, Config{})
			response := performReload(t, api, test.bundle, testReloadToken)
			if response.Code != http.StatusUnprocessableEntity {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
			if status, output := renderReloadState(api); status != http.StatusOK || output != "state=initial\n" {
				t.Fatalf("rejected bundle changed registry: status=%d output=%q", status, output)
			}
		})
	}
}

func TestReloadEnforcesBodyAndDefinitionCountLimits(t *testing.T) {
	document := ReloadDocument{Source: "packs/reload/commands/show_state.yaml", YAML: reloadDefinitionYAML("snapshot-size", "size")}
	bundle := completeBundle("git-size", document)

	t.Run("body", func(t *testing.T) {
		api := reloadAPI(t, Config{MaxReloadBodyBytes: 128})
		response := performReload(t, api, bundle, testReloadToken)
		if response.Code != http.StatusRequestEntityTooLarge {
			t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
		}
	})

	t.Run("count", func(t *testing.T) {
		api := reloadAPI(t, Config{MaxReloadDefinitions: 1})
		second := ReloadDocument{Source: "packs/other/commands/show_other.yaml", YAML: strings.ReplaceAll(reloadDefinitionYAML("snapshot-other", "other"), "show state", "show other")}
		oversized := completeBundle("git-count", document, second)
		response := performReload(t, api, oversized, testReloadToken)
		if response.Code != http.StatusUnprocessableEntity {
			t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
		}
	})

	t.Run("one yaml document", func(t *testing.T) {
		api := reloadAPI(t, Config{})
		tooLarge := ReloadDocument{
			Source: "packs/reload/commands/too_large.yaml",
			YAML:   strings.Repeat("x", outputfsm.MaxDefinitionBytes+1),
		}
		response := performReload(t, api, completeBundle("git-document-size", tooLarge), testReloadToken)
		if response.Code != http.StatusUnprocessableEntity {
			t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
		}
	})
}

func TestReloadDigestIsIndependentOfDocumentOrder(t *testing.T) {
	one := ReloadDocument{Source: "packs/a/commands/one.yaml", YAML: "one\n"}
	two := ReloadDocument{Source: "packs/b/commands/two.yaml", YAML: "two\n"}
	if left, right := ComputeReloadDigest([]ReloadDocument{one, two}), ComputeReloadDigest([]ReloadDocument{two, one}); left != right {
		t.Fatalf("digest depends on bundle ordering: %s != %s", left, right)
	}
}

func TestReloadConcurrentRendersObserveOnlyCompleteSnapshots(t *testing.T) {
	api := reloadAPI(t, Config{})
	bundleA := completeBundle("git-a", ReloadDocument{Source: "packs/reload/commands/show_state.yaml", YAML: reloadDefinitionYAML("snapshot-a", "A")})
	bundleB := completeBundle("git-b", ReloadDocument{Source: "packs/reload/commands/show_state.yaml", YAML: reloadDefinitionYAML("snapshot-b", "B")})

	var wait sync.WaitGroup
	errorsFound := make(chan string, 32)
	for worker := 0; worker < 4; worker++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			for index := 0; index < 100; index++ {
				status, output := renderReloadState(api)
				if status != http.StatusOK || (output != "state=initial\n" && output != "state=A\n" && output != "state=B\n") {
					errorsFound <- fmt.Sprintf("render status=%d output=%q", status, output)
					return
				}
			}
		}()
	}
	for worker := 0; worker < 2; worker++ {
		wait.Add(1)
		go func(offset int) {
			defer wait.Done()
			for index := 0; index < 25; index++ {
				bundle := bundleA
				if (index+offset)%2 == 1 {
					bundle = bundleB
				}
				response := performReload(t, api, bundle, testReloadToken)
				if response.Code != http.StatusOK {
					errorsFound <- fmt.Sprintf("reload status=%d body=%s", response.Code, response.Body.String())
					return
				}
			}
		}(worker)
	}
	wait.Wait()
	close(errorsFound)
	for failure := range errorsFound {
		t.Error(failure)
	}
}

func TestNewRejectsWeakReloadToken(t *testing.T) {
	definition, err := outputfsm.Decode([]byte(reloadDefinitionYAML("snapshot-weak", "weak")))
	if err != nil {
		t.Fatal(err)
	}
	registry, err := engine.NewRegistry([]outputfsm.Definition{definition})
	if err != nil {
		t.Fatal(err)
	}
	renderEngine, err := engine.New(registry, engine.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := New(renderEngine, Config{ReloadToken: "too-short"}); err == nil {
		t.Fatal("expected a weak reload token to be rejected")
	}
}
