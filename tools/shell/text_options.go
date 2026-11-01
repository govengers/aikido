package shell

import (
	"fmt"
	"strings"
)

// argumentKind says whether an option takes an argument, like getopt_long's
// no_argument, required_argument and optional_argument.
type argumentKind int

const (
	noArgument argumentKind = iota
	requiredArgument
	// optionalArgument takes an argument only when it is attached: -iSUFFIX,
	// --in-place=SUFFIX.
	optionalArgument
)

// optionSpec is one option a command accepts. Short is 0 for a long-only
// option, Long is "" for a short-only one.
type optionSpec struct {
	short    byte
	long     string
	argument argumentKind
}

// key is the name a parsed option reports: the long name if there is one,
// otherwise the short letter.
func (s *optionSpec) key() string {
	if s.long != "" {
		return s.long
	}
	return string(s.short)
}

// parsedOption is one option found on the command line, in order.
type parsedOption struct {
	key      string
	value    string
	hasValue bool
}

// usageError is a command-line error GNU reports as "name: message" followed
// by the command's usage hint.
type usageError struct{ message string }

func (e *usageError) Error() string { return e.message }

// optionParser parses arguments like glibc's getopt_long with permutation:
// options and operands may mix, "--" ends the options, "-" is an operand,
// long options may be abbreviated to an unambiguous prefix.
type optionParser struct {
	specs []optionSpec
	// digitsKey, when set, makes a run of digits in a short cluster (grep's
	// -5) an option with this key and the digits as value.
	digitsKey string
	// stopAtOperand ends option parsing at the first operand, like a leading
	// "+" in getopt's option string (POSIXLY_CORRECT).
	stopAtOperand bool
}

// parse splits arguments into options and operands. A malformed option
// yields a *usageError with GNU's message.
func (p *optionParser) parse(arguments []string) ([]parsedOption, []string, error) {
	var options []parsedOption
	var operands []string
	for index := 0; index < len(arguments); index++ {
		argument := arguments[index]
		switch {
		case argument == "--":
			return options, append(operands, arguments[index+1:]...), nil
		case strings.HasPrefix(argument, "--"):
			option, consumed, err := p.parseLong(argument, arguments[index+1:])
			if err != nil {
				return nil, nil, err
			}
			options = append(options, option)
			index += consumed
		case len(argument) > 1 && argument[0] == '-':
			found, consumed, err := p.parseShortCluster(argument[1:], arguments[index+1:])
			if err != nil {
				return nil, nil, err
			}
			options = append(options, found...)
			index += consumed
		default:
			if p.stopAtOperand {
				return options, append(operands, arguments[index:]...), nil
			}
			operands = append(operands, argument)
		}
	}
	return options, operands, nil
}

// parseLong parses one "--name" or "--name=value" argument; rest are the
// arguments after it, of which it consumes the returned count.
func (p *optionParser) parseLong(argument string, rest []string) (parsedOption, int, error) {
	name, value, hasValue := strings.Cut(argument[2:], "=")
	spec, err := p.findLong(name, argument)
	if err != nil {
		return parsedOption{}, 0, err
	}
	switch spec.argument {
	case noArgument:
		if hasValue {
			return parsedOption{}, 0, &usageError{fmt.Sprintf("option '--%s' doesn't allow an argument", spec.long)}
		}
	case requiredArgument:
		if !hasValue {
			if len(rest) == 0 {
				return parsedOption{}, 0, &usageError{fmt.Sprintf("option '--%s' requires an argument", spec.long)}
			}
			return parsedOption{key: spec.key(), value: rest[0], hasValue: true}, 1, nil
		}
	case optionalArgument:
	}
	return parsedOption{key: spec.key(), value: value, hasValue: hasValue}, 0, nil
}

// findLong resolves a long option name or an unambiguous prefix of one.
func (p *optionParser) findLong(name, argument string) (*optionSpec, error) {
	var candidates []*optionSpec
	for index := range p.specs {
		spec := &p.specs[index]
		if spec.long == "" || !strings.HasPrefix(spec.long, name) {
			continue
		}
		if spec.long == name {
			return spec, nil
		}
		candidates = append(candidates, spec)
	}
	switch len(candidates) {
	case 0:
		return nil, &usageError{fmt.Sprintf("unrecognized option '%s'", argument)}
	case 1:
		return candidates[0], nil
	}
	possibilities := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		possibilities = append(possibilities, "'--"+candidate.long+"'")
	}
	return nil, &usageError{fmt.Sprintf("option '--%s' is ambiguous; possibilities: %s", name, strings.Join(possibilities, " "))}
}

// parseShortCluster parses the letters of one "-abc" argument.
func (p *optionParser) parseShortCluster(cluster string, rest []string) ([]parsedOption, int, error) {
	var options []parsedOption
	for position := 0; position < len(cluster); position++ {
		letter := cluster[position]
		if p.digitsKey != "" && isDigit(letter) {
			end := position
			for end < len(cluster) && isDigit(cluster[end]) {
				end++
			}
			options = append(options, parsedOption{key: p.digitsKey, value: cluster[position:end], hasValue: true})
			position = end - 1
			continue
		}
		spec := p.findShort(letter)
		if spec == nil {
			return nil, 0, &usageError{fmt.Sprintf("invalid option -- '%c'", letter)}
		}
		attached := cluster[position+1:]
		switch spec.argument {
		case noArgument:
			options = append(options, parsedOption{key: spec.key()})
			continue
		case optionalArgument:
			return append(options, parsedOption{key: spec.key(), value: attached, hasValue: attached != ""}), 0, nil
		case requiredArgument:
		}
		if attached != "" {
			return append(options, parsedOption{key: spec.key(), value: attached, hasValue: true}), 0, nil
		}
		if len(rest) == 0 {
			return nil, 0, &usageError{fmt.Sprintf("option requires an argument -- '%c'", letter)}
		}
		return append(options, parsedOption{key: spec.key(), value: rest[0], hasValue: true}), 1, nil
	}
	return options, 0, nil
}

func (p *optionParser) findShort(letter byte) *optionSpec {
	for index := range p.specs {
		if p.specs[index].short == letter {
			return &p.specs[index]
		}
	}
	return nil
}

func isDigit(character byte) bool { return character >= '0' && character <= '9' }
