// Command wordstat prints statistics about a text file.
package main

import (
	"flag"
	"fmt"
	"os"

	"example.com/wordstat/wordstat"
)

func usage() {
	fmt.Fprintln(os.Stderr, "usage: wordstat count <file>\n       wordstat top [-n N] <file>")
	os.Exit(2)
}

func read(path string) string {
	text, err := os.ReadFile(path)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	return string(text)
}

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	switch os.Args[1] {
	case "count":
		if len(os.Args) != 3 {
			usage()
		}
		fmt.Println(wordstat.Count(read(os.Args[2])))
	case "top":
		fs := flag.NewFlagSet("top", flag.ExitOnError)
		n := fs.Int("n", 10, "number of words")
		_ = fs.Parse(os.Args[2:])
		if fs.NArg() != 1 {
			usage()
		}
		for _, wc := range wordstat.TopWords(read(fs.Arg(0)), *n) {
			fmt.Printf("%s %d\n", wc.Word, wc.Count)
		}
	default:
		usage()
	}
}
