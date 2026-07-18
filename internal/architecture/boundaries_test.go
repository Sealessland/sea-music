package architecture_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

const modulePath = "github.com/sealessland/sea-music"

var domainModules = []string{
	"identity",
	"video",
	"social",
	"discovery",
	"events",
	"platform",
}

func TestDomainModulesExist(t *testing.T) {
	root := repositoryRoot(t)

	for _, domain := range domainModules {
		domain := domain
		t.Run(domain, func(t *testing.T) {
			info, err := os.Stat(filepath.Join(root, "internal", domain))
			if err != nil {
				t.Fatalf("domain module %q must exist: %v", domain, err)
			}
			if !info.IsDir() {
				t.Fatalf("domain module %q is not a directory", domain)
			}
		})
	}
}

func TestDomainModulesDoNotImportEachOther(t *testing.T) {
	root := repositoryRoot(t)
	fset := token.NewFileSet()

	for _, owner := range domainModules {
		ownerRoot := filepath.Join(root, "internal", owner)
		err := filepath.WalkDir(ownerRoot, func(path string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}

			file, err := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
			if err != nil {
				return err
			}
			for _, spec := range file.Imports {
				importPath, err := strconv.Unquote(spec.Path.Value)
				if err != nil {
					return err
				}
				assertAllowedDomainImport(t, fset, owner, importPath, spec)
			}
			return nil
		})
		if err != nil {
			t.Fatalf("inspect domain %q: %v", owner, err)
		}
	}
}

func TestInnerLayersDoNotImportHTTPAdapter(t *testing.T) {
	root := repositoryRoot(t)
	fset := token.NewFileSet()
	for _, owner := range domainModules {
		owner := owner
		err := filepath.WalkDir(filepath.Join(root, "internal", owner), func(path string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			file, err := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
			if err != nil {
				return err
			}
			for _, spec := range file.Imports {
				importPath, err := strconv.Unquote(spec.Path.Value)
				if err != nil {
					return err
				}
				if importPath == modulePath+"/internal/appapi" {
					t.Errorf("%s: inner package %q must not import the HTTP adapter", fset.Position(spec.Pos()), owner)
				}
			}
			return nil
		})
		if err != nil {
			t.Fatalf("inspect inner package %q: %v", owner, err)
		}
	}
}

func assertAllowedDomainImport(t *testing.T, fset *token.FileSet, owner, importPath string, spec *ast.ImportSpec) {
	t.Helper()
	internalPrefix := modulePath + "/internal/"
	if !strings.HasPrefix(importPath, internalPrefix) {
		return
	}

	imported := strings.Split(strings.TrimPrefix(importPath, internalPrefix), "/")[0]
	if imported == owner {
		return
	}

	for _, domain := range domainModules {
		if imported == domain {
			t.Errorf("%s: domain %q must not import domain %q directly; collaborate through an application contract or event", fset.Position(spec.Pos()), owner, imported)
		}
	}
}

func repositoryRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatalf("resolve repository root: %v", err)
	}
	return root
}
