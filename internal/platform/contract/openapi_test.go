package contract_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOpenAPIBaselineIsStructurallyValid(t *testing.T) {
	path := filepath.Join(repositoryRoot(t), "api", "openapi.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read OpenAPI document: %v", err)
	}
	var document map[string]any
	if err := json.Unmarshal(data, &document); err != nil {
		t.Fatalf("parse OpenAPI document: %v", err)
	}
	version, _ := document["openapi"].(string)
	if !strings.HasPrefix(version, "3.1.") {
		t.Fatalf("openapi = %q, want 3.1.x", version)
	}

	paths := object(t, document, "paths")
	for _, pathName := range []string{"/livez", "/readyz"} {
		pathItem := object(t, paths, pathName)
		operation := object(t, pathItem, "get")
		if len(object(t, operation, "responses")) == 0 {
			t.Errorf("GET %s has no responses", pathName)
		}
	}

	components := object(t, document, "components")
	schemas := object(t, components, "schemas")
	for _, schema := range []string{"ErrorResponse", "CursorPageMeta", "HealthResponse"} {
		if _, ok := schemas[schema]; !ok {
			t.Errorf("components.schemas.%s is missing", schema)
		}
	}
	assertReferencesResolve(t, document, document)
}

func assertReferencesResolve(t *testing.T, value any, document map[string]any) {
	t.Helper()
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			if key == "$ref" {
				reference, ok := child.(string)
				if !ok || !strings.HasPrefix(reference, "#/") {
					t.Errorf("unsupported reference %v", child)
					continue
				}
				if !referenceExists(document, reference) {
					t.Errorf("reference %q does not resolve", reference)
				}
				continue
			}
			assertReferencesResolve(t, child, document)
		}
	case []any:
		for _, child := range typed {
			assertReferencesResolve(t, child, document)
		}
	}
}

func referenceExists(document map[string]any, reference string) bool {
	var current any = document
	for _, segment := range strings.Split(strings.TrimPrefix(reference, "#/"), "/") {
		object, ok := current.(map[string]any)
		if !ok {
			return false
		}
		current, ok = object[segment]
		if !ok {
			return false
		}
	}
	return true
}

func object(t *testing.T, parent map[string]any, key string) map[string]any {
	t.Helper()
	value, ok := parent[key].(map[string]any)
	if !ok {
		t.Fatalf("%s is missing or is not an object", key)
	}
	return value
}

func repositoryRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatalf("resolve repository root: %v", err)
	}
	return root
}
