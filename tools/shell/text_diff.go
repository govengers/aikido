package shell

import (
	"bytes"
	"context"
	"fmt"
	"path"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/govengers/aikido/vfs"
)

var diffTool = &textTool{
	name:        "diff",
	hint:        "diff: Try 'diff --help' for more information.\n",
	usageStatus: 2,
	help: `Usage: diff [OPTION]... FILES
Compare FILES line by line.
  -q, --brief                   report only when files differ
  -s, --report-identical-files  report when two files are the same
  -u, -U NUM, --unified[=NUM]   output NUM (default 3) lines of unified context
  -r, --recursive               recursively compare any subdirectories found
  -N, --new-file                treat absent files as empty
FILES are 'FILE1 FILE2' or 'DIR1 DIR2' or 'DIR FILE' or 'FILE DIR'.
Exit status is 0 if inputs are the same, 1 if different, 2 if trouble.
`,
}

var diffParser = &optionParser{specs: []optionSpec{
	{short: 'q', long: "brief"},
	{short: 's', long: "report-identical-files"},
	{short: 'u'},
	{short: 'U', argument: requiredArgument},
	{long: "unified", argument: optionalArgument},
	{short: 'r', long: "recursive"},
	{short: 'N', long: "new-file"},
	helpOption,
}}

func diffCommand() Command {
	return Command{Name: "diff", Summary: "compare files line by line (-u -U N -q -s -r -N)", Run: runDiff}
}

// diffSettings is a parsed diff command line.
type diffSettings struct {
	brief, reportIdentical, recursive, newFile bool
	// context is the number of unified context lines, -1 for normal output.
	context int
	// switches are the options as typed, for "diff -r a/x b/x" lines.
	switches string
}

func runDiff(ctx context.Context, invocation *Invocation) error {
	line, err := diffTool.parseOptions(invocation, diffParser)
	if line.done {
		return err
	}
	settings := diffSettings{context: -1}
	var switches []string
	for _, option := range line.options {
		switch option.key {
		case "brief":
			settings.brief = true
		case "report-identical-files":
			settings.reportIdentical = true
		case "recursive":
			settings.recursive = true
		case "new-file":
			settings.newFile = true
		case "u":
			settings.context = max(settings.context, 3)
		case "U", "unified":
			if settings.context, err = parseDiffContext(option); err != nil {
				return diffTool.usageFailure(invocation, err.Error())
			}
		}
	}
	for _, argument := range invocation.Arguments {
		if strings.HasPrefix(argument, "-") && !slices.Contains(line.operands, argument) {
			switches = append(switches, argument)
		}
	}
	settings.switches = strings.Join(switches, " ")
	switch {
	case len(line.operands) == 0:
		return diffTool.usageFailure(invocation, "missing operand after 'diff'")
	case len(line.operands) == 1:
		return diffTool.usageFailure(invocation, fmt.Sprintf("missing operand after '%s'", line.operands[0]))
	case len(line.operands) > 2:
		return diffTool.usageFailure(invocation, fmt.Sprintf("extra operand '%s'", line.operands[2]))
	}
	differ := &differ{settings: settings, invocation: invocation}
	status, err := differ.compareOperands(ctx, line.operands[0], line.operands[1])
	if err != nil {
		return err
	}
	if _, err := fmt.Fprint(invocation.Stdout, differ.output.String()); err != nil {
		return fmt.Errorf("write output: %w", err)
	}
	return exitStatus(status)
}

func parseDiffContext(option parsedOption) (int, error) {
	if !option.hasValue {
		return 3, nil
	}
	value, err := strconv.Atoi(option.value)
	if err != nil || value < 0 {
		return 0, fmt.Errorf("invalid context length '%s'", option.value)
	}
	return value, nil
}

// differ compares operands and collects the output.
type differ struct {
	settings   diffSettings
	invocation *Invocation
	output     strings.Builder
}

