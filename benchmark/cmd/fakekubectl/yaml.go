package main

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// toYAML renders a decoded JSON document (maps, slices, strings, json.Number,
// bools, nil) as block YAML with sorted keys, which is how kubectl prints
// -o yaml.
func toYAML(document any) string {
	var builder strings.Builder
	writeYAMLValue(&builder, document, 0)
	return builder.String()
}

func writeYAMLValue(builder *strings.Builder, value any, indent int) {
	switch typed := value.(type) {
	case map[string]any:
		keys := make([]string, 0, len(typed))
		for key := range typed {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			builder.WriteString(strings.Repeat(" ", indent) + yamlScalar(key) + ":")
			writeYAMLChild(builder, typed[key], indent+2)
		}
	case []any:
		for _, element := range typed {
			builder.WriteString(strings.Repeat(" ", indent) + "-")
			writeYAMLListElement(builder, element, indent+2)
		}
	default:
		builder.WriteString(strings.Repeat(" ", indent) + yamlScalar(typed) + "\n")
	}
}

// writeYAMLChild writes the value after "key:": inline for scalars and empty
// collections, on the next lines otherwise. Lists under a key keep the key's
// indentation, like kubectl.
func writeYAMLChild(builder *strings.Builder, value any, indent int) {
	switch typed := value.(type) {
	case map[string]any:
		if len(typed) == 0 {
			builder.WriteString(" {}\n")
			return
		}
		builder.WriteString("\n")
		writeYAMLValue(builder, typed, indent)
	case []any:
		if len(typed) == 0 {
			builder.WriteString(" []\n")
			return
		}
		builder.WriteString("\n")
		writeYAMLValue(builder, typed, indent-2)
	default:
		builder.WriteString(" " + yamlScalar(typed) + "\n")
	}
}

// writeYAMLListElement writes the value after "-": a map's first key on the
// dash line, the rest indented below it.
func writeYAMLListElement(builder *strings.Builder, value any, indent int) {
	asMap, isMap := value.(map[string]any)
	if !isMap || len(asMap) == 0 {
		writeYAMLChild(builder, value, indent)
		return
	}
	var nested strings.Builder
	writeYAMLValue(&nested, asMap, indent)
	builder.WriteString(" " + strings.TrimPrefix(nested.String(), strings.Repeat(" ", indent)))
}

func yamlScalar(value any) string {
	switch typed := value.(type) {
	case nil:
		return "null"
	case bool:
		return strconv.FormatBool(typed)
	case json.Number:
		return typed.String()
	case float64:
		return strconv.FormatFloat(typed, 'f', -1, 64)
	case string:
		return yamlString(typed)
	default:
		return fmt.Sprint(typed)
	}
}

// yamlString quotes strings YAML would read as something else: a leading
// indicator character, ": " or " #" inside, surrounding blanks, line breaks.
func yamlString(text string) string {
	if text == "" || strings.TrimSpace(text) != text || strings.ContainsAny(text, "\n\t\"") ||
		strings.Contains(text, ": ") || strings.Contains(text, " #") || strings.HasSuffix(text, ":") ||
		strings.ContainsAny(text[:1], "-?:,[]{}#&*!|>'%@`") {
		return strconv.Quote(text)
	}
	switch strings.ToLower(text) {
	case "true", "false", "yes", "no", "on", "off", "null", "~":
		return strconv.Quote(text)
	}
	if _, err := strconv.ParseFloat(text, 64); err == nil {
		return strconv.Quote(text)
	}
	return text
}
