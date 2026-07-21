package main

import (
	"fmt"
	"os"
)

// main runs the API command, printing any startup error to standard error and exiting with status 1.
func main() {
	if err := run(); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "api: %v\n", err)
		os.Exit(1)
	}
}
