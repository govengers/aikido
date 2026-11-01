package shell

import (
	"context"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
)

func findCommand() Command {
	return Command{
		Name: "find",
		Summary: "walk directories in sorted order (-name -iname -path -ipath -type f|d -empty -maxdepth -mindepth " +
			"! -not -a -o ( ) -prune -print -print0 -delete -exec cmd {} ; and +)",
		Run: runFind,
	}
}

// visit is one path find evaluates its expression on.
type visit struct {
	display string // the path as find prints it
	name    string // the workspace path
	entry
	depth int
	prune bool // set by -prune: do not descend
}

// predicate is a compiled find expression: whether it is true for a visit.
type predicate func(ctx context.Context, item *visit) (bool, error)

// finder is one find invocation: the compiled expression and the walk state.
type finder struct {
	invocation *Invocation
	files      workspace
	index      map[string][]child
	expression predicate
	maxDepth   int // -1 for no limit
	minDepth   int
	depthFirst bool // -delete: a directory after its contents
	failed     bool // an operand or an action failed: exit status 1
	batches    []*execBatch
}

func runFind(ctx context.Context, invocation *Invocation) error {
	arguments := invocation.Arguments
	for len(arguments) > 0 && (arguments[0] == "-H" || arguments[0] == "-L" || arguments[0] == "-P") {
		arguments = arguments[1:]
	}
	starts := []string{}
	for len(arguments) > 0 && !startsExpression(arguments[0]) {
		starts, arguments = append(starts, arguments[0]), arguments[1:]
	}
	if len(starts) == 0 {
		starts = []string{"."}
	}
	find := &finder{invocation: invocation, files: workspaceOf(invocation), maxDepth: -1}
	if err := find.compile(arguments); err != nil {
		complain(invocation, "find", "%v", err)
		return errFailed
	}
	index, err := find.files.index(ctx)
	if err != nil {
		return err
	}
	find.index = index
	for _, start := range starts {
		if err := find.walkStart(ctx, start); err != nil {
			return err
		}
	}
	for _, batch := range find.batches {
		if !batch.flush(ctx) {
			find.failed = true
		}
	}
	if find.failed {
		return errFailed
	}
	return nil
}

// startsExpression reports whether a find argument begins the expression
// rather than naming a start path.
func startsExpression(argument string) bool {
	return (strings.HasPrefix(argument, "-") && argument != "-") || argument == "(" || argument == "!" || argument == ")"
}

func (f *finder) walkStart(ctx context.Context, start string) error {
	name, err := resolve(f.invocation, start)
	found := entry{}
	if err == nil {
		found, err = f.files.stat(ctx, name)
	}
	if err != nil || found.kind == entryMissing {
		complain(f.invocation, "find", "'%s': %v", start, errNotFound)
		f.failed = true
		return nil //nolint:nilerr // a missing start path is reported and the walk goes on, like GNU find
	}
	return f.walk(ctx, &visit{display: start, name: name, entry: found})
}

func (f *finder) walk(ctx context.Context, item *visit) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !f.depthFirst {
		if err := f.evaluate(ctx, item); err != nil {
			return err
		}
	}
	if item.kind == entryDirectory && !item.prune && (f.maxDepth < 0 || item.depth < f.maxDepth) {
		for _, entry := range f.index[item.name] {
			childName := entry.name
			if item.name != "" {
				childName = item.name + "/" + entry.name
			}
			display := strings.TrimSuffix(item.display, "/") + "/" + entry.name
			if err := f.walk(ctx, &visit{display: display, name: childName, entry: entry.entry, depth: item.depth + 1}); err != nil {
				return err
			}
		}
	}
	if f.depthFirst {
		return f.evaluate(ctx, item)
	}
	return nil
}

func (f *finder) evaluate(ctx context.Context, item *visit) error {
	if item.depth < f.minDepth {
		return nil
	}
	_, err := f.expression(ctx, item)
	return err
}

// compile parses the expression; without an action it prints every match.
func (f *finder) compile(arguments []string) error {
	parser := &findParser{finder: f, arguments: arguments}
	expression, err := parser.expression()
	if err != nil {
		return err
	}
	if !parser.hasAction {
		matches := expression
		expression = func(ctx context.Context, item *visit) (bool, error) {
			matched, err := matches(ctx, item)
			if err != nil || !matched {
				return false, err
			}
			return true, f.print(item.display + "\n")
		}
	}
	f.expression = expression
	return nil
}

