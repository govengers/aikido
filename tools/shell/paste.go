package shell

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"strings"
)

func pasteCommand() Command {
	return Command{Name: "paste", Summary: "merge lines of files side by side (-s serial, -d LIST delimiters; - is stdin)", Run: runPaste}
}

func runPaste(ctx context.Context, invocation *Invocation) error {
	parsed, operands, err := optionSet{flags: "s", valued: "d", long: map[string]longOption{
		"serial": {name: "s"}, "delimiters": {name: "d", valued: true},
	}}.parse(invocation.Arguments)
	if err != nil {
		return usage(invocation, "paste", err, errFailed)
	}
	delimiters := []string{"\t"}
	for _, option := range parsed {
		if option.name == "d" {
			if delimiters, err = pasteDelimiters(option.value); err != nil {
				complain(invocation, "paste", "%v", err)
				return errFailed
			}
		}
	}
	inputs, err := readPasteInputs(ctx, invocation, operandsOrStdin(operands))
	if err != nil {
		return err
	}
	output := bufio.NewWriter(invocation.Stdout)
	if parsed.has("s") {
		pasteSerial(output, inputs, delimiters)
	} else {
		pasteParallel(output, inputs, delimiters)
	}
	if err := output.Flush(); err != nil {
		return fmt.Errorf("write output: %w", err)
	}
	return nil
}

// pasteDelimiters reads -d LIST: one delimiter per character, with \n, \t,
// \\ and \0 (no delimiter) escapes, used in turn.
func pasteDelimiters(list string) ([]string, error) {
	delimiters := []string{}
	for index := 0; index < len(list); index++ {
		if list[index] != '\\' {
			delimiters = append(delimiters, list[index:index+1])
			continue
		}
		index++
		if index == len(list) {
			return nil, fmt.Errorf("delimiter list ends with an unescaped backslash: %s", list)
		}
		switch list[index] {
		case 'n':
			delimiters = append(delimiters, "\n")
		case 't':
			delimiters = append(delimiters, "\t")
		case '0':
			delimiters = append(delimiters, "")
		default:
			delimiters = append(delimiters, list[index:index+1])
		}
	}
	if len(delimiters) == 0 {
		delimiters = []string{""}
	}
	return delimiters, nil
}

// pasteInput is one operand's lines. Every "-" shares stdin's lines: each
// reads the next one, as with GNU paste - -.
type pasteInput struct {
	lines *[]string
}

func (p pasteInput) next() (string, bool) {
	if len(*p.lines) == 0 {
		return "", false
	}
	line := (*p.lines)[0]
	*p.lines = (*p.lines)[1:]
	return line, true
}

func readPasteInputs(ctx context.Context, invocation *Invocation, operands []string) ([]pasteInput, error) {
	var stdin *[]string
	inputs := make([]pasteInput, 0, len(operands))
	for _, operand := range operands {
		if operand == "-" && stdin != nil {
			inputs = append(inputs, pasteInput{lines: stdin})
			continue
		}
		content, err := readOperand(ctx, invocation, operand)
		if reason, ok := operandReason(err); ok {
			complain(invocation, "paste", "%s: %s", operand, reason)
			return nil, errFailed
		}
		if err != nil {
			return nil, err
		}
		lines, _ := splitLines(string(content))
		inputs = append(inputs, pasteInput{lines: &lines})
		if operand == "-" {
			stdin = &lines
		}
	}
	return inputs, nil
}

// pasteParallel writes line N of every input joined by the delimiters, until
// all are exhausted; an exhausted input contributes an empty field.
func pasteParallel(output io.Writer, inputs []pasteInput, delimiters []string) {
	for {
		fields, some := make([]string, len(inputs)), false
		for index, input := range inputs {
			line, ok := input.next()
			fields[index], some = line, some || ok
		}
		if !some {
			return
		}
		fmt.Fprintln(output, joinWithDelimiters(fields, delimiters))
	}
}

// pasteSerial writes each input's lines as one line.
func pasteSerial(output io.Writer, inputs []pasteInput, delimiters []string) {
	for _, input := range inputs {
		var fields []string
		for line, ok := input.next(); ok; line, ok = input.next() {
			fields = append(fields, line)
		}
		fmt.Fprintln(output, joinWithDelimiters(fields, delimiters))
	}
}

func joinWithDelimiters(fields, delimiters []string) string {
	var joined strings.Builder
	for index, field := range fields {
		if index > 0 {
			joined.WriteString(delimiters[(index-1)%len(delimiters)])
		}
		joined.WriteString(field)
	}
	return joined.String()
}
