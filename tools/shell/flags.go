package shell

import (
	"fmt"
	"strings"
)

// optionSet describes the options of one command the way GNU getopt_long
// reads them: clustered short flags (-rf), short values attached or separate
// (-n5, -n 5), long options with = or a separate value, "--" ending the
// options, and options after operands unless inOrder is set.
type optionSet struct {
	// flags are the short options without a value, e.g. "rfv".
	flags string
	// valued are the short options that take a value, e.g. "n".
	valued string
	// long maps a long option name to its canonical option.
	long map[string]longOption
	// inOrder stops at the first operand, for commands whose operands are a
	// command line (xargs, env).
	inOrder bool
}

// longOption is the canonical name of a long option (usually its short
// letter) and whether it takes a value.
type longOption struct {
	name   string
	valued bool
}

// option is one parsed option: its canonical name and its value.
type option struct {
	name  string
	value string
}

// options are the parsed options of one invocation, in command-line order.
type options []option

// has reports whether the option was given.
func (o options) has(name string) bool {
	for _, parsed := range o {
		if parsed.name == name {
			return true
		}
	}
	return false
}

// usageError is a command-line error GNU tools print followed by a "Try
// --help" line.
type usageError struct {
	message string
}

func (e *usageError) Error() string { return e.message }

// parse splits arguments into options and operands. Errors are *usageError
// with GNU getopt's wording.
func (set optionSet) parse(arguments []string) (options, []string, error) {
	parsed := options{}
	operands := []string{}
	for index := 0; index < len(arguments); index++ {
		argument := arguments[index]
		switch {
		case argument == "--":
			return parsed, append(operands, arguments[index+1:]...), nil
		case strings.HasPrefix(argument, "--"):
			consumed, err := set.parseLong(argument, arguments[index+1:], &parsed)
			if err != nil {
				return nil, nil, err
			}
			index += consumed
		case strings.HasPrefix(argument, "-") && argument != "-":
			consumed, err := set.parseShort(argument, arguments[index+1:], &parsed)
			if err != nil {
				return nil, nil, err
			}
			index += consumed
		case set.inOrder:
			return parsed, append(operands, arguments[index:]...), nil
		default:
			operands = append(operands, argument)
		}
	}
	return parsed, operands, nil
}

// parseLong reads one --name or --name=value option and returns how many of
// the following arguments it consumed as its value.
func (set optionSet) parseLong(argument string, rest []string, parsed *options) (int, error) {
	name, value, hasValue := strings.Cut(argument[2:], "=")
	known, ok := set.long[name]
	switch {
	case !ok:
		return 0, &usageError{fmt.Sprintf("unrecognized option '--%s'", name)}
	case !known.valued && hasValue:
		return 0, &usageError{fmt.Sprintf("option '--%s' doesn't allow an argument", name)}
	case !known.valued:
		*parsed = append(*parsed, option{name: known.name})
		return 0, nil
	case hasValue:
		*parsed = append(*parsed, option{name: known.name, value: value})
		return 0, nil
	case len(rest) == 0:
		return 0, &usageError{fmt.Sprintf("option '--%s' requires an argument", name)}
	}
	*parsed = append(*parsed, option{name: known.name, value: rest[0]})
	return 1, nil
}

// parseShort reads one cluster of short options such as -rf or -n5 and
// returns how many of the following arguments it consumed as a value.
func (set optionSet) parseShort(argument string, rest []string, parsed *options) (int, error) {
	for position, letter := range argument[1:] {
		name := string(letter)
		switch {
		case strings.ContainsRune(set.flags, letter):
			*parsed = append(*parsed, option{name: name})
		case strings.ContainsRune(set.valued, letter):
			if attached := argument[position+2:]; attached != "" {
				*parsed = append(*parsed, option{name: name, value: attached})
				return 0, nil
			}
			if len(rest) == 0 {
				return 0, &usageError{fmt.Sprintf("option requires an argument -- '%s'", name)}
			}
			*parsed = append(*parsed, option{name: name, value: rest[0]})
			return 1, nil
		default:
			return 0, &usageError{fmt.Sprintf("invalid option -- '%s'", name)}
		}
	}
	return 0, nil
}
