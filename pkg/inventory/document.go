// Package inventory provides helpers around source-agnostic inventory
// documents. MongoDB and GraphQL adapters can populate the same map shape.
package inventory

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

const maxInventoryBytes = 16 << 20

type Document map[string]any

// LoadFile reads a JSON or YAML inventory document.
func LoadFile(path string) (Document, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read inventory %q: %w", path, err)
	}
	if len(data) > maxInventoryBytes {
		return nil, fmt.Errorf("inventory %q exceeds %d bytes", path, maxInventoryBytes)
	}
	var document Document
	if json.Valid(data) {
		decoder := json.NewDecoder(bytes.NewReader(data))
		decoder.UseNumber()
		if err := decoder.Decode(&document); err != nil {
			return nil, fmt.Errorf("decode inventory JSON: %w", err)
		}
	} else if err := yaml.Unmarshal(data, &document); err != nil {
		return nil, fmt.Errorf("decode inventory YAML: %w", err)
	}
	if len(document) == 0 {
		return nil, errors.New("inventory document must not be empty")
	}
	return document, nil
}

// Get resolves a dotted path through string-keyed maps and numeric slice
// indexes. A leading "device." is optional.
func Get(document map[string]any, path string) (any, bool) {
	path = strings.TrimSpace(path)
	path = strings.TrimPrefix(path, "device.")
	if path == "device" || path == "" {
		return document, true
	}
	parts := strings.Split(path, ".")
	var current any = document
	for _, part := range parts {
		switch typed := current.(type) {
		case map[string]any:
			var ok bool
			current, ok = typed[part]
			if !ok {
				return nil, false
			}
		case Document:
			var ok bool
			current, ok = typed[part]
			if !ok {
				return nil, false
			}
		case []any:
			index, err := strconv.Atoi(part)
			if err != nil || index < 0 || index >= len(typed) {
				return nil, false
			}
			current = typed[index]
		default:
			return nil, false
		}
	}
	return current, true
}

// Identity returns a stable best-effort identity for deterministic generation.
func Identity(document map[string]any) string {
	for _, path := range []string{"id", "name", "hostname", "primary_ip.address", "address"} {
		if value, ok := Get(document, path); ok && fmt.Sprint(value) != "" {
			return fmt.Sprint(value)
		}
	}
	data, err := canonicalJSON(document)
	if err != nil {
		return fmt.Sprint(document)
	}
	return string(data)
}

func canonicalJSON(value any) ([]byte, error) {
	return json.Marshal(value)
}
