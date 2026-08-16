// Package httpapi exposes the CliSynth engine over a versioned JSON API.
package httpapi

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/open-nv/clisynth/pkg/engine"
)

const DefaultMaxBodyBytes int64 = 64 << 20

type Config struct {
	Logger               *slog.Logger
	MaxBodyBytes         int64
	AllowedOrigins       []string
	Version              string
	ReloadToken          string
	MaxReloadBodyBytes   int64
	MaxReloadDefinitions int
}

type API struct {
	engine          *engine.Engine
	logger          *slog.Logger
	maxBodyBytes    int64
	allowedOrigins  map[string]struct{}
	version         string
	reloadEnabled   bool
	reloadTokenHash [32]byte
	maxReloadBytes  int64
	maxReloadDefs   int
	reloadMu        sync.Mutex
	handler         http.Handler
}

func New(renderEngine *engine.Engine, config Config) (*API, error) {
	if renderEngine == nil {
		return nil, errors.New("engine is required")
	}
	if config.Logger == nil {
		config.Logger = slog.Default()
	}
	if config.MaxBodyBytes == 0 {
		config.MaxBodyBytes = DefaultMaxBodyBytes
	}
	if config.MaxBodyBytes < 1 {
		return nil, errors.New("max body bytes must be positive")
	}
	if config.MaxReloadBodyBytes == 0 {
		config.MaxReloadBodyBytes = DefaultMaxReloadBodyBytes
	}
	if config.MaxReloadBodyBytes < 1 || config.MaxReloadBodyBytes > AbsoluteMaxReloadBodyBytes {
		return nil, fmt.Errorf("max reload body bytes must be between 1 and %d", AbsoluteMaxReloadBodyBytes)
	}
	if config.MaxReloadDefinitions == 0 {
		config.MaxReloadDefinitions = DefaultMaxReloadDefinitions
	}
	if config.MaxReloadDefinitions < 1 || config.MaxReloadDefinitions > AbsoluteMaxReloadDefinitions {
		return nil, fmt.Errorf("max reload definitions must be between 1 and %d", AbsoluteMaxReloadDefinitions)
	}
	if config.ReloadToken != "" {
		if err := validateReloadToken(config.ReloadToken); err != nil {
			return nil, err
		}
	}
	reloadTokenHash := hashReloadToken(config.ReloadToken)
	api := &API{
		engine:          renderEngine,
		logger:          config.Logger,
		maxBodyBytes:    config.MaxBodyBytes,
		allowedOrigins:  make(map[string]struct{}, len(config.AllowedOrigins)),
		version:         config.Version,
		reloadEnabled:   config.ReloadToken != "",
		reloadTokenHash: reloadTokenHash,
		maxReloadBytes:  config.MaxReloadBodyBytes,
		maxReloadDefs:   config.MaxReloadDefinitions,
	}
	for _, origin := range config.AllowedOrigins {
		origin = strings.TrimSpace(origin)
		if origin != "" {
			api.allowedOrigins[origin] = struct{}{}
		}
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", api.health)
	mux.HandleFunc("GET /readyz", api.ready)
	mux.HandleFunc("GET /v1/definitions", api.listDefinitions)
	mux.HandleFunc("POST /v1/render", api.render)
	mux.HandleFunc("POST /v1/render:batch", api.renderBatch)
	mux.HandleFunc("POST /v1/execution/render", api.executeBatch)
	if api.reloadEnabled {
		mux.HandleFunc("PUT /v1/admin/definitions", api.reloadDefinitions)
	}
	mux.HandleFunc("OPTIONS /", api.options)
	api.handler = api.middleware(mux)
	return api, nil
}

func (a *API) Handler() http.Handler { return a.handler }

func (a *API) health(writer http.ResponseWriter, _ *http.Request) {
	writeJSON(writer, http.StatusOK, map[string]any{"status": "ok", "version": a.version})
}

func (a *API) ready(writer http.ResponseWriter, _ *http.Request) {
	count := a.engine.Registry().Count()
	if count == 0 {
		writeJSON(writer, http.StatusServiceUnavailable, map[string]any{"status": "not_ready", "definitions": 0})
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"status": "ready", "definitions": count})
}