func (f *finder) print(text string) error {
	_, err := io.WriteString(f.invocation.Stdout, text)
	return err
}

// findParser reads a find expression by recursive descent: or of ands of
// optionally negated primaries.
type findParser struct {
	finder    *finder
	arguments []string
	hasAction bool
}

// expression parses the whole expression; an empty one is true.
func (p *findParser) expression() (predicate, error) {
	if len(p.arguments) == 0 {
		return func(context.Context, *visit) (bool, error) { return true, nil }, nil
	}
	parsed, err := p.or()
	switch {
	case err != nil:
		return nil, err
	case len(p.arguments) > 0 && p.arguments[0] == ")":
		return nil, errors.New("invalid expression; you have too many ')'")
	case len(p.arguments) > 0:
		return nil, fmt.Errorf("paths must precede expression: `%s'", p.arguments[0])
	}
	return parsed, nil
}

func (p *findParser) next() string {
	argument := p.arguments[0]
	p.arguments = p.arguments[1:]
	return argument
}

func (p *findParser) or() (predicate, error) {
	left, err := p.and()
	if err != nil {
		return nil, err
	}
	for len(p.arguments) > 0 && (p.arguments[0] == "-o" || p.arguments[0] == "-or") {
		p.next()
		right, err := p.and()
		if err != nil {
			return nil, err
		}
		left = orPredicate(left, right)
	}
	return left, nil
}

func (p *findParser) and() (predicate, error) {
	left, err := p.not()
	if err != nil {
		return nil, err
	}
	for len(p.arguments) > 0 && p.arguments[0] != "-o" && p.arguments[0] != "-or" && p.arguments[0] != ")" {
		if p.arguments[0] == "-a" || p.arguments[0] == "-and" {
			p.next()
		}
		right, err := p.not()
		if err != nil {
			return nil, err
		}
		left = andPredicate(left, right)
	}
	return left, nil
}

// orPredicate is true when left is, else when right is.
func orPredicate(left, right predicate) predicate {
	return func(ctx context.Context, item *visit) (bool, error) {
		matched, err := left(ctx, item)
		if err != nil || matched {
			return matched, err
		}
		return right(ctx, item)
	}
}

// andPredicate is true when both are; right runs only when left is true.
func andPredicate(left, right predicate) predicate {
	return func(ctx context.Context, item *visit) (bool, error) {
		matched, err := left(ctx, item)
		if err != nil || !matched {
			return false, err
		}
		return right(ctx, item)
	}
}

func (p *findParser) not() (predicate, error) {
	if len(p.arguments) == 0 {
		return nil, fmt.Errorf("invalid expression")
	}
	if p.arguments[0] == "!" || p.arguments[0] == "-not" {
		p.next()
		inner, err := p.not()
		if err != nil {
			return nil, err
		}
		return func(ctx context.Context, item *visit) (bool, error) {
			matched, err := inner(ctx, item)
			return !matched, err
		}, nil
	}
	return p.primary()
}

func (p *findParser) primary() (predicate, error) {
	switch argument := p.next(); argument {
	case "(":
		inner, err := p.or()
		if err != nil {
			return nil, err
		}
		if len(p.arguments) == 0 || p.next() != ")" {
			return nil, fmt.Errorf("invalid expression; I was expecting to find a ')' somewhere but did not see one")
		}
		return inner, nil
	case "-name", "-iname", "-path", "-ipath", "-wholename":
		return p.patternTest(argument)
	case "-type":
		return p.typeTest()
	case "-maxdepth", "-mindepth":
		return p.depthOption(argument)
	case "-empty":
		return p.finder.isEmpty, nil
	case "-prune":
		return func(_ context.Context, item *visit) (bool, error) { item.prune = true; return true, nil }, nil
	case "-true":
		return func(context.Context, *visit) (bool, error) { return true, nil }, nil
	case "-false":
		return func(context.Context, *visit) (bool, error) { return false, nil }, nil
	case "-print", "-print0":
		p.hasAction = true
		terminator := "\n"
		if argument == "-print0" {
			terminator = "\x00"
		}
		return func(_ context.Context, item *visit) (bool, error) {
			return true, p.finder.print(item.display + terminator)
		}, nil
	case "-delete":
		p.hasAction = true
		p.finder.depthFirst = true
		return p.finder.delete, nil
	case "-exec":
		p.hasAction = true
		return p.exec()
	default:
		return nil, fmt.Errorf("unknown predicate `%s'", argument)
	}
}

