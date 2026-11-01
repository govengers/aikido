package main

import (
	"regexp"
	"strconv"
	"strings"
)

// matcher is one label matcher of a stream selector.
type matcher struct {
	name, operator, value string
	pattern               *regexp.Regexp
}

type selector []matcher

func (matchers selector) matches(labels map[string]string) bool {
	for _, label := range matchers {
		value := labels[label.name]
		var hit bool
		switch label.operator {
		case "=", "!=":
			hit = value == label.value
		default:
			hit = label.pattern.MatchString(value)
		}
		if hit == strings.HasPrefix(label.operator, "!") {
			return false
		}
	}
	return true
}

// lineFilter is |= "x", != "x", |~ "re" or !~ "re".
type lineFilter struct {
	operator, value string
	pattern         *regexp.Regexp
}

type lineFilters []lineFilter

func (filters lineFilters) keep(line string) bool {
	for _, filter := range filters {
		var hit bool
		if filter.pattern != nil {
			hit = filter.pattern.MatchString(line)
		} else {
			hit = strings.Contains(line, filter.value)
		}
		if hit == strings.HasPrefix(filter.operator, "!") {
			return false
		}
	}
	return true
}

func parseError(detail string) error {
	return failure("Error response from server: parse error : %s", detail)
}

// parseLogQL reads a log query: a stream selector and line filters.
// ponytail: parser and label-filter stages (| json, | logfmt, | level="x")
// are accepted and ignored; metric queries are refused.
func parseLogQL(text string) (selector, lineFilters, error) {
	text = strings.TrimSpace(text)
	if !strings.HasPrefix(text, "{") {
		return nil, nil, failure("Error response from server: only log queries are served to logcli here, start the query with a stream selector like {app=\"shop-api\"}")
	}
	closing := indexOutsideQuotes(text, '}')
	if closing < 0 {
		return nil, nil, parseError("syntax error: unexpected end of input, expecting }")
	}
	matchers, err := parseSelector(text[1:closing])
	if err != nil {
		return nil, nil, err
	}
	filters, err := parsePipeline(text[closing+1:])
	return matchers, filters, err
}

func parseSelector(body string) (selector, error) {
	var matchers selector
	nonEmpty := false
	for _, part := range splitOutsideQuotes(body, ',') {
		if part = strings.TrimSpace(part); part == "" {
			continue
		}
		label, err := parseMatcher(part)
		if err != nil {
			return nil, err
		}
		nonEmpty = nonEmpty || label.value != "" && (label.operator == "=" || label.operator == "=~" && label.value != ".*")
		matchers = append(matchers, label)
	}
	if !nonEmpty {
		return nil, parseError("queries require at least one regexp or equality matcher that does not have an empty-compatible value. For instance, app=~\".*\" does not meet this requirement, but app=~\".+\" will")
	}
	return matchers, nil
}

func parseMatcher(part string) (matcher, error) {
	for _, operator := range []string{"=~", "!~", "!=", "="} {
		name, raw, ok := strings.Cut(part, operator)
		if !ok {
			continue
		}
		value, rest, err := readQuoted(strings.TrimSpace(raw))
		if err != nil || strings.TrimSpace(rest) != "" {
			return matcher{}, parseError("syntax error: unexpected IDENTIFIER, expecting STRING in " + strconv.Quote(part))
		}
		label := matcher{name: strings.TrimSpace(name), operator: operator, value: value}
		if strings.Contains(operator, "~") {
			pattern, err := regexp.Compile("^(?:" + value + ")$")
			if err != nil {
				return matcher{}, parseError(err.Error())
			}
			label.pattern = pattern
		}
		return label, nil
	}
	return matcher{}, parseError("syntax error: unexpected IDENTIFIER in " + strconv.Quote(part))
}

func parsePipeline(rest string) (lineFilters, error) {
	var filters lineFilters
	for rest = strings.TrimSpace(rest); rest != ""; rest = strings.TrimSpace(rest) {
		operator := ""
		for _, candidate := range []string{"|=", "!=", "|~", "!~"} {
			if strings.HasPrefix(rest, candidate) {
				operator = candidate
			}
		}
		if operator == "" {
			if !strings.HasPrefix(rest, "|") {
				return nil, parseError("syntax error: unexpected " + strconv.Quote(rest))
			}
			next := nextStage(rest[1:])
			rest = rest[1+next:]
			continue
		}
		value, remaining, err := readQuoted(strings.TrimSpace(rest[len(operator):]))
		if err != nil {
			return nil, err
		}
		filter := lineFilter{operator: operator, value: value}
		if strings.Contains(operator, "~") {
			if filter.pattern, err = regexp.Compile(value); err != nil {
				return nil, parseError(err.Error())
			}
		}
		filters, rest = append(filters, filter), remaining
	}
	return filters, nil
}

// nextStage is the offset of the next pipeline operator outside quotes.
func nextStage(text string) int {
	quote := rune(0)
	for index, character := range text {
		switch {
		case quote != 0:
			if character == quote {
				quote = 0
			}
		case character == '"' || character == '`':
			quote = character
		case character == '|' || character == '!' && index+1 < len(text) && (text[index+1] == '=' || text[index+1] == '~'):
			return index
		}
	}
	return len(text)
}

// readQuoted reads a "double-quoted" or `backtick` string at the start of text.
func readQuoted(text string) (value, rest string, err error) {
	if strings.HasPrefix(text, "`") {
		end := strings.Index(text[1:], "`")
		if end < 0 {
			return "", "", parseError("syntax error: unterminated raw string")
		}
		return text[1 : end+1], text[end+2:], nil
	}
	if !strings.HasPrefix(text, `"`) {
		return "", "", parseError("syntax error: unexpected IDENTIFIER, expecting STRING")
	}
	for index := 1; index < len(text); index++ {
		switch text[index] {
		case '\\':
			index++
		case '"':
			value, err := strconv.Unquote(text[:index+1])
			if err != nil {
				return "", "", parseError(err.Error())
			}
			return value, text[index+1:], nil
		}
	}
	return "", "", parseError("syntax error: unterminated string")
}

func indexOutsideQuotes(text string, target rune) int {
	quote, escaped := rune(0), false
	for index, character := range text {
		switch {
		case escaped:
			escaped = false
		case quote == '"' && character == '\\':
			escaped = true
		case quote != 0:
			if character == quote {
				quote = 0
			}
		case character == '"' || character == '`':
			quote = character
		case character == target:
			return index
		}
	}
	return -1
}

func splitOutsideQuotes(text string, separator rune) []string {
	var parts []string
	for {
		index := indexOutsideQuotes(text, separator)
		if index < 0 {
			return append(parts, text)
		}
		parts, text = append(parts, text[:index]), text[index+1:]
	}
}
