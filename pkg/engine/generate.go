package engine

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math"
	"math/big"
	"reflect"
	"strconv"
	"strings"

	"github.com/open-nv/clisynth/pkg/outputfsm"
)

func pureGenerate(generator outputfsm.Generator, value any) (any, error) {
	switch generator.Kind {
	case "identity":
		return fmt.Sprint(value), nil
	case "uptime_human":
		seconds, err := integerValue(value)
		if err != nil {
			return nil, err
		}
		days := seconds / 86400
		remainder := seconds % 86400
		hours := remainder / 3600
		remainder %= 3600
		minutes := remainder / 60
		seconds = remainder % 60
		return fmt.Sprintf("%dd %02d:%02d:%02d", days, hours, minutes, seconds), nil
	case "yes_no":
		if truthy(value) {
			return "yes", nil
		}
		return "no", nil
	case "up_down":
		if truthy(value) {
			return "up", nil
		}
		return "down", nil
	case "on_off":
		if truthy(value) {
			return "on", nil
		}
		return "off", nil
	case "percent":
		number, err := floatValue(value)
		if err != nil {
			return nil, err
		}
		return fmt.Sprintf("%.*f%%", generator.Precision, number), nil
	case "join":
		reflected := reflect.ValueOf(value)
		if reflected.Kind() != reflect.Array && reflected.Kind() != reflect.Slice {
			return nil, fmt.Errorf("join generator requires an array, got %T", value)
		}
		separator := generator.Separator
		if separator == "" {
			separator = ", "
		}
		items := make([]string, reflected.Len())
		for index := 0; index < reflected.Len(); index++ {
			items[index] = fmt.Sprint(reflected.Index(index).Interface())
		}
		return strings.Join(items, separator), nil
	default:
		return nil, fmt.Errorf("unsupported source generator kind %q", generator.Kind)
	}
}

func integerValue(value any) (int64, error) {
	switch typed := value.(type) {
	case int:
		return int64(typed), nil
	case int64:
		return typed, nil
	case int32:
		return int64(typed), nil
	case uint:
		if uint64(typed) > math.MaxInt64 {
			return 0, fmt.Errorf("integer %d exceeds int64", typed)
		}
		return int64(typed), nil
	case uint32:
		return int64(typed), nil
	case int8:
		return int64(typed), nil
	case int16:
		return int64(typed), nil
	case uint64:
		if typed > uint64(^uint64(0)>>1) {
			return 0, fmt.Errorf("integer %d exceeds int64", typed)
		}
		return int64(typed), nil
	case json.Number:
		return typed.Int64()
	case float64:
		return int64(typed), nil
	case string:
		return strconv.ParseInt(typed, 10, 64)
	default:
		return 0, fmt.Errorf("cannot convert %T to integer", value)
	}
}

func floatValue(value any) (float64, error) {
	switch typed := value.(type) {
	case float64:
		return typed, nil
	case float32:
		return float64(typed), nil
	case int:
		return float64(typed), nil
	case int64:
		return float64(typed), nil
	case int32:
		return float64(typed), nil
	case uint:
		return float64(typed), nil
	case uint64:
		return float64(typed), nil
	case json.Number:
		return typed.Float64()
	case string:
		return strconv.ParseFloat(typed, 64)
	default:
		return 0, fmt.Errorf("cannot convert %T to number", value)
	}
}

func truthy(value any) bool {
	switch typed := value.(type) {
	case bool:
		return typed
	case string:
		return typed != "" && typed != "0" && !strings.EqualFold(typed, "false")
	case int:
		return typed != 0
	case int64:
		return typed != 0
	case float64:
		return typed != 0
	case float32:
		return typed != 0
	case uint:
		return typed != 0
	case uint64:
		return typed != 0
	case json.Number:
		number, err := typed.Float64()
		return err == nil && number != 0
	case nil:
		return false
	default:
		return true
	}
}

func generate(generator outputfsm.Generator, entropy string) (any, error) {
	digest := sha256.Sum256([]byte(entropy))
	number := binary.BigEndian.Uint64(digest[:8])
	switch generator.Kind {
	case "integer":
		minimum := big.NewInt(generator.Min)
		span := new(big.Int).Sub(big.NewInt(generator.Max), minimum)
		span.Add(span, big.NewInt(1))
		value := new(big.Int).SetBytes(digest[:])
		value.Mod(value, span)
		value.Add(value, minimum)
		return value.Int64(), nil
	case "choice":
		return generator.Values[number%uint64(len(generator.Values))], nil
	case "hex":
		const alphabet = "0123456789ABCDEF"
		var builder strings.Builder
		builder.Grow(len(generator.Prefix) + generator.Length)
		builder.WriteString(generator.Prefix)
		for index := 0; index < generator.Length; index++ {
			builder.WriteByte(alphabet[digest[index%len(digest)]&0x0f])
		}
		return builder.String(), nil
	case "mac":
		prefix := strings.ToLower(strings.ReplaceAll(strings.ReplaceAll(generator.Prefix, ":", ""), "-", ""))
		hex := prefix
		for index := 0; len(hex) < 12; index++ {
			hex += fmt.Sprintf("%02x", digest[index%len(digest)])
		}
		hex = hex[:12]
		parts := make([]string, 0, 6)
		for index := 0; index < 12; index += 2 {
			parts = append(parts, hex[index:index+2])
		}
		return strings.Join(parts, ":"), nil
	case "ipv4":
		// Stay in the RFC 5737 TEST-NET-2 range so generated examples are
		// unambiguously non-routable documentation addresses.
		return fmt.Sprintf("198.51.100.%d", 1+(number%254)), nil
	default:
		return nil, fmt.Errorf("unsupported generator kind %q", generator.Kind)
	}
}
