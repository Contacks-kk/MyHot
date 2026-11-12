package httpapi

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The HTTP package adapts requests to domain services. server_new.go is the
// composition root; request handlers must not acquire database dependencies.
func TestHandlersDoNotDependOnDatabaseImplementations(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") || name == "server_new.go" {
			continue
		}
		file, err := parser.ParseFile(token.NewFileSet(), filepath.Clean(name), nil, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("parse imports in %s: %v", name, err)
		}
		for _, spec := range file.Imports {
			importPath := strings.Trim(spec.Path.Value, "\"")
			if isDatabaseImplementation(importPath) {
				t.Errorf("%s imports database implementation %q; handlers must call a domain service", name, importPath)
			}
		}
	}
}

func isDatabaseImplementation(importPath string) bool {
	return importPath == "database/sql" || importPath == "gorm.io/gorm" ||
		strings.HasPrefix(importPath, "gorm.io/gorm/") ||
		strings.HasPrefix(importPath, "github.com/jackc/pgx/") ||
		strings.HasPrefix(importPath, "github.com/jackc/pgx-") ||
		strings.HasPrefix(importPath, "aihot/backend-go/internal/model") ||
		strings.HasPrefix(importPath, "aihot/backend-go/internal/db")
}