// diffEntry is one side of a comparison.
type diffEntry struct {
	name      string
	directory bool
	missing   bool
	content   []byte
	updated   time.Time
}

// compareOperands compares the two command-line operands; a directory
// against a file compares the file of the same name inside it.
func (d *differ) compareOperands(ctx context.Context, left, right string) (ExitError, error) {
	leftEntry, err := d.load(ctx, left)
	if err != nil {
		return 0, err
	}
	rightEntry, err := d.load(ctx, right)
	if err != nil {
		return 0, err
	}
	switch {
	case leftEntry.directory && !rightEntry.directory && !rightEntry.missing:
		if leftEntry, err = d.load(ctx, path.Join(left, path.Base(right))); err != nil {
			return 0, err
		}
	case rightEntry.directory && !leftEntry.directory && !leftEntry.missing:
		if rightEntry, err = d.load(ctx, path.Join(right, path.Base(left))); err != nil {
			return 0, err
		}
	}
	for _, entry := range []diffEntry{leftEntry, rightEntry} {
		if entry.missing && !d.settings.newFile {
			diffTool.complain(d.invocation, "%s: %s", entry.name, reasonNoSuchFile)
			return 2, nil
		}
	}
	if leftEntry.directory && rightEntry.directory {
		return d.compareDirectories(ctx, left, right)
	}
	return d.compareFiles(leftEntry, rightEntry, false), nil
}

// load reads one operand; a missing file or a directory is not an error.
func (d *differ) load(ctx context.Context, name string) (diffEntry, error) {
	entry := diffEntry{name: name}
	if name == "-" {
		content, err := readOperand(ctx, d.invocation, name)
		entry.content, entry.updated = content, time.Now()
		return entry, err
	}
	content, err := readOperand(ctx, d.invocation, name)
	reason, ok := operandReason(err)
	switch {
	case ok && reason == reasonIsDirectory:
		entry.directory = true
	case ok:
		entry.missing = true
	case err != nil:
		return entry, err
	}
	entry.content = content
	if resolved, resolveErr := Resolve(d.invocation.Directory, name); resolveErr == nil && !ok {
		entry.updated = d.modificationTime(ctx, resolved)
	}
	return entry, nil
}

// modificationTime is the file's UpdatedAt, the epoch when unknown.
func (d *differ) modificationTime(ctx context.Context, resolved string) time.Time {
	_, metadata, err := d.invocation.Storage.ReadFile(ctx, resolved)
	if err != nil || metadata.UpdatedAt.IsZero() {
		return time.Unix(0, 0)
	}
	return metadata.UpdatedAt
}

// compareFiles prints the difference of two files and returns 1 when they
// differ. inDirectory adds the "diff -r a b" line GNU prints there.
func (d *differ) compareFiles(left, right diffEntry, inDirectory bool) ExitError {
	if bytes.Equal(left.content, right.content) {
		if d.settings.reportIdentical {
			fmt.Fprintf(&d.output, "Files %s and %s are identical\n", left.name, right.name)
		}
		return 0
	}
	if d.settings.brief || bytes.IndexByte(left.content, 0) >= 0 || bytes.IndexByte(right.content, 0) >= 0 {
		if d.settings.brief {
			fmt.Fprintf(&d.output, "Files %s and %s differ\n", left.name, right.name)
		} else {
			fmt.Fprintf(&d.output, "Binary files %s and %s differ\n", left.name, right.name)
		}
		return 1
	}
	if inDirectory {
		d.output.WriteString(strings.Join(slices.DeleteFunc([]string{"diff", d.settings.switches, left.name, right.name}, func(part string) bool { return part == "" }), " ") + "\n")
	}
	leftLines, leftTerminated := splitLines(string(left.content))
	rightLines, rightTerminated := splitLines(string(right.content))
	keyedLeft, keyedRight := keyLines(leftLines, leftTerminated), keyLines(rightLines, rightTerminated)
	hunks := diffLines(keyedLeft, keyedRight)
	printer := &diffPrinter{output: &d.output, left: leftLines, right: rightLines, leftTerminated: leftTerminated, rightTerminated: rightTerminated}
	if d.settings.context >= 0 {
		printer.unifiedHeader(left, right)
		printer.unified(hunks, d.settings.context)
	} else {
		printer.normal(hunks)
	}
	return 1
}

