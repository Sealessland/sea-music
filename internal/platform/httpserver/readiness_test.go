package httpserver_test

import (
	"context"
	"errors"
	"testing"

	"github.com/sealessland/sea-music/internal/platform/httpserver"
)

func TestReadinessFailsRequiredDependencyButNotOptionalDependency(t *testing.T) {
	optionalFailure := errors.New("optional broker unavailable")
	dependencies := httpserver.Dependencies{
		Required: map[string]httpserver.ReadinessChecker{
			"database": httpserver.CheckFunc(func(context.Context) error { return nil }),
		},
		Optional: map[string]httpserver.ReadinessChecker{
			"broker": httpserver.CheckFunc(func(context.Context) error { return optionalFailure }),
		},
	}
	if err := dependencies.Check(context.Background()); err != nil {
		t.Fatalf("optional dependency failed readiness: %v", err)
	}
	if !errors.Is(dependencies.OptionalStatus(context.Background())["broker"], optionalFailure) {
		t.Fatal("optional dependency status did not retain failure")
	}
	dependencies.Required["database"] = httpserver.CheckFunc(func(context.Context) error { return errors.New("database unavailable") })
	if err := dependencies.Check(context.Background()); err == nil {
		t.Fatal("required dependency failure did not fail readiness")
	}
}
