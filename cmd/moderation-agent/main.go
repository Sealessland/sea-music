package main

import (
	"fmt"
	"os"
)

// main runs the moderation agent, reporting startup errors to standard error and exiting with status 1 on failure.
func main() {
	if err := run(); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "moderation-agent: %v\n", err)
		os.Exit(1)
	}
}