// value returns the argument of a primary such as -name.
func (p *findParser) value(primary string) (string, error) {
	if len(p.arguments) == 0 {
		return "", fmt.Errorf("missing argument to `%s'", primary)
	}
	return p.next(), nil
}

func (p *findParser) patternTest(primary string) (predicate, error) {
	pattern, err := p.value(primary)
	if err != nil {
		return nil, err
	}
	matcher, err := globRegexp(pattern, strings.HasPrefix(primary, "-i"))
	if err != nil {
		return nil, err
	}
	onName := primary == "-name" || primary == "-iname"
	return func(_ context.Context, item *visit) (bool, error) {
		subject := item.display
		if onName {
			subject = baseName(item.display)
		}
		return matcher.MatchString(subject), nil
	}, nil
}

func (p *findParser) typeTest() (predicate, error) {
	letter, err := p.value("-type")
	if err != nil {
		return nil, err
	}
	var want entryKind
	switch letter {
	case "f":
		want = entryFile
	case "d":
		want = entryDirectory
	case "c":
		want = entryDevice
	case "l", "b", "p", "s", "D":
		want = entryMissing // nothing in a workspace is a link, block device, pipe or socket
	default:
		return nil, &usageError{message: "Unknown argument to -type: " + letter}
	}
	return func(_ context.Context, item *visit) (bool, error) {
		return want != entryMissing && item.kind == want, nil
	}, nil
}

func (p *findParser) depthOption(primary string) (predicate, error) {
	text, err := p.value(primary)
	if err != nil {
		return nil, err
	}
	depth, err := strconv.Atoi(text)
	if err != nil || depth < 0 {
		return nil, &usageError{message: fmt.Sprintf("Expected a positive decimal integer argument to %s, but got `%s'", primary, text)}
	}
	if primary == "-maxdepth" {
		p.finder.maxDepth = depth
	} else {
		p.finder.minDepth = depth
	}
	return func(context.Context, *visit) (bool, error) { return true, nil }, nil
}

func (f *finder) isEmpty(_ context.Context, item *visit) (bool, error) {
	switch item.kind {
	case entryFile:
		return item.metadata.Size == 0, nil
	case entryDirectory:
		return len(f.index[item.name]) == 0, nil
	case entryMissing, entryDevice:
	}
	return false, nil
}

// delete removes a file, or a directory once its contents are gone (find
// visits it after them).
func (f *finder) delete(ctx context.Context, item *visit) (bool, error) {
	var err error
	switch {
	case item.name == "" || item.display == ".":
		return true, nil
	case item.kind == entryFile:
		err = describe(f.files.storage.DeleteFile(ctx, item.name))
	case item.kind == entryDirectory:
		listed, listErr := f.files.children(ctx, item.name)
		switch {
		case listErr != nil:
			return false, listErr
		case len(listed) > 0:
			err = errNotEmpty
		default:
			f.files.directories.removeTree(item.name)
		}
	}
	if err != nil {
		complain(f.invocation, "find", "cannot delete '%s': %v", item.display, err)
		f.failed = true
		return false, nil
	}
	return true, nil
}

// exec parses "-exec command ... ;" (run once per path, true on exit
// status 0) or "-exec command ... {} +" (paths collected and run in
// batches, always true).
func (p *findParser) exec() (predicate, error) {
	command := []string{}
	for len(p.arguments) > 0 {
		argument := p.next()
		if argument == ";" {
			return p.finder.execEach(command)
		}
		if argument == "+" && len(command) > 0 && command[len(command)-1] == "{}" {
			batch := &execBatch{invocation: p.finder.invocation, command: command[:len(command)-1]}
			p.finder.batches = append(p.finder.batches, batch)
			return batch.add, nil
		}
		command = append(command, argument)
	}
	return nil, fmt.Errorf("missing argument to `-exec'")
}

