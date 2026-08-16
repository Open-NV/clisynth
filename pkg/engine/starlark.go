package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"

	"go.starlark.net/starlark"
)

func evalCondition(ctx context.Context, program *starlark.Program, device, variables map[string]any, seed int64, maxSteps uint64) (bool, error) {
	thread, cleanup := starlarkThread(ctx, "outputfsm-condition", maxSteps)
	defer cleanup()
	globals, err := program.Init(thread, nil)
	if err != nil {
		return false, err
	}
	callable := globals["condition"]
	value, err := starlark.Call(thread, callable, starlark.Tuple{
		mustStarlark(device),
		mustStarlark(variables),
		starlark.MakeInt64(seed),
	}, nil)
	if err != nil {
		return false, err
	}
	boolean, ok := value.(starlark.Bool)
	if !ok {
		return false, fmt.Errorf("condition returned %s, want bool", value.Type())
	}
	return bool(boolean), nil
}

func runProcessor(ctx context.Context, program *starlark.Program, device, variables map[string]any, seed int64, maxSteps uint64) (map[string]any, error) {
	thread, cleanup := starlarkThread(ctx, "outputfsm-processor", maxSteps)
	defer cleanup()
	globals, err := program.Init(thread, nil)
	if err != nil {
		return nil, err
	}
	callable, ok := globals["process"]
	if !ok {
		return nil, fmt.Errorf("starlark script must define process(device, vars, seed)")
	}
	result, err := starlark.Call(thread, callable, starlark.Tuple{
		mustStarlark(device),
		mustStarlark(variables),
		starlark.MakeInt64(seed),
	}, nil)
	if err != nil {
		return nil, err
	}
	converted, err := fromStarlark(result)
	if err != nil {
		return nil, err
	}
	resultMap, ok := converted.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("process returned %T, want dict", converted)
	}
	return resultMap, nil
}

func starlarkThread(ctx context.Context, name string, maxSteps uint64) (*starlark.Thread, func()) {
	thread := &starlark.Thread{Name: name}
	thread.SetMaxExecutionSteps(maxSteps)
	if ctx.Done() == nil {
		return thread, func() {}
	}
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			thread.Cancel(ctx.Err().Error())
		case <-done:
		}
	}()
	return thread, func() { close(done) }
}

func mustStarlark(value any) starlark.Value {
	converted, err := toStarlark(value)
	if err != nil {
		// Inventory values have already come through JSON/YAML-compatible
		// structures. This path remains defensive and produces a string.
		return starlark.String(fmt.Sprint(value))
	}
	return converted
}

func toStarlark(value any) (starlark.Value, error) {
	switch typed := value.(type) {
	case nil:
		return starlark.None, nil
	case bool:
		return starlark.Bool(typed), nil
	case string:
		return starlark.String(typed), nil
	case int:
		return starlark.MakeInt(typed), nil
	case int8:
		return starlark.MakeInt64(int64(typed)), nil
	case int16:
		return starlark.MakeInt64(int64(typed)), nil
	case int32:
		return starlark.MakeInt64(int64(typed)), nil
	case int64:
		return starlark.MakeInt64(typed), nil
	case uint:
		return starlark.MakeUint(uint(typed)), nil
	case uint8:
		return starlark.MakeUint64(uint64(typed)), nil
	case uint16:
		return starlark.MakeUint64(uint64(typed)), nil
	case uint32:
		return starlark.MakeUint64(uint64(typed)), nil
	case uint64:
		return starlark.MakeUint64(typed), nil
	case float32:
		return starlark.Float(typed), nil
	case float64:
		return starlark.Float(typed), nil
	case json.Number:
		if integer, err := typed.Int64(); err == nil {
			return starlark.MakeInt64(integer), nil
		}
		floating, err := typed.Float64()
		if err != nil {
			return nil, fmt.Errorf("invalid JSON number %q: %w", typed, err)
		}
		return starlark.Float(floating), nil
	case map[string]any:
		keys := make([]string, 0, len(typed))
		for key := range typed {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		dict := starlark.NewDict(len(keys))
		for _, key := range keys {
			child, err := toStarlark(typed[key])
			if err != nil {
				return nil, err
			}
			if err := dict.SetKey(starlark.String(key), child); err != nil {
				return nil, err
			}
		}
		return dict, nil
	case []any:
		items := make([]starlark.Value, len(typed))
		for index, item := range typed {
			converted, err := toStarlark(item)
			if err != nil {
				return nil, err
			}
			items[index] = converted
		}
		return starlark.NewList(items), nil
	default:
		return nil, fmt.Errorf("cannot convert %T to Starlark", value)
	}
}

func fromStarlark(value starlark.Value) (any, error) {
	switch typed := value.(type) {
	case starlark.NoneType:
		return nil, nil
	case starlark.Bool:
		return bool(typed), nil
	case starlark.String:
		return string(typed), nil
	case starlark.Int:
		if integer, ok := typed.Int64(); ok {
			return integer, nil
		}
		return nil, fmt.Errorf("Starlark integer is outside int64 range")
	case starlark.Float:
		return float64(typed), nil
	case *starlark.List:
		result := make([]any, 0, typed.Len())
		iterator := typed.Iterate()
		defer iterator.Done()
		var item starlark.Value
		for iterator.Next(&item) {
			converted, err := fromStarlark(item)
			if err != nil {
				return nil, err
			}
			result = append(result, converted)
		}
		return result, nil
	case starlark.Tuple:
		result := make([]any, 0, len(typed))
		for _, item := range typed {
			converted, err := fromStarlark(item)
			if err != nil {
				return nil, err
			}
			result = append(result, converted)
		}
		return result, nil
	case *starlark.Dict:
		result := make(map[string]any, typed.Len())
		for _, item := range typed.Items() {
			key, ok := item[0].(starlark.String)
			if !ok {
				return nil, fmt.Errorf("Starlark result dict key %s is not a string", item[0].Type())
			}
			converted, err := fromStarlark(item[1])
			if err != nil {
				return nil, err
			}
			result[string(key)] = converted
		}
		return result, nil
	default:
		return nil, fmt.Errorf("cannot convert Starlark %s to Go", value.Type())
	}
}