func (a *API) listDefinitions(writer http.ResponseWriter, _ *http.Request) {
	definitions := a.engine.Registry().List()
	writeJSON(writer, http.StatusOK, map[string]any{"definitions": definitions, "count": len(definitions)})
}

func (a *API) render(writer http.ResponseWriter, request *http.Request) {
	var payload engine.RenderRequest
	if err := a.decodeJSON(writer, request, &payload); err != nil {
		writeAPIError(writer, http.StatusBadRequest, engine.CodeInvalidRequest, err.Error())
		return
	}
	result, err := a.engine.Render(request.Context(), payload)
	if err != nil {
		writeEngineError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, result)
}

func (a *API) renderBatch(writer http.ResponseWriter, request *http.Request) {
	var payload engine.BatchRequest
	if err := a.decodeJSON(writer, request, &payload); err != nil {
		writeAPIError(writer, http.StatusBadRequest, engine.CodeInvalidRequest, err.Error())
		return
	}
	result, err := a.engine.RenderBatch(request.Context(), payload)
	if err != nil {
		writeEngineError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, result)
}

func (a *API) options(writer http.ResponseWriter, _ *http.Request) {
	writer.WriteHeader(http.StatusNoContent)
}

func (a *API) decodeJSON(writer http.ResponseWriter, request *http.Request, destination any) error {
	request.Body = http.MaxBytesReader(writer, request.Body, a.maxBodyBytes)
	decoder := json.NewDecoder(request.Body)
	decoder.DisallowUnknownFields()
	decoder.UseNumber()
	if err := decoder.Decode(destination); err != nil {
		return fmt.Errorf("invalid JSON body: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err == nil {
		return errors.New("request body must contain exactly one JSON value")
	} else if !errors.Is(err, io.EOF) {
		return fmt.Errorf("invalid trailing JSON: %w", err)
	}
	return nil
}

func (a *API) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		started := time.Now()
		requestID := request.Header.Get("X-Request-ID")
		if requestID == "" || len(requestID) > 128 {
			requestID = newRequestID()
		}
		writer.Header().Set("X-Request-ID", requestID)
		writer.Header().Set("X-Content-Type-Options", "nosniff")
		if origin := request.Header.Get("Origin"); origin != "" {
			if _, ok := a.allowedOrigins[origin]; ok {
				writer.Header().Set("Access-Control-Allow-Origin", origin)
				writer.Header().Set("Vary", "Origin")
				writer.Header().Set("Access-Control-Allow-Headers", "Content-Type, X-Request-ID")
				writer.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
			}
		}
		defer func() {
			if recovered := recover(); recovered != nil {
				a.logger.Error("request panic", "request_id", requestID, "panic", recovered)
				writeAPIError(writer, http.StatusInternalServerError, "internal_error", "internal server error")
			}
			a.logger.Info("request", "request_id", requestID, "method", request.Method, "path", request.URL.Path, "duration_ms", time.Since(started).Milliseconds())
		}()
		next.ServeHTTP(writer, request)
	})
}

func newRequestID() string {
	buffer := make([]byte, 12)
	if _, err := rand.Read(buffer); err != nil {
		return fmt.Sprintf("fallback-%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(buffer)
}

func writeEngineError(writer http.ResponseWriter, err error) {
	var typed *engine.Error
	if !errors.As(err, &typed) {
		writeAPIError(writer, http.StatusInternalServerError, "internal_error", "internal server error")
		return
	}
	status := http.StatusUnprocessableEntity
	switch typed.Code {
	case engine.CodeInvalidRequest:
		status = http.StatusBadRequest
	case engine.CodeDefinitionNotFound:
		status = http.StatusNotFound
	case engine.CodeCanceled:
		status = http.StatusRequestTimeout
	}
	writeAPIError(writer, status, typed.Code, typed.Error())
}

func writeAPIError(writer http.ResponseWriter, status int, code, message string) {
	writeJSONStatus(writer, status, map[string]any{"error": map[string]string{"code": code, "message": message}})
}

func writeJSON(writer http.ResponseWriter, status int, value any) {
	writeJSONStatus(writer, status, value)
}

func writeJSONStatus(writer http.ResponseWriter, status int, value any) {
	writer.Header().Set("Content-Type", "application/json; charset=utf-8")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(value)
}
