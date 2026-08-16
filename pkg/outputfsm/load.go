package outputfsm

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// MaxDefinitionBytes bounds one YAML definition regardless of whether it is
// loaded from disk or delivered through the administrative reload API.
const MaxDefinitionBytes = 2 << 20

// LoadFile decodes and validates exactly one OutputFSM YAML document.
func LoadFile(path string) (Definition, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Definition{}, fmt.Errorf("read definition %q: %w", path, err)
	}
	if len(data) > MaxDefinitionBytes {
		return Definition{}, fmt.Errorf("definition %q exceeds %d bytes", path, MaxDefinitionBytes)
	}
	definition, err := Decode(data)
	if err != nil {
		return Definition{}, fmt.Errorf("definition %q: %w", path, err)
	}
	definition.Source = path
	return definition, nil
}

// Decode parses one strict YAML document. Unknown fields and additional YAML
// documents are rejected to catch misspelled configuration before runtime.
func Decode(data []byte) (Definition, error) {
	var header struct {
		APIVersion string `yaml:"apiVersion"`
		Kind       string `yaml:"kind"`
	}
	if err := yaml.Unmarshal(data, &header); err != nil {
		return Definition{}, fmt.Errorf("decode YAML header: %w", err)
	}
	if header.Kind == Kind && header.APIVersion == OpenNVAPIVersion {
		return decodeOpenNVPack(data)
	}
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	var definition Definition
	if err := decoder.Decode(&definition); err != nil {
		return Definition{}, fmt.Errorf("decode YAML: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err != nil {
			return Definition{}, fmt.Errorf("decode trailing YAML: %w", err)
		}
		return Definition{}, errors.New("exactly one YAML document is allowed")
	}
	if err := definition.Validate(); err != nil {
		return Definition{}, err
	}
	return definition, nil
}

// LoadDir recursively loads .yaml and .yml files in lexical path order.
func LoadDir(root string) ([]Definition, error) {
	info, err := os.Stat(root)
	if err != nil {
		return nil, fmt.Errorf("stat definitions path %q: %w", root, err)
	}
	if !info.IsDir() {
		definition, err := LoadFile(root)
		if err != nil {
			return nil, err
		}
		return []Definition{definition}, nil
	}

	paths, err := discoverPackPaths(root)
	if err != nil {
		return nil, err
	}
	if len(paths) == 0 {
		paths = nil
		err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if path != root && entry.IsDir() && strings.HasPrefix(entry.Name(), ".") {
				return filepath.SkipDir
			}
			if entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
				return nil
			}
			extension := strings.ToLower(filepath.Ext(entry.Name()))
			if extension == ".yaml" || extension == ".yml" {
				paths = append(paths, path)
			}
			return nil
		})
		if err != nil {
			return nil, fmt.Errorf("walk definitions path %q: %w", root, err)
		}
	}
	sort.Strings(paths)
	if len(paths) == 0 {
		return nil, fmt.Errorf("definitions path %q contains no YAML files", root)
	}

	definitions := make([]Definition, 0, len(paths))
	var errs []error
	for _, path := range paths {
		definition, loadErr := LoadFile(path)
		if loadErr != nil {
			errs = append(errs, loadErr)
			continue
		}
		definitions = append(definitions, definition)
	}
	if len(errs) != 0 {
		return nil, errors.Join(errs...)
	}
	return definitions, nil
}

func discoverPackPaths(root string) ([]string, error) {
	canonicalRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return nil, fmt.Errorf("resolve definitions root %q: %w", root, err)
	}
	canonicalRoot, err = filepath.Abs(canonicalRoot)
	if err != nil {
		return nil, fmt.Errorf("resolve absolute definitions root %q: %w", root, err)
	}
	patterns := []string{
		filepath.Join(root, "packs", "*", "commands", "*.yaml"),
		filepath.Join(root, "packs", "*", "commands", "*.yml"),
		filepath.Join(root, "*", "commands", "*.yaml"),
		filepath.Join(root, "*", "commands", "*.yml"),
	}
	var paths []string
	for _, pattern := range patterns {
		matches, err := filepath.Glob(pattern)
		if err != nil {
			return nil, fmt.Errorf("discover OutputFSM packs: %w", err)
		}
		for _, match := range matches {
			info, statErr := os.Lstat(match)
			if statErr != nil {
				return nil, fmt.Errorf("inspect pack definition %q: %w", match, statErr)
			}
			if info.Mode()&os.ModeSymlink != 0 {
				return nil, fmt.Errorf("pack definition %q must not be a symlink", match)
			}
			resolved, resolveErr := filepath.EvalSymlinks(match)
			if resolveErr != nil {
				return nil, fmt.Errorf("resolve pack definition %q: %w", match, resolveErr)
			}
			resolved, resolveErr = filepath.Abs(resolved)
			if resolveErr != nil {
				return nil, fmt.Errorf("resolve absolute pack definition %q: %w", match, resolveErr)
			}
			relative, resolveErr := filepath.Rel(canonicalRoot, resolved)
			if resolveErr != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
				return nil, fmt.Errorf("pack definition %q resolves outside root", match)
			}
			paths = append(paths, match)
		}
	}
	sort.Strings(paths)
	return paths, nil
}
