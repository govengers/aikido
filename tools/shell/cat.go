package shell

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
)

func catCommand() Command {
	return Command{
		Name:    "cat",
		Summary: "print files (-n -b number lines, -s squeeze blanks, -A -E -T -v -e -t show ends, tabs, control bytes; - is stdin)",
		Run:     runCat,
	}
}

var catOptions = optionSet{flags: "AbeEnstTuv", long: map[string]longOption{
	"show-all": {name: "A"}, "number-nonblank": {name: "b"}, "show-ends": {name: "E"}, "number": {name: "n"},
	"squeeze-blank": {name: "s"}, "show-tabs": {name: "T"}, "show-nonprinting": {name: "v"},
}}

func runCat(ctx context.Context, invocation *Invocation) error {
	parsed, operands, err := catOptions.parse(invocation.Arguments)
	if err != nil {
		return usage(invocation, "cat", err, errFailed)
	}
	if len(operands) == 0 {
		operands = []string{"-"}
	}
	output := &catOutput{format: catFormatOf(parsed)}
	var status error
	for _, operand := range operands {
		reader, err := openInput(ctx, invocation, operand)
		if err != nil {
			complain(invocation, "cat", "%s: %v", operand, err)
			status = errFailed
			continue
		}
		if err := output.copy(invocation.Stdout, reader); err != nil {
			return err
		}
	}
	return status
}

// catFormat is what cat does to its input; the zero value copies it.
type catFormat struct {
	number, numberNonblank, squeezeBlank bool
	showEnds, showTabs, showNonprinting  bool
}

// catFormatOf reads cat's options: -A is -vET, -e is -vE, -t is -vT, -b
// numbers non-blank lines only and wins over -n, -u is ignored.
func catFormatOf(parsed options) catFormat {
	format := catFormat{}
	for _, option := range parsed {
		switch option.name {
		case "A":
			format.showNonprinting, format.showEnds, format.showTabs = true, true, true
		case "b":
			format.numberNonblank = true
		case "e":
			format.showNonprinting, format.showEnds = true, true
		case "E":
			format.showEnds = true
		case "n":
			format.number = true
		case "s":
			format.squeezeBlank = true
		case "t":
			format.showNonprinting, format.showTabs = true, true
		case "T":
			format.showTabs = true
		case "v":
			format.showNonprinting = true
		}
	}
	format.number = format.number || format.numberNonblank
	return format
}

// catOutput is cat's output stream. Line numbers and blank-line squeezing
// carry across files: GNU cat treats all inputs as one stream.
type catOutput struct {
	format catFormat
	line   int
	// newlines counts line ends since the last non-empty line, as GNU cat
	// does: -1 inside an unterminated line, 0 after a line, 1 after one
	// empty line, 2 after more.
	newlines int
}

func (c *catOutput) copy(output io.Writer, input io.Reader) error {
	if c.format == (catFormat{}) {
		_, err := io.Copy(output, input)
		return err
	}
	reader := bufio.NewReader(input)
	for {
		line, readErr := reader.ReadBytes('\n')
		if rendered := c.render(line); len(rendered) > 0 {
			if _, err := output.Write(rendered); err != nil {
				return err
			}
		}
		if errors.Is(readErr, io.EOF) {
			return nil
		}
		if readErr != nil {
			return readErr
		}
	}
}

// render formats one line read with its newline, if it had one. A squeezed
// blank line, and the empty read at the end of a file, render as nothing.
func (c *catOutput) render(line []byte) []byte {
	if len(line) == 0 {
		return nil
	}
	content, terminated := bytes.CutSuffix(line, []byte("\n"))
	if len(content) == 0 {
		return c.renderBlank()
	}
	var rendered []byte
	if c.newlines >= 0 && c.format.number {
		rendered = c.appendLineNumber(rendered)
	}
	c.newlines = -1
	if terminated {
		c.newlines = 0
	}
	return c.appendContent(rendered, content, terminated)
}

// renderBlank formats an empty line; -s drops the second one in a row.
func (c *catOutput) renderBlank() []byte {
	c.newlines = min(c.newlines+1, 2)
	if c.newlines == 2 && c.format.squeezeBlank {
		return nil
	}
	var rendered []byte
	if c.newlines > 0 && c.format.number && !c.format.numberNonblank {
		rendered = c.appendLineNumber(rendered)
	}
	return c.appendContent(rendered, nil, true)
}

func (c *catOutput) appendLineNumber(rendered []byte) []byte {
	c.line++
	return fmt.Appendf(rendered, "%6d\t", c.line)
}

// appendContent appends a line: with -T tabs as ^I, with -v control bytes
// as ^X, DEL as ^? and bytes from 128 up as M- and the byte below, and with
// -E a $ before the newline (GNU shows a CR before it as ^M with -E alone
// too).
func (c *catOutput) appendContent(rendered, content []byte, terminated bool) []byte {
	carriageReturn := false
	if terminated && c.format.showEnds {
		content, carriageReturn = bytes.CutSuffix(content, []byte("\r"))
	}
	for _, character := range content {
		switch {
		case character == '\t' && c.format.showTabs:
			rendered = append(rendered, '^', 'I')
		case !c.format.showNonprinting || character == '\t':
			rendered = append(rendered, character)
		default:
			rendered = appendVisible(rendered, character)
		}
	}
	if carriageReturn {
		rendered = append(rendered, '^', 'M')
	}
	if terminated {
		if c.format.showEnds {
			rendered = append(rendered, '$')
		}
		rendered = append(rendered, '\n')
	}
	return rendered
}

// appendVisible appends cat -v's notation for a byte other than tab and
// newline.
func appendVisible(rendered []byte, character byte) []byte {
	if character >= 128 {
		rendered = append(rendered, 'M', '-')
		character -= 128
	}
	switch {
	case character < 32:
		return append(rendered, '^', character+64)
	case character == 127:
		return append(rendered, '^', '?')
	}
	return append(rendered, character)
}
