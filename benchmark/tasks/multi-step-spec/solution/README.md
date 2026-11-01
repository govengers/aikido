# wordstat

Small word statistics for plain-text files.

## Commands

### count

```
wordstat count <file>
```

Prints the number of words in the file. A word is a run of letters and digits.

### top

```
wordstat top [-n N] <file>
```

Prints the N most frequent words (default 10) as `<word> <count>` lines, most frequent first; ties are sorted alphabetically.

## Development

`go test ./...`
