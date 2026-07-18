package main

import (
	"fmt"
	"os"
)

func main() {
	if err := run(); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "api: %v\n", err)
		os.Exit(1)
	}
}