// keyLines makes a last line without newline differ from the same text
// with one, as GNU diff compares them.
func keyLines(lines []string, terminated bool) []string {
	if terminated || len(lines) == 0 {
		return lines
	}
	keyed := slices.Clone(lines)
	keyed[len(keyed)-1] += "\x00no newline"
	return keyed
}

// compareDirectories compares the entries of two directories by name.
func (d *differ) compareDirectories(ctx context.Context, left, right string) (ExitError, error) {
	leftNames, err := d.children(ctx, left)
	if err != nil {
		return 0, err
	}
	rightNames, err := d.children(ctx, right)
	if err != nil {
		return 0, err
	}
	names := slices.Compact(slices.Sorted(slices.Values(append(slices.Clone(leftNames), rightNames...))))
	var status ExitError
	for _, name := range names {
		child := diffChild{name: name, inLeft: slices.Contains(leftNames, name), inRight: slices.Contains(rightNames, name)}
		entryStatus, err := d.compareChild(ctx, left, right, child)
		if err != nil {
			return 0, err
		}
		status = max(status, entryStatus)
	}
	return status, nil
}

// diffChild is a name found in one or both of two compared directories.
type diffChild struct {
	name            string
	inLeft, inRight bool
}

// compareChild compares one name of two directories.
func (d *differ) compareChild(ctx context.Context, left, right string, child diffChild) (ExitError, error) {
	leftPath, rightPath := path.Join(left, child.name), path.Join(right, child.name)
	if !d.settings.newFile && (!child.inLeft || !child.inRight) {
		directory := left
		if !child.inLeft {
			directory = right
		}
		fmt.Fprintf(&d.output, "Only in %s: %s\n", directory, child.name)
		return 1, nil
	}
	leftEntry, err := d.load(ctx, leftPath)
	if err != nil {
		return 0, err
	}
	rightEntry, err := d.load(ctx, rightPath)
	if err != nil {
		return 0, err
	}
	switch {
	case leftEntry.directory && rightEntry.directory:
		if !d.settings.recursive {
			fmt.Fprintf(&d.output, "Common subdirectories: %s and %s\n", leftPath, rightPath)
			return 0, nil
		}
		return d.compareDirectories(ctx, leftPath, rightPath)
	case leftEntry.directory != rightEntry.directory && !leftEntry.missing && !rightEntry.missing:
		kind := map[bool]string{true: "directory", false: "regular file"}
		fmt.Fprintf(&d.output, "File %s is a %s while file %s is a %s\n", leftPath, kind[leftEntry.directory], rightPath, kind[rightEntry.directory])
		return 1, nil
	}
	return d.compareFiles(leftEntry, rightEntry, true), nil
}

// children lists the names directly below a directory.
func (d *differ) children(ctx context.Context, directory string) ([]string, error) {
	resolved, err := Resolve(d.invocation.Directory, directory)
	if err != nil {
		return nil, nil //nolint:nilerr // an unresolvable directory has no children
	}
	files, err := d.invocation.Storage.ListFiles(ctx)
	if err != nil {
		return nil, fmt.Errorf("list files: %w", err)
	}
	return childNames(files, resolved), nil
}

// childNames are the first path segments below prefix, sorted and unique.
func childNames(files []vfs.FileMetadata, directory string) []string {
	prefix := directory + "/"
	if directory == "" {
		prefix = ""
	}
	var names []string
	for _, file := range files {
		if rest, ok := strings.CutPrefix(file.Path, prefix); ok {
			first, _, _ := strings.Cut(rest, "/")
			names = append(names, first)
		}
	}
	slices.Sort(names)
	return slices.Compact(names)
}

