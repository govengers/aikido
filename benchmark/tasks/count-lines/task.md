Create a file `LINES.md` at the repository root that lists the line count of every file under `pkg/` whose name ends in `.go` (test files included, files in all subdirectories included, nothing outside `pkg/`).

Format, one entry per line, sorted by path in byte order (as `LC_ALL=C sort` does), followed by a final total line, and nothing else:

```
pkg/some/file.go: 42
pkg/some/other.go: 7
total: 49
```

Paths are relative to the repository root. The line count of a file is its number of lines; every file ends with a newline. Do not change any other file.
