# Spec: most frequent words

## Library

Add to package `wordstat` (new file `wordstat/top.go`):

```go
// WordCount is one word with its number of occurrences.
type WordCount struct {
	Word  string
	Count int
}

func TopWords(text string, n int) []WordCount
```

- Words are exactly those returned by the existing `Words` function (lower-cased).
- The result is ordered by `Count` descending; words with the same count are ordered alphabetically (byte order) ascending.
- At most `n` entries are returned; if there are fewer distinct words, all of them are returned.
- `n <= 0` or a text without words returns an empty, non-nil slice.

## Command line

Add a subcommand `top` to `cmd/wordstat`:

```
wordstat top [-n N] <file>
```

- `-n` defaults to `10`.
- Prints one line per entry of `TopWords`, in order, formatted as `<word> <count>` (single space), and nothing else.
- The existing `count` subcommand keeps working unchanged. Update the usage message to mention both subcommands.

## Documentation

In `README.md`, add a section `### top` under `## Commands`, after the `### count` section, that shows the usage line `wordstat top [-n N] <file>` in a code block (like `count` does) and explains in one or two sentences what it prints.