func (f *finder) execEach(command []string) (predicate, error) {
	if len(command) == 0 {
		return nil, fmt.Errorf("missing argument to `-exec'")
	}
	return func(ctx context.Context, item *visit) (bool, error) {
		arguments := make([]string, len(command))
		for index, argument := range command {
			arguments[index] = strings.ReplaceAll(argument, "{}", item.display)
		}
		status := runSubcommand(ctx, f.invocation, "find", arguments, f.invocation.Stdin)
		return status == 0, ctx.Err()
	}, nil
}

// maximumBatchArguments bounds one "-exec ... {} +" run, like the system's
// argument length limit does for GNU find.
const maximumBatchArguments = 4096

// execBatch collects the paths of one "-exec ... {} +".
type execBatch struct {
	invocation *Invocation
	command    []string
	paths      []string
	failed     bool
}

func (b *execBatch) add(ctx context.Context, item *visit) (bool, error) {
	b.paths = append(b.paths, item.display)
	if len(b.paths) >= maximumBatchArguments && !b.flush(ctx) {
		b.failed = true
	}
	return true, ctx.Err()
}

// flush runs the command on the collected paths and reports whether every
// run so far succeeded.
func (b *execBatch) flush(ctx context.Context) bool {
	if len(b.paths) > 0 {
		arguments := append(append([]string{}, b.command...), b.paths...)
		b.paths = nil
		if runSubcommand(ctx, b.invocation, "find", arguments, b.invocation.Stdin) != 0 {
			b.failed = true
		}
	}
	return !b.failed
}

// baseName is the last segment of a displayed path, "/" for the root.
func baseName(display string) string {
	trimmed := strings.TrimRight(display, "/")
	if trimmed == "" {
		return "/"
	}
	return trimmed[strings.LastIndex(trimmed, "/")+1:]
}

// globRegexp compiles a shell pattern (fnmatch without FNM_PATHNAME, so *
// also matches "/") to an anchored regular expression.
func globRegexp(pattern string, ignoreCase bool) (*regexp.Regexp, error) {
	var expression strings.Builder
	if ignoreCase {
		expression.WriteString("(?i)")
	}
	expression.WriteString("^(?s:")
	for index := 0; index < len(pattern); index++ {
		switch character := pattern[index]; character {
		case '*':
			expression.WriteString(".*")
		case '?':
			expression.WriteString(".")
		case '\\':
			if index+1 < len(pattern) {
				index++
				expression.WriteString(regexp.QuoteMeta(pattern[index : index+1]))
			} else {
				expression.WriteString(`\\`)
			}
		case '[':
			closing := bracketEnd(pattern, index)
			if closing < 0 {
				expression.WriteString(`\[`)
				continue
			}
			class := pattern[index+1 : closing]
			if strings.HasPrefix(class, "!") {
				class = "^" + class[1:]
			}
			expression.WriteString("[" + strings.ReplaceAll(class, `\`, `\\`) + "]")
			index = closing
		default:
			expression.WriteString(regexp.QuoteMeta(string(character)))
		}
	}
	expression.WriteString(")$")
	return regexp.Compile(expression.String())
}

// bracketEnd returns the index of the "]" closing the class that starts at
// open, or -1; a "]" right after "[" or "[!" belongs to the class.
func bracketEnd(pattern string, open int) int {
	index := open + 1
	if index < len(pattern) && (pattern[index] == '!' || pattern[index] == '^') {
		index++
	}
	if index < len(pattern) && pattern[index] == ']' {
		index++
	}
	for ; index < len(pattern); index++ {
		switch {
		case pattern[index] == ']':
			return index
		case pattern[index] == '[' && index+1 < len(pattern) && strings.IndexByte(":=.", pattern[index+1]) >= 0:
			// [:alpha:], [=a=] and [.a.] end with their own "]".
			if end := strings.Index(pattern[index+2:], pattern[index+1:index+2]+"]"); end >= 0 {
				index += end + 3
			}
		}
	}
	return -1
}
