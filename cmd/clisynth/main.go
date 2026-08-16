package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/open-nv/clisynth/pkg/engine"
	"github.com/open-nv/clisynth/pkg/httpapi"
	"github.com/open-nv/clisynth/pkg/inventory"
	"github.com/open-nv/clisynth/pkg/outputfsm"
	"gopkg.in/yaml.v3"
)

var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run(arguments []string, stdout, stderr io.Writer) error {
	if len(arguments) == 0 {
		printUsage(stderr)
		return errors.New("a command is required")
	}
	switch arguments[0] {
	case "serve":
		return serve(arguments[1:], stderr)
	case "validate":
		return validate(arguments[1:], stdout, stderr)
	case "list":
		return list(arguments[1:], stdout, stderr)
	case "render":
		return render(arguments[1:], stdout, stderr)
	case "render-batch":
		return renderBatch(arguments[1:], stdout, stderr)
	case "version":
		fmt.Fprintf(stdout, "clisynth %s (commit %s, built %s)\n", version, commit, date)
		return nil
	case "help", "-h", "--help":
		printUsage(stdout)
		return nil
	default:
		printUsage(stderr)
		return fmt.Errorf("unknown command %q", arguments[0])
	}
}

func serve(arguments []string, stderr io.Writer) error {
	flags := flag.NewFlagSet("serve", flag.ContinueOnError)
	flags.SetOutput(stderr)
	definitions := flags.String("definitions", definitionsDefault(), "definition directory or file")
	address := flags.String("addr", addressDefault(), "HTTP listen address")
	maxConcurrency := flags.Int("max-concurrency", envInt("CLISYNTH_MAX_CONCURRENCY", engine.DefaultMaxConcurrency), "maximum batch workers")
	cors := flags.String("cors-origins", os.Getenv("CLISYNTH_CORS_ORIGINS"), "comma-separated allowed CORS origins")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	renderEngine, err := loadEngine(*definitions, *maxConcurrency)
	if err != nil {
		return err
	}
	logger := slog.New(slog.NewJSONHandler(stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	api, err := httpapi.New(renderEngine, httpapi.Config{
		Logger:               logger,
		AllowedOrigins:       splitNonempty(*cors),
		Version:              version,
		ReloadToken:          os.Getenv("CLISYNTH_RELOAD_TOKEN"),
		MaxReloadBodyBytes:   envInt64("CLISYNTH_RELOAD_MAX_BODY_BYTES", httpapi.DefaultMaxReloadBodyBytes),
		MaxReloadDefinitions: envInt("CLISYNTH_RELOAD_MAX_DEFINITIONS", httpapi.DefaultMaxReloadDefinitions),
	})
	if err != nil {
		return err
	}
	server := &http.Server{
		Addr:              *address,
		Handler:           api.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      5 * time.Minute,
		IdleTimeout:       2 * time.Minute,
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	serveErrors := make(chan error, 1)
	go func() {
		logger.Info("CliSynth listening", "address", *address, "definitions", renderEngine.Registry().Count())
		serveErrors <- server.ListenAndServe()
	}()
	select {
	case err := <-serveErrors:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		return server.Shutdown(shutdownCtx)
	}
}

func validate(arguments []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("validate", flag.ContinueOnError)
	flags.SetOutput(stderr)
	definitions := flags.String("definitions", definitionsDefault(), "definition directory or file")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	loaded, err := outputfsm.LoadDir(*definitions)
	if err != nil {
		return err
	}
	registry, err := engine.NewRegistry(loaded)
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "validated %d OutputFSM definitions from %s\n", registry.Count(), *definitions)
	return nil
}

func list(arguments []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("list", flag.ContinueOnError)
	flags.SetOutput(stderr)
	definitions := flags.String("definitions", definitionsDefault(), "definition directory or file")
	jsonOutput := flags.Bool("json", false, "emit JSON")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	renderEngine, err := loadEngine(*definitions, engine.DefaultMaxConcurrency)
	if err != nil {
		return err
	}
	summaries := renderEngine.Registry().List()
	if *jsonOutput {
		return writePrettyJSON(stdout, summaries)
	}
	for _, summary := range summaries {
		fmt.Fprintf(stdout, "%s\t%s\t%s\n", summary.Platform, summary.Command, summary.Name)
	}
	return nil
}

func render(arguments []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("render", flag.ContinueOnError)
	flags.SetOutput(stderr)
	definitions := flags.String("definitions", definitionsDefault(), "definition directory or file")
	inventoryPath := flags.String("inventory", "", "inventory JSON/YAML file")
	platform := flags.String("platform", "", "platform override")
	command := flags.String("command", "", "show command")
	seed := flags.Int64("seed", 0, "deterministic generation seed")
	jsonOutput := flags.Bool("json", false, "emit the full result as JSON")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	if *inventoryPath == "" {
		return errors.New("--inventory is required")
	}
	document, err := inventory.LoadFile(*inventoryPath)
	if err != nil {
		return err
	}
	renderEngine, err := loadEngine(*definitions, engine.DefaultMaxConcurrency)
	if err != nil {
		return err
	}
	result, err := renderEngine.Render(context.Background(), engine.RenderRequest{Platform: *platform, Command: *command, Device: document, Seed: *seed})
	if err != nil {
		return err
	}
	if *jsonOutput {
		return writePrettyJSON(stdout, result)
	}
	_, err = fmt.Fprint(stdout, result.Output)
	return err
}

func renderBatch(arguments []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("render-batch", flag.ContinueOnError)
	flags.SetOutput(stderr)
	definitions := flags.String("definitions", definitionsDefault(), "definition directory or file")
	input := flags.String("input", "", "batch request JSON/YAML file")
	concurrency := flags.Int("concurrency", 0, "workers for this batch")
	maxConcurrency := flags.Int("max-concurrency", envInt("CLISYNTH_MAX_CONCURRENCY", engine.DefaultMaxConcurrency), "maximum batch workers")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	if *input == "" {
		return errors.New("--input is required")
	}
	payload, err := os.ReadFile(*input)
	if err != nil {
		return err
	}
	var request engine.BatchRequest
	if json.Valid(payload) {
		decoder := json.NewDecoder(bytes.NewReader(payload))
		decoder.DisallowUnknownFields()
		decoder.UseNumber()
		err = decoder.Decode(&request)
	} else {
		err = yaml.Unmarshal(payload, &request)
	}
	if err != nil {
		return fmt.Errorf("decode batch: %w", err)
	}
	if *concurrency != 0 {
		request.Concurrency = *concurrency
	}
	renderEngine, err := loadEngine(*definitions, *maxConcurrency)
	if err != nil {
		return err
	}
	result, err := renderEngine.RenderBatch(context.Background(), request)
	if err != nil {
		return err
	}
	return writePrettyJSON(stdout, result)
}

func loadEngine(path string, maxConcurrency int) (*engine.Engine, error) {
	definitions, err := outputfsm.LoadDir(path)
	if err != nil {
		return nil, err
	}
	registry, err := engine.NewRegistry(definitions)
	if err != nil {
		return nil, err
	}
	return engine.New(registry, engine.Config{MaxConcurrency: maxConcurrency})
}

func writePrettyJSON(writer io.Writer, value any) error {
	encoder := json.NewEncoder(writer)
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}

func printUsage(writer io.Writer) {
	fmt.Fprintln(writer, `CliSynth renders deterministic synthetic network CLI output.

Usage:
  clisynth serve [flags]
  clisynth validate [flags]
  clisynth list [flags]
  clisynth render --inventory FILE --command COMMAND [flags]
  clisynth render-batch --input FILE [flags]
  clisynth version`)
}

func envOr(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

func definitionsDefault() string {
	if value := os.Getenv("OUTPUTFSM_ROOT"); value != "" {
		return value
	}
	return envOr("CLISYNTH_DEFINITIONS", "./examples/definitions")
}

func addressDefault() string {
	if value := os.Getenv("CLISYNTH_ADDR"); value != "" {
		return value
	}
	if port := os.Getenv("PORT"); port != "" {
		return ":" + strings.TrimPrefix(port, ":")
	}
	return ":8080"
}

func envInt(name string, fallback int) int {
	value := os.Getenv(name)
	if value == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return fallback
	}
	return parsed
}

func envInt64(name string, fallback int64) int64 {
	value := os.Getenv(name)
	if value == "" {
		return fallback
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return fallback
	}
	return parsed
}

func splitNonempty(value string) []string {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	parts := strings.Split(value, ",")
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			result = append(result, trimmed)
		}
	}
	return result
}
