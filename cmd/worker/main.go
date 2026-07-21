package main

import (
	"fmt"
	"os"
)

// main runs the worker; if run returns an error, it prints the error to standard error and exits with status 1.
func main() {
	if err := run(); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "worker: %v\n", err)
		os.Exit(1)
	}
}
