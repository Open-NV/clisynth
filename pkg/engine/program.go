package engine

import (
	"fmt"
	"strings"

	"github.com/open-nv/clisynth/pkg/outputfsm"
	"go.starlark.net/starlark"
	"go.starlark.net/syntax"
)

type compiledDefinition struct {
	definition outputfsm.Definition
	conditions []*starlark.Program
	processor  *starlark.Program
}

func compileDefinition(definition outputfsm.Definition) (compiledDefinition, error) {
	compiled := compiledDefinition{definition: definition}
	for index, condition := range definition.Spec.Conditions {
		source := "def condition(device, vars, seed):\n    return (" + condition.When + ")\n"
		_, program, err := starlark.SourceProgramOptions(&syntax.FileOptions{}, definition.Metadata.Name+"-condition.star", source, func(string) bool { return false })
		if err != nil {
			return compiledDefinition{}, fmt.Errorf("condition %d does not compile: %w", index, err)
		}
		if program.NumLoads() != 0 {
			return compiledDefinition{}, fmt.Errorf("condition %d: load statements are not allowed", index)
		}
		compiled.conditions = append(compiled.conditions, program)
	}
	if strings.TrimSpace(definition.Spec.Starlark) != "" {
		file, program, err := starlark.SourceProgramOptions(&syntax.FileOptions{}, definition.Metadata.Name+".star", definition.Spec.Starlark, func(string) bool { return false })
		if err != nil {
			return compiledDefinition{}, fmt.Errorf("starlark processor does not compile: %w", err)
		}
		if program.NumLoads() != 0 {
			return compiledDefinition{}, fmt.Errorf("starlark processor: load statements are not allowed")
		}
		foundProcess := false
		for _, statement := range file.Stmts {
			definitionStatement, ok := statement.(*syntax.DefStmt)
			if !ok {
				return compiledDefinition{}, fmt.Errorf("starlark processor may contain only function definitions at top level")
			}
			if definitionStatement.Name.Name == "process" {
				if len(definitionStatement.Params) != 3 {
					return compiledDefinition{}, fmt.Errorf("process must accept exactly (device, vars, seed)")
				}
				expectedParameters := []string{"device", "vars", "seed"}
				for index, parameter := range definitionStatement.Params {
					identifier, ok := parameter.(*syntax.Ident)
					if !ok || identifier.Name != expectedParameters[index] {
						return compiledDefinition{}, fmt.Errorf("process must accept exactly (device, vars, seed)")
					}
				}
				foundProcess = true
			}
		}
		if !foundProcess {
			return compiledDefinition{}, fmt.Errorf("starlark processor must define process(device, vars, seed)")
		}
		compiled.processor = program
	}
	return compiled, nil
}
