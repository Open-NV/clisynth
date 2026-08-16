// Package outputfsm defines and validates CliSynth's inverse-TextFSM format.
package outputfsm

import (
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

const (
	APIVersion = "clisynth.opennv.io/v1alpha1"
	Kind       = "OutputFSM"
)

var (
	namePattern        = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9._-]*[a-z0-9])?$`)
	variablePattern    = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	templateTokenRegex = regexp.MustCompile(`\{\{\s*([^{}]+?)\s*\}\}`)
	resultPathPattern  = regexp.MustCompile(`^[a-z][a-z0-9_]*(?:\.[a-z][a-z0-9_]*)+$`)
)

// Definition is a complete synthetic CLI-output definition.
type Definition struct {
	APIVersion      string         `yaml:"apiVersion" json:"apiVersion"`
	Kind            string         `yaml:"kind" json:"kind"`
	Metadata        Metadata       `yaml:"metadata" json:"metadata"`
	Spec            Spec           `yaml:"spec" json:"spec"`
	Source          string         `yaml:"-" json:"-"`
	ResultSchema    []ResultField  `yaml:"-" json:"-"`
	FixtureDefaults map[string]any `yaml:"-" json:"-"`
}

type Metadata struct {
	Name        string            `yaml:"name" json:"name"`
	Version     string            `yaml:"version,omitempty" json:"version,omitempty"`
	Description string            `yaml:"description,omitempty" json:"description,omitempty"`
	Labels      map[string]string `yaml:"labels,omitempty" json:"labels,omitempty"`
}

type Spec struct {
	Platform        string              `yaml:"platform" json:"platform"`
	PlatformAliases []string            `yaml:"platformAliases,omitempty" json:"platformAliases,omitempty"`
	Command         string              `yaml:"command" json:"command"`
	CommandAliases  []string            `yaml:"commandAliases,omitempty" json:"commandAliases,omitempty"`
	Template        string              `yaml:"template" json:"template"`
	Variables       map[string]Variable `yaml:"variables,omitempty" json:"variables,omitempty"`
	Conditions      []Condition         `yaml:"conditions,omitempty" json:"conditions,omitempty"`
	Starlark        string              `yaml:"starlark,omitempty" json:"starlark,omitempty"`
}

type Variable struct {
	Source    string     `yaml:"source,omitempty" json:"source,omitempty"`
	Default   any        `yaml:"default,omitempty" json:"default,omitempty"`
	Required  bool       `yaml:"required,omitempty" json:"required,omitempty"`
	Transform string     `yaml:"transform,omitempty" json:"transform,omitempty"`
	Generator *Generator `yaml:"generator,omitempty" json:"generator,omitempty"`
}

// Generator creates a deterministic value from the request seed, device
// identity, definition identity, and variable name.
type Generator struct {
	Kind      string `yaml:"kind" json:"kind"`
	Source    string `yaml:"source,omitempty" json:"source,omitempty"`
	Min       int64  `yaml:"min,omitempty" json:"min,omitempty"`
	Max       int64  `yaml:"max,omitempty" json:"max,omitempty"`
	Length    int    `yaml:"length,omitempty" json:"length,omitempty"`
	Prefix    string `yaml:"prefix,omitempty" json:"prefix,omitempty"`
	Values    []any  `yaml:"values,omitempty" json:"values,omitempty"`
	Precision int    `yaml:"precision,omitempty" json:"precision,omitempty"`
	Separator string `yaml:"separator,omitempty" json:"separator,omitempty"`
}

type ResultField struct {
	Path   string `yaml:"path" json:"path"`
	Type   string `yaml:"type" json:"type"`
	Source string `yaml:"source" json:"source"`
}

// Condition applies Set when When evaluates true and Else otherwise. Values
// may be literals or a field reference encoded as {"from": "device.path"}.
type Condition struct {
	When string         `yaml:"when" json:"when"`
	Set  map[string]any `yaml:"set,omitempty" json:"set,omitempty"`
	Else map[string]any `yaml:"else,omitempty" json:"else,omitempty"`
}

// Validate returns all schema issues in a single error.
func (d *Definition) Validate() error {
	var errs []error
	if d.APIVersion != APIVersion {
		errs = append(errs, fmt.Errorf("apiVersion must be %q", APIVersion))
	}
	if d.Kind != Kind {
		errs = append(errs, fmt.Errorf("kind must be %q", Kind))
	}
	if !namePattern.MatchString(d.Metadata.Name) {
		errs = append(errs, fmt.Errorf("metadata.name %q must be lowercase DNS-like text", d.Metadata.Name))
	}
	if strings.TrimSpace(d.Spec.Platform) == "" {
		errs = append(errs, errors.New("spec.platform is required"))
	}
	if strings.TrimSpace(d.Spec.Command) == "" {
		errs = append(errs, errors.New("spec.command is required"))
	}
	if d.Spec.Template == "" {
		errs = append(errs, errors.New("spec.template is required"))
	}

	for name, variable := range d.Spec.Variables {
		if !variablePattern.MatchString(name) {
			errs = append(errs, fmt.Errorf("variable %q must match %s", name, variablePattern))
		}
		if variable.Source != "" && variable.Generator != nil {
			errs = append(errs, fmt.Errorf("variable %q cannot define both source and generator", name))
		}
		if variable.Source == "" && variable.Generator == nil && variable.Default == nil {
			errs = append(errs, fmt.Errorf("variable %q must define source, generator, or default", name))
		}
		if err := validateTransform(variable.Transform); err != nil {
			errs = append(errs, fmt.Errorf("variable %q: %w", name, err))
		}
		if variable.Generator != nil {
			if err := variable.Generator.Validate(); err != nil {
				errs = append(errs, fmt.Errorf("variable %q: %w", name, err))
			}
		}
	}

	known := make(map[string]struct{}, len(d.Spec.Variables))
	for name := range d.Spec.Variables {
		known[name] = struct{}{}
	}
	for index, condition := range d.Spec.Conditions {
		if strings.TrimSpace(condition.When) == "" {
			errs = append(errs, fmt.Errorf("condition %d: when is required", index))
		}
		if len(condition.Set) == 0 && len(condition.Else) == 0 {
			errs = append(errs, fmt.Errorf("condition %d: set or else is required", index))
		}
		for name := range condition.Set {
			known[name] = struct{}{}
		}
		for name := range condition.Else {
			known[name] = struct{}{}
		}
	}
	resultPaths := make(map[string]struct{}, len(d.ResultSchema))
	for index, field := range d.ResultSchema {
		if !resultPathPattern.MatchString(field.Path) {
			errs = append(errs, fmt.Errorf("result field %d has invalid path %q", index, field.Path))
		}
		if _, duplicate := resultPaths[field.Path]; duplicate {
			errs = append(errs, fmt.Errorf("result field %d duplicates path %q", index, field.Path))
		}
		resultPaths[field.Path] = struct{}{}
		switch field.Type {
		case "string", "integer", "number", "boolean", "array", "object":
		default:
			errs = append(errs, fmt.Errorf("result field %d has unsupported type %q", index, field.Type))
		}
		if strings.TrimSpace(field.Source) == "" {
			errs = append(errs, fmt.Errorf("result field %d source is required", index))
		}
	}

	for _, match := range templateTokenRegex.FindAllStringSubmatch(d.Spec.Template, -1) {
		name := strings.TrimSpace(match[1])
		if !variablePattern.MatchString(name) {
			errs = append(errs, fmt.Errorf("template token %q is not a simple variable", name))
			continue
		}
		if _, ok := known[name]; !ok {
			errs = append(errs, fmt.Errorf("template token %q has no variable or condition assignment", name))
		}
	}
	if strings.Contains(templateTokenRegex.ReplaceAllString(d.Spec.Template, ""), "{{") ||
		strings.Contains(templateTokenRegex.ReplaceAllString(d.Spec.Template, ""), "}}") {
		errs = append(errs, errors.New("template contains a malformed token"))
	}

	return errors.Join(errs...)
}

func validateTransform(transform string) error {
	switch transform {
	case "", "string", "lower", "upper", "trim", "json":
		return nil
	default:
		return fmt.Errorf("unsupported transform %q", transform)
	}
}

func (g Generator) Validate() error {
	switch g.Kind {
	case "integer":
		if g.Max < g.Min {
			return errors.New("integer generator max must be greater than or equal to min")
		}
	case "hex":
		if g.Length < 1 || g.Length > 128 {
			return errors.New("hex generator length must be between 1 and 128")
		}
	case "choice":
		if len(g.Values) == 0 {
			return errors.New("choice generator requires values")
		}
	case "mac":
		if g.Prefix != "" {
			clean := strings.ReplaceAll(strings.ReplaceAll(g.Prefix, ":", ""), "-", "")
			if len(clean) > 12 || len(clean)%2 != 0 || !regexp.MustCompile(`^[0-9a-fA-F]*$`).MatchString(clean) {
				return errors.New("mac generator prefix must contain an even number of at most 12 hexadecimal digits")
			}
		}
	case "ipv4":
		// No additional parameters.
	case "identity", "uptime_human", "yes_no", "up_down", "percent", "join", "on_off":
		if strings.TrimSpace(g.Source) == "" {
			return fmt.Errorf("%s generator requires source", g.Kind)
		}
		if g.Kind == "percent" && (g.Precision < 0 || g.Precision > 6) {
			return errors.New("percent generator precision must be between 0 and 6")
		}
		if len(g.Separator) > 32 {
			return errors.New("generator separator must be at most 32 characters")
		}
	default:
		return fmt.Errorf("unsupported generator kind %q", g.Kind)
	}
	return nil
}

// TemplateVariables returns sorted unique template tokens.
func (d Definition) TemplateVariables() []string {
	set := map[string]struct{}{}
	for _, match := range templateTokenRegex.FindAllStringSubmatch(d.Spec.Template, -1) {
		set[strings.TrimSpace(match[1])] = struct{}{}
	}
	result := make([]string, 0, len(set))
	for key := range set {
		result = append(result, key)
	}
	sort.Strings(result)
	return result
}

// ReplaceTemplateTokens performs literal variable substitution. It deliberately
// does not expose a general-purpose template evaluator.
func ReplaceTemplateTokens(template string, values map[string]any) (string, error) {
	var replaceErr error
	output := templateTokenRegex.ReplaceAllStringFunc(template, func(token string) string {
		if replaceErr != nil {
			return token
		}
		match := templateTokenRegex.FindStringSubmatch(token)
		name := strings.TrimSpace(match[1])
		value, ok := values[name]
		if !ok {
			replaceErr = fmt.Errorf("template variable %q is not set", name)
			return token
		}
		return fmt.Sprint(value)
	})
	if replaceErr != nil {
		return "", replaceErr
	}
	return output, nil
}
