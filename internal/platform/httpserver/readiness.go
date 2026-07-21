package httpserver

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"
)

type CheckFunc func(context.Context) error

// Check invokes the wrapped function with ctx and returns its error unchanged.
func (function CheckFunc) Check(ctx context.Context) error {
	return function(ctx)
}

type Dependencies struct {
	Required map[string]ReadinessChecker
	Optional map[string]ReadinessChecker
	Timeout  time.Duration
}

// Check runs all required readiness checks in sorted name order, optionally under a shared timeout, and joins failures annotated with their dependency names.
func (dependencies Dependencies) Check(ctx context.Context) error {
	if dependencies.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, dependencies.Timeout)
		defer cancel()
	}
	names := make([]string, 0, len(dependencies.Required))
	for name := range dependencies.Required {
		names = append(names, name)
	}
	sort.Strings(names)
	var result error
	for _, name := range names {
		if err := dependencies.Required[name].Check(ctx); err != nil {
			result = errors.Join(result, fmt.Errorf("%s: %w", name, err))
		}
	}
	return result
}

// OptionalStatus runs every optional readiness check with ctx and returns each result keyed by dependency name without applying the configured timeout.
func (dependencies Dependencies) OptionalStatus(ctx context.Context) map[string]error {
	result := make(map[string]error, len(dependencies.Optional))
	for name, checker := range dependencies.Optional {
		result[name] = checker.Check(ctx)
	}
	return result
}
