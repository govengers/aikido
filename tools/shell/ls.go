package shell

import (
	"cmp"
	"context"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"
)

// errSeriousTrouble is ls's exit status for an operand it cannot access.
const errSeriousTrouble ExitError = 2

func lsCommand() Command {
	return Command{
		Name:    "ls",
		Summary: "list directories, one entry per line (-1 -a -A -l -h -R -d -t -S -r -F -p)",
		Run:     runLs,
	}
}

// listing is one ls invocation: its flags and where it prints.
type listing struct {
	invocation *Invocation
	files      workspace
	all        bool // -a: also . and .. and hidden names
	almostAll  bool // -A: hidden names
	long       bool
	human      bool
	recursive  bool
	itself     bool // -d
	byTime     bool
	bySize     bool
	reverse    bool
	classify   bool // -F, -p
	now        time.Time
}

// listItem is one name ls prints with what it names.
type listItem struct {
	display string
	entry
}

func runLs(ctx context.Context, invocation *Invocation) error {
	parsed, operands, err := optionSet{flags: "1aAlhRdtSrFp", long: map[string]longOption{
		"all": {name: "a"}, "almost-all": {name: "A"}, "human-readable": {name: "h"},
		"recursive": {name: "R"}, "directory": {name: "d"}, "reverse": {name: "r"}, "classify": {name: "F"},
	}}.parse(invocation.Arguments)
	if err != nil {
		return usage(invocation, "ls", err, errSeriousTrouble)
	}
	list := listing{
		invocation: invocation, files: workspaceOf(invocation),
		all: parsed.has("a"), almostAll: parsed.has("A"), long: parsed.has("l"), human: parsed.has("h"),
		recursive: parsed.has("R"), itself: parsed.has("d"), byTime: parsed.has("t"), bySize: parsed.has("S"),
		reverse: parsed.has("r"), classify: parsed.has("F") || parsed.has("p"), now: time.Now().UTC(),
	}
	if len(operands) == 0 {
		operands = []string{"."}
	}
	return list.run(ctx, operands)
}

func (l listing) run(ctx context.Context, operands []string) error {
	var status error
	plain, directories := []listItem{}, []listItem{}
	for _, operand := range operands {
		name, err := resolve(l.invocation, operand)
		found := entry{}
		if err == nil {
			found, err = l.files.stat(ctx, name)
		}
		switch {
		case err != nil || found.kind == entryMissing:
			complain(l.invocation, "ls", "cannot access '%s': %v", operand, errNotFound)
			status = errSeriousTrouble
		case strings.HasSuffix(operand, "/") && found.kind != entryDirectory:
			complain(l.invocation, "ls", "cannot access '%s': %v", operand, errNotDirectory)
			status = errSeriousTrouble
		case found.kind == entryDirectory && !l.itself:
			directories = append(directories, listItem{display: operand, entry: found})
		default:
			plain = append(plain, listItem{display: operand, entry: found})
		}
	}
	l.sort(plain)
	l.sort(directories)
	if err := l.print(plain); err != nil {
		return err
	}
	headers := len(operands) > 1 || l.recursive
	for index, directory := range directories {
		if index > 0 || len(plain) > 0 {
			fmt.Fprintln(l.invocation.Stdout)
		}
		if err := l.listDirectory(ctx, directory.display, headers); err != nil {
			return err
		}
	}
	return status
}

// listDirectory prints the contents of one directory operand, and with -R
// those of its subdirectories.
func (l listing) listDirectory(ctx context.Context, operand string, header bool) error {
	items, err := l.entries(ctx, operand)
	if err != nil {
		return err
	}
	if header {
		fmt.Fprintf(l.invocation.Stdout, "%s:\n", operand)
	}
	if l.long {
		total := strconv.FormatInt(totalBlocks(items), 10)
		if l.human {
			total = humanSize(totalBlocks(items) * 1024)
		}
		fmt.Fprintf(l.invocation.Stdout, "total %s\n", total)
	}
	if err := l.print(items); err != nil {
		return err
	}
	if !l.recursive {
		return nil
	}
	for _, item := range items {
		if item.kind != entryDirectory || item.display == "." || item.display == ".." {
			continue
		}
		fmt.Fprintln(l.invocation.Stdout)
		if err := l.listDirectory(ctx, strings.TrimSuffix(operand, "/")+"/"+item.display, true); err != nil {
			return err
		}
	}
	return nil
}

