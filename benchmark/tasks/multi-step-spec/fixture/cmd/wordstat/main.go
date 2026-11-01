// Command wordstat prints statistics about a text file.
package main

import (
	"fmt"
	"os"

	"example.com/wordstat/wordstat"
)

func usage() {
	fmt.Fprintln(os.Stderr, "usage: wordstat count <file>")
	os.Exit(2)
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
		text, err := os.ReadFile(os.Args[2])
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		fmt.Println(wordstat.Count(string(text)))
	default:
		usage()
	}
}