// diffPrinter renders hunks in normal or unified format.
type diffPrinter struct {
	output                          *strings.Builder
	left, right                     []string
	leftTerminated, rightTerminated bool
}

const noNewlineMarker = "\\ No newline at end of file\n"

// line writes one line with its prefix and GNU's marker when it is the
// last line and lacks a newline.
func (p *diffPrinter) line(prefix string, lines []string, index int, terminated bool) {
	p.output.WriteString(prefix + lines[index] + "\n")
	if index == len(lines)-1 && !terminated {
		p.output.WriteString(noNewlineMarker)
	}
}

// normal writes the default "2c2 / < / --- / >" format.
func (p *diffPrinter) normal(hunks []diffHunk) {
	for _, hunk := range hunks {
		letter := "c"
		switch {
		case hunk.aStart == hunk.aEnd:
			letter = "a"
		case hunk.bStart == hunk.bEnd:
			letter = "d"
		}
		p.output.WriteString(normalRange(hunk.aStart, hunk.aEnd) + letter + normalRange(hunk.bStart, hunk.bEnd) + "\n")
		for index := hunk.aStart; index < hunk.aEnd; index++ {
			p.line("< ", p.left, index, p.leftTerminated)
		}
		if letter == "c" {
			p.output.WriteString("---\n")
		}
		for index := hunk.bStart; index < hunk.bEnd; index++ {
			p.line("> ", p.right, index, p.rightTerminated)
		}
	}
}

// normalRange is "N" or "N,M" in 1-based lines; an empty range names the
// line before it.
func normalRange(start, end int) string {
	switch end - start {
	case 0:
		return strconv.Itoa(start)
	case 1:
		return strconv.Itoa(start + 1)
	}
	return strconv.Itoa(start+1) + "," + strconv.Itoa(end)
}

func (p *diffPrinter) unifiedHeader(left, right diffEntry) {
	const layout = "2006-01-02 15:04:05.000000000 -0700"
	fmt.Fprintf(p.output, "--- %s\t%s\n", left.name, left.updated.UTC().Format(layout))
	fmt.Fprintf(p.output, "+++ %s\t%s\n", right.name, right.updated.UTC().Format(layout))
}

// unified writes the hunks with context lines, merging hunks whose context
// would touch.
func (p *diffPrinter) unified(hunks []diffHunk, context int) {
	for first := 0; first < len(hunks); {
		last := first
		for last+1 < len(hunks) && hunks[last+1].aStart-hunks[last].aEnd <= 2*context {
			last++
		}
		aStart := max(0, hunks[first].aStart-context)
		bStart := max(0, hunks[first].bStart-context)
		aEnd := min(len(p.left), hunks[last].aEnd+context)
		bEnd := min(len(p.right), hunks[last].bEnd+context)
		fmt.Fprintf(p.output, "@@ -%s +%s @@\n", unifiedRange(aStart, aEnd), unifiedRange(bStart, bEnd))
		position := aStart
		for _, hunk := range hunks[first : last+1] {
			for ; position < hunk.aStart; position++ {
				p.line(" ", p.left, position, p.leftTerminated)
			}
			for index := hunk.aStart; index < hunk.aEnd; index++ {
				p.line("-", p.left, index, p.leftTerminated)
			}
			for index := hunk.bStart; index < hunk.bEnd; index++ {
				p.line("+", p.right, index, p.rightTerminated)
			}
			position = hunk.aEnd
		}
		for ; position < aEnd; position++ {
			p.line(" ", p.left, position, p.leftTerminated)
		}
		first = last + 1
	}
}

// unifiedRange is "N", "N,COUNT" or, for no lines, "BEFORE,0".
func unifiedRange(start, end int) string {
	switch end - start {
	case 0:
		return strconv.Itoa(start) + ",0"
	case 1:
		return strconv.Itoa(start + 1)
	}
	return strconv.Itoa(start+1) + "," + strconv.Itoa(end-start)
}
