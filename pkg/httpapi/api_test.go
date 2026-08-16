package httpapi

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/open-nv/clisynth/pkg/engine"
	"github.com/open-nv/clisynth/pkg/outputfsm"
)

func testAPI(t *testing.T) *API {
	t.Helper()
	definition := outputfsm.Definition{
		APIVersion: outputfsm.APIVersion,
		Kind:       outputfsm.Kind,
		Metadata:   outputfsm.Metadata{Name: "test-show"},
		Spec: outputfsm.Spec{
			Platform: "test_os",
			Command:  "show value",
			Template: "value={{ value }}\n",
			Variables: map[string]outputfsm.Variable{
				"value": {Source: "device.value", Required: true},
			},
		},
	}
	registry, err := engine.NewRegistry([]outputfsm.Definition{definition})
	if err != nil {
		t.Fatal(err)
	}
	renderEngine, err := engine.New(registry, engine.Config{MaxConcurrency: 4})
	if err != nil {
		t.Fatal(err)
	}
	api, err := New(renderEngine, Config{
		Logger:         slog.New(slog.NewTextHandler(io.Discard, nil)),
		AllowedOrigins: []string{"https://ui.example"},
		Version:        "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	return api
}

func TestHealthReadyAndDefinitions(t *testing.T) {
	api := testAPI(t)
	for _, test := range []struct {
		path     string
		contains string
	}{
		{"/healthz", `"status":"ok"`},
		{"/readyz", `"definitions":1`},
		{"/v1/definitions", `"name":"test-show"`},
	} {
		request := httptest.NewRequest(http.MethodGet, test.path, nil)
		response := httptest.NewRecorder()
		api.Handler().ServeHTTP(response, request)
		if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), test.contains) {
			t.Errorf("GET %s = %d %s", test.path, response.Code, response.Body.String())
		}
	}
}

func TestRenderAndCORS(t *testing.T) {
	api := testAPI(t)
	body := `{"command":"show value","device":{"name":"one","platform":"test_os","value":42}}`
	request := httptest.NewRequest(http.MethodPost, "/v1/render", strings.NewReader(body))
	request.Header.Set("Origin", "https://ui.example")
	response := httptest.NewRecorder()
	api.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("render returned %d: %s", response.Code, response.Body.String())
	}
	if response.Header().Get("Access-Control-Allow-Origin") != "https://ui.example" {
		t.Fatalf("expected allowed CORS origin, got %q", response.Header().Get("Access-Control-Allow-Origin"))
	}
	var result engine.RenderResult
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Output != "value=42\n" {
		t.Fatalf("unexpected output %q", result.Output)
	}
}

func TestRenderStrictJSONAndNotFound(t *testing.T) {
	api := testAPI(t)
	tests := []struct {
		name string
		body string
		code int
	}{
		{"unknown field", `{"command":"show value","extra":true,"device":{"platform":"test_os"}}`, http.StatusBadRequest},
		{"missing definition", `{"command":"show missing","device":{"platform":"test_os"}}`, http.StatusNotFound},
		{"missing inventory field", `{"command":"show value","device":{"platform":"test_os"}}`, http.StatusUnprocessableEntity},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, "/v1/render", bytes.NewBufferString(test.body))
			response := httptest.NewRecorder()
			api.Handler().ServeHTTP(response, request)
			if response.Code != test.code {
				t.Fatalf("got %d %s, want %d", response.Code, response.Body.String(), test.code)
			}
		})
	}
}

func TestRenderBatchPreservesOrder(t *testing.T) {
	api := testAPI(t)
	body := `{"concurrency":2,"requests":[
        {"command":"show value","device":{"platform":"test_os","name":"a","value":"first"}},
        {"command":"show value","device":{"platform":"test_os","name":"b","value":"second"}}
    ]}`
	request := httptest.NewRequest(http.MethodPost, "/v1/render:batch", strings.NewReader(body))
	response := httptest.NewRecorder()
	api.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("batch returned %d: %s", response.Code, response.Body.String())
	}
	var result engine.BatchResult
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Results[0].Result.Output != "value=first\n" || result.Results[1].Result.Output != "value=second\n" {
		t.Fatalf("unexpected ordered results %#v", result.Results)
	}
}