// entries returns what ls shows of a directory: hidden names only with -a
// or -A, "." and ".." with -a, sorted.
func (l listing) entries(ctx context.Context, operand string) ([]listItem, error) {
	name, err := resolve(l.invocation, operand)
	if err != nil {
		return nil, err
	}
	listed, err := l.files.children(ctx, name)
	if err != nil {
		return nil, err
	}
	items := []listItem{}
	if l.all {
		items = append(items, listItem{display: ".", entry: entry{kind: entryDirectory}}, listItem{display: "..", entry: entry{kind: entryDirectory}})
	}
	for _, item := range listed {
		if !strings.HasPrefix(item.name, ".") || l.all || l.almostAll {
			items = append(items, listItem{display: item.name, entry: item.entry})
		}
	}
	l.sort(items)
	return items, nil
}

// sort orders items by name (byte order, the C locale), or with -t by time
// and -S by size, largest first; -r reverses.
func (l listing) sort(items []listItem) {
	slices.SortStableFunc(items, func(left, right listItem) int {
		order := 0
		switch {
		case l.bySize:
			order = cmp.Compare(right.metadata.Size, left.metadata.Size)
		case l.byTime:
			order = right.metadata.UpdatedAt.Compare(left.metadata.UpdatedAt)
		}
		if order == 0 {
			order = strings.Compare(left.display, right.display)
		}
		return order
	})
	if l.reverse {
		slices.Reverse(items)
	}
}

func (l listing) print(items []listItem) error {
	if !l.long {
		for _, item := range items {
			if _, err := fmt.Fprintln(l.invocation.Stdout, item.display+l.suffix(item)); err != nil {
				return err
			}
		}
		return nil
	}
	width := 0
	for _, item := range items {
		width = max(width, len(l.size(sizeOf(item))))
	}
	for _, item := range items {
		mode, links := "-rw-r--r--", 1
		switch item.kind {
		case entryDirectory:
			mode, links = "drwxr-xr-x", 2
		case entryDevice:
			mode = "crw-rw-rw-"
		case entryMissing, entryFile:
		}
		line := fmt.Sprintf("%s %d user user %*s %s %s%s", mode, links, width, l.size(sizeOf(item)), l.timestamp(item.metadata.UpdatedAt), item.display, l.suffix(item))
		if _, err := fmt.Fprintln(l.invocation.Stdout, line); err != nil {
			return err
		}
	}
	return nil
}

func (l listing) suffix(item listItem) string {
	if l.classify && item.kind == entryDirectory {
		return "/"
	}
	return ""
}

// size formats a byte count, with -h the way GNU does (1.5K, 23M).
func (l listing) size(bytes int64) string {
	if !l.human {
		return strconv.FormatInt(bytes, 10)
	}
	return humanSize(bytes)
}

// timestamp is ls -l's date column: time of day within six months, else the
// year.
func (l listing) timestamp(updated time.Time) string {
	if updated.IsZero() {
		// A directory without files below it ("." of an empty mkdir) has
		// no time of its own; it shows the current one.
		updated = l.now
	}
	updated = updated.UTC()
	if updated.After(l.now.AddDate(0, -6, 0)) && !updated.After(l.now) {
		return updated.Format("Jan _2 15:04")
	}
	return updated.Format("Jan _2  2006")
}

func sizeOf(item listItem) int64 {
	if item.kind == entryDirectory {
		return 4096
	}
	return item.metadata.Size
}

// totalBlocks is ls -l's "total": 1 KiB blocks on a file system with 4 KiB
// blocks.
func totalBlocks(items []listItem) int64 {
	blocks := int64(0)
	for _, item := range items {
		blocks += (sizeOf(item) + 4095) / 4096 * 4
	}
	return blocks
}

// humanSize formats bytes like GNU's -h: powers of 1024, rounded up, one
// decimal below 10.
func humanSize(bytes int64) string {
	const units = "KMGTPE"
	value := float64(bytes)
	unit := -1
	for value >= 1024 && unit < len(units)-1 {
		value /= 1024
		unit++
	}
	if unit < 0 {
		return strconv.FormatInt(bytes, 10)
	}
	if tenths := math.Ceil(value*10) / 10; tenths < 10 {
		return fmt.Sprintf("%.1f%c", tenths, units[unit])
	}
	return fmt.Sprintf("%.0f%c", math.Ceil(value), units[unit])
}
