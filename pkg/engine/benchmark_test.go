package engine

import (
	"context"
	"strconv"
	"testing"

	"github.com/open-nv/clisynth/pkg/outputfsm"
)

func BenchmarkRender(b *testing.B) {
	renderEngine := newTestEngine(b, testDefinition(), Config{})
	request := RenderRequest{Command: "show version", Device: map[string]any{"id": "device-1", "name": "router", "platform": "test_os", "active": true}}
	b.ReportAllocs()
	b.ResetTimer()
	for index := 0; index < b.N; index++ {
		request.Seed = int64(index)
		if _, err := renderEngine.Render(context.Background(), request); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkRenderBatch1000(b *testing.B) {
	renderEngine := newTestEngine(b, testDefinition(), Config{MaxConcurrency: 32})
	request := BatchRequest{Requests: make([]RenderRequest, 1000), Concurrency: 32}
	for index := range request.Requests {
		request.Requests[index] = RenderRequest{Command: "show version", Device: map[string]any{"id": strconv.Itoa(index), "name": "router", "platform": "test_os"}}
	}
	b.ReportAllocs()
	b.ResetTimer()
	for index := 0; index < b.N; index++ {
		if _, err := renderEngine.RenderBatch(context.Background(), request); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkRenderBatch100000 is intentionally excluded from ordinary tests and
// runs only when explicitly selected with -bench. It exercises the public
// one-command/100,000-device scale target while retaining every ordered result.
func BenchmarkRenderBatch100000(b *testing.B) {
	definition := outputfsm.Definition{
		APIVersion: outputfsm.APIVersion,
		Kind:       outputfsm.Kind,
		Metadata:   outputfsm.Metadata{Name: "scale-show-version"},
		Spec: outputfsm.Spec{
			Platform: "scale_os",
			Command:  "show version",
			Variables: map[string]outputfsm.Variable{
				"hostname": {Source: "device.name", Required: true},
				"version":  {Source: "device.version", Required: true},
				"serial":   {Generator: &outputfsm.Generator{Kind: "hex", Prefix: "SN", Length: 12}},
			},
			Template: "{{ hostname }} version {{ version }} serial {{ serial }}\n",
		},
	}
	renderEngine := newTestEngine(b, definition, Config{MaxConcurrency: 256})
	const deviceCount = 100_000
	request := BatchRequest{Requests: make([]RenderRequest, deviceCount), Concurrency: 256}
	for index := range request.Requests {
		request.Requests[index] = RenderRequest{
			Command: "show version",
			Device: map[string]any{
				"id":       "device-" + strconv.Itoa(index),
				"name":     "router-" + strconv.Itoa(index),
				"platform": "scale_os",
				"version":  "1.0",
			},
		}
	}
	b.ReportAllocs()
	b.ResetTimer()
	for iteration := 0; iteration < b.N; iteration++ {
		result, err := renderEngine.RenderBatch(context.Background(), request)
		if err != nil {
			b.Fatal(err)
		}
		if result.Summary.Succeeded != deviceCount || len(result.Results) != deviceCount {
			b.Fatalf("unexpected result summary %#v", result.Summary)
		}
	}
	b.StopTimer()
	b.ReportMetric(float64(deviceCount*b.N)/b.Elapsed().Seconds(), "devices/s")
}
