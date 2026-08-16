package httpapi

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/open-nv/clisynth/pkg/engine"
	"github.com/open-nv/clisynth/pkg/outputfsm"
)

func siblingPackRoot(t *testing.T) string {
	t.Helper()
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(filename), "..", "..", "..", "outputfsm-packs"))
	if _, err := os.Stat(filepath.Join(root, "packs", "cisco_ios", "commands", "show_version.yaml")); err != nil {
		t.Skipf("sibling outputfsm-packs checkout is unavailable: %v", err)
	}
	return root
}

func siblingPackAPI(t *testing.T) *API {
	t.Helper()
	definitions, err := outputfsm.LoadDir(siblingPackRoot(t))
	if err != nil {
		t.Fatal(err)
	}
	if len(definitions) != 160 {
		t.Fatalf("loaded %d sibling pack definitions, want 160", len(definitions))
	}
	registry, err := engine.NewRegistry(definitions)
	if err != nil {
		t.Fatal(err)
	}
	renderEngine, err := engine.New(registry, engine.Config{MaxConcurrency: 16})
	if err != nil {
		t.Fatal(err)
	}
	api, err := New(renderEngine, Config{Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), Version: "test"})
	if err != nil {
		t.Fatal(err)
	}
	return api
}

func executionPayload() string {
	return `{
  "apiVersion":"v1",
  "requestId":"req-integration-1",
  "runId":"run-integration-1",
  "mode":"emulated",
  "inventoryRevision":"inventory-sha",
  "packRevision":"pack-sha",
  "devices":[
    {
      "id":"device-1",
      "name":"edge-rtr-01",
      "platform":"cisco_ios",
      "managementAddress":"192.0.2.10",
      "role":"edge",
      "site":"dfw01",
      "status":"active",
      "osVersion":"15.9(3)M8",
      "imageFile":"flash:customer-approved.bin",
      "customFields":{"system":{"uptime_seconds":172801}}
    }
  ],
  "commands":["show version","show missing"],
  "concurrency":8,
  "timeoutSeconds":30
}`
}

func TestExecutionCompatibilityWithSiblingOpenNVPacks(t *testing.T) {
	api := siblingPackAPI(t)
	call := func() ExecutionResponse {
		request := httptest.NewRequest(http.MethodPost, "/v1/execution/render", strings.NewReader(executionPayload()))
		response := httptest.NewRecorder()
		api.Handler().ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("execution returned %d: %s", response.Code, response.Body.String())
		}
		var envelope ExecutionResponse
		if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
			t.Fatal(err)
		}
		return envelope
	}
	first := call()
	second := call()
	if first.RequestID != "req-integration-1" || len(first.Results) != 2 {
		t.Fatalf("unexpected response %#v", first)
	}
	succeeded := first.Results[0]
	if succeeded.TaskID != executionTaskID("run-integration-1", "device-1", "show version") || succeeded.TaskID != second.Results[0].TaskID {
		t.Fatalf("task ID is not deterministic: %q %q", succeeded.TaskID, second.Results[0].TaskID)
	}
	if succeeded.Backend != "clisynth" || !succeeded.Synthetic || succeeded.State != "succeeded" {
		t.Fatalf("unexpected successful result %#v", succeeded)
	}
	if succeeded.Output == nil || !strings.Contains(*succeeded.Output, "edge-rtr-01#show version") || !strings.Contains(*succeeded.Output, "15.9(3)M8") || !strings.Contains(*succeeded.Output, "2d 00:00:01") {
		t.Fatalf("canonical inventory was not applied to actual pack output: %v", succeeded.Output)
	}
	if succeeded.Parsed["system"].(map[string]any)["os_version"] != "15.9(3)M8" {
		t.Fatalf("unexpected normalized result %#v", succeeded.Parsed)
	}
	unsupported := first.Results[1]
	if unsupported.State != "unsupported" || unsupported.Error == nil || unsupported.Error.Retryable {
		t.Fatalf("unexpected unsupported result %#v", unsupported)
	}
	if first.Results[0].Command != "show version" || first.Results[1].Command != "show missing" {
		t.Fatalf("result order changed: %#v", first.Results)
	}
}

func TestExecutionCompatibilityRejectsWrongModeAndDuplicates(t *testing.T) {
	api := siblingPackAPI(t)
	for _, body := range []string{
		strings.Replace(executionPayload(), `"mode":"emulated"`, `"mode":"real"`, 1),
		strings.Replace(executionPayload(), `"commands":["show version","show missing"]`, `"commands":["show version","show version"]`, 1),
		strings.Replace(executionPayload(), `"concurrency":8`, `"concurrency":0`, 1),
	} {
		request := httptest.NewRequest(http.MethodPost, "/v1/execution/render", bytes.NewBufferString(body))
		response := httptest.NewRecorder()
		api.Handler().ServeHTTP(response, request)
		if response.Code != http.StatusUnprocessableEntity {
			t.Fatalf("invalid compatibility request returned %d: %s", response.Code, response.Body.String())
		}
	}
}

func TestExecutionCompatibilityAcceptsContractsExample(t *testing.T) {
	api := siblingPackAPI(t)
	contractExample := filepath.Join(siblingPackRoot(t), "..", "opennv-contracts", "examples", "execution-backend.json")
	body, err := os.ReadFile(contractExample)
	if err != nil {
		t.Skipf("sibling opennv-contracts example is unavailable: %v", err)
	}
	request := httptest.NewRequest(http.MethodPost, "/v1/execution/render", bytes.NewReader(body))
	response := httptest.NewRecorder()
	api.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("contract example returned %d: %s", response.Code, response.Body.String())
	}
	var envelope ExecutionResponse
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.RequestID != "req-demo-001" || len(envelope.Results) != 1 || envelope.Results[0].Backend != "clisynth" || !envelope.Results[0].Synthetic {
		t.Fatalf("unexpected contract response %#v", envelope)
	}
}
