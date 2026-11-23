// Command copyfile copies one file to another, for a go:generate line that
// has to run on every system, Windows included, where there is no cp.
//
//	go run ./internal/cmd/copyfile <from> <to>
package main

import (
	"fmt"
	"os"
)

func main() {
	if len(os.Args) != 3 {
		fmt.Fprintln(os.Stderr, "usage: copyfile <from> <to>")
		os.Exit(2)
	}
	data, err := os.ReadFile(os.Args[1])
	if err == nil {
		err = os.WriteFile(os.Args[2], data, 0o644) //nolint:gosec // a source file in the checkout, readable like the rest of it
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "copyfile:", err)
		os.Exit(1)
	}
}
