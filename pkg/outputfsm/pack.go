package outputfsm

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

const (
	OpenNVAPIVersion = "opennv.io/v1alpha1"
)

var dollarTemplateToken = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}|\$([A-Za-z_][A-Za-z0-9_]*)`)

type packDefinition struct {
	APIVersion string       `yaml:"apiVersion"`
	Kind       string       `yaml:"kind"`
	Metadata   packMetadata `yaml:"metadata"`
	Spec       packSpec     `yaml:"spec"`
}

type packMetadata struct {
	ID          string `yaml:"id"`
	Title       string `yaml:"title"`
	Description string `yaml:"description"`
	License     string `yaml:"license"`
	Synthetic   bool   `yaml:"synthetic"`
	Category    string `yaml:"category"`
}

type packSpec struct {
	Platform        string                  `yaml:"platform"`
	PlatformAliases []string                `yaml:"platform_aliases,omitempty"`
	Command         string                  `yaml:"command"`
	Aliases         []string                `yaml:"aliases"`
	Variables       map[string]packVariable `yaml:"variables"`
	Template        string                  `yaml:"template"`
	ResultSchema    []ResultField           `yaml:"result_schema"`
	Fixtures        []packFixture           `yaml:"fixtures"`
}

type packVariable struct {
	Source    string         `yaml:"source,omitempty"`
	Literal   packLiteral    `yaml:"literal,omitempty"`
	Generator *packGenerator `yaml:"generator,omitempty"`
}

type packLiteral struct {
	Set   bool
	Value any
}

func (literal *packLiteral) UnmarshalYAML(node *yaml.Node) error {
	literal.Set = true
	return node.Decode(&literal.Value)
}

type packGenerator struct {
	Name      string `yaml:"name"`
	Source    string `yaml:"source"`
	Precision int    `yaml:"precision,omitempty"`
	Separator string `yaml:"separator,omitempty"`
}

type packFixture struct {
	Name           string         `yaml:"name"`
	Description    string         `yaml:"description"`
	Inventory      map[string]any `yaml:"inventory"`
	ExpectedOutput string         `yaml:"expected_output"`
	ExpectedResult map[string]any `yaml:"expected_result"`
}

func decodeOpenNVPack(data []byte) (Definition, error) {
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	var packed packDefinition
	if err := decoder.Decode(&packed); err != nil {
		return Definition{}, fmt.Errorf("decode OpenNV pack YAML: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err != nil {
			return Definition{}, fmt.Errorf("decode trailing OpenNV pack YAML: %w", err)
		}
		return Definition{}, errors.New("exactly one YAML document is allowed")
	}
	if packed.APIVersion != OpenNVAPIVersion {
		return Definition{}, fmt.Errorf("unsupported OpenNV OutputFSM apiVersion %q", packed.APIVersion)
	}
	if packed.Kind != Kind {
		return Definition{}, fmt.Errorf("kind must be %q", Kind)
	}
	if !packed.Metadata.Synthetic {
		return Definition{}, errors.New("metadata.synthetic must be true")
	}
	if packed.Metadata.License != "Apache-2.0" {
		return Definition{}, errors.New("metadata.license must be Apache-2.0")
	}
	if strings.TrimSpace(packed.Metadata.ID) == "" || strings.TrimSpace(packed.Metadata.Title) == "" || strings.TrimSpace(packed.Metadata.Description) == "" || strings.TrimSpace(packed.Metadata.Category) == "" {
		return Definition{}, errors.New("metadata.id, title, description, and category are required")
	}
	if len(packed.Spec.Variables) == 0 {
		return Definition{}, errors.New("spec.variables must not be empty")
	}
	if len(packed.Spec.ResultSchema) == 0 {
		return Definition{}, errors.New("spec.result_schema must not be empty")
	}
	if len(packed.Spec.Fixtures) == 0 || len(packed.Spec.Fixtures[0].Inventory) == 0 {
		return Definition{}, errors.New("spec.fixtures must include a baseline inventory")
	}

	template, err := convertDollarTemplate(packed.Spec.Template)
	if err != nil {
		return Definition{}, err
	}
	definition := Definition{
		APIVersion: APIVersion,
		Kind:       Kind,
		Metadata: Metadata{
			Name:        packDefinitionName(packed.Spec.Platform, packed.Metadata.ID),
			Description: packed.Metadata.Description,
			Labels: map[string]string{
				"opennv.io/category":   packed.Metadata.Category,
				"opennv.io/synthetic":  "true",
				"opennv.io/source-api": packed.APIVersion,
			},
		},
		Spec: Spec{
			Platform:        packed.Spec.Platform,
			PlatformAliases: append([]string(nil), packed.Spec.PlatformAliases...),
			Command:         packed.Spec.Command,
			CommandAliases:  append([]string(nil), packed.Spec.Aliases...),
			Template:        template,
			Variables:       make(map[string]Variable, len(packed.Spec.Variables)),
		},
		ResultSchema:    append([]ResultField(nil), packed.Spec.ResultSchema...),
		FixtureDefaults: cloneMap(packed.Spec.Fixtures[0].Inventory),
	}
	for name, rule := range packed.Spec.Variables {
		choices := 0
		if rule.Source != "" {
			choices++
		}
		if rule.Literal.Set {
			choices++
		}
		if rule.Generator != nil {
			choices++
		}
		if choices != 1 {
			return Definition{}, fmt.Errorf("variable %q must set exactly one of source, literal, or generator", name)
		}
		switch {
		case rule.Source != "":
			source, sourceErr := packSource(rule.Source)
			if sourceErr != nil {
				return Definition{}, fmt.Errorf("variable %q: %w", name, sourceErr)
			}
			definition.Spec.Variables[name] = Variable{Source: source, Required: true}
		case rule.Literal.Set:
			definition.Spec.Variables[name] = Variable{Default: rule.Literal.Value}
		case rule.Generator != nil:
			source, sourceErr := packSource(rule.Generator.Source)
			if sourceErr != nil {
				return Definition{}, fmt.Errorf("variable %q generator: %w", name, sourceErr)
			}
			definition.Spec.Variables[name] = Variable{Generator: &Generator{
				Kind:      rule.Generator.Name,
				Source:    source,
				Precision: rule.Generator.Precision,
				Separator: rule.Generator.Separator,
			}}
		}
	}
	for index := range definition.ResultSchema {
		source, sourceErr := packSource(definition.ResultSchema[index].Source)
		if sourceErr != nil {
			return Definition{}, fmt.Errorf("result_schema %d: %w", index, sourceErr)
		}
		definition.ResultSchema[index].Source = source
	}
	if err := definition.Validate(); err != nil {
		return Definition{}, err
	}
	return definition, nil
}

func packDefinitionName(platform, id string) string {
	return strings.ReplaceAll(platform, "_", "-") + "-" + strings.ReplaceAll(id, "_", "-")
}

func packSource(source string) (string, error) {
	if !strings.HasPrefix(source, "inventory.") || len(source) == len("inventory.") {
		return "", fmt.Errorf("source %q must begin with inventory.", source)
	}
	return "device." + strings.TrimPrefix(source, "inventory."), nil
}

func convertDollarTemplate(template string) (string, error) {
	const escapedDollar = "\x00CLISYNTH_ESCAPED_DOLLAR\x00"
	if strings.Contains(template, escapedDollar) {
		return "", errors.New("pack template contains a reserved marker")
	}
	template = strings.ReplaceAll(template, "$$", escapedDollar)
	converted := dollarTemplateToken.ReplaceAllStringFunc(template, func(token string) string {
		match := dollarTemplateToken.FindStringSubmatch(token)
		name := match[1]
		if name == "" {
			name = match[2]
		}
		return "{{ " + name + " }}"
	})
	if strings.Contains(converted, "$") {
		return "", errors.New("pack template contains an unsupported dollar expression")
	}
	return strings.ReplaceAll(converted, escapedDollar, "$"), nil
}

func cloneMap(source map[string]any) map[string]any {
	result := make(map[string]any, len(source))
	for key, value := range source {
		switch typed := value.(type) {
		case map[string]any:
			result[key] = cloneMap(typed)
		case []any:
			result[key] = cloneSlice(typed)
		default:
			result[key] = typed
		}
	}
	return result
}

func cloneSlice(source []any) []any {
	result := make([]any, len(source))
	for index, value := range source {
		switch typed := value.(type) {
		case map[string]any:
			result[index] = cloneMap(typed)
		case []any:
			result[index] = cloneSlice(typed)
		default:
			result[index] = typed
		}
	}
	return result
}
