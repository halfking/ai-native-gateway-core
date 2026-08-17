package dispatch

import (
	"go/parser"
	"go/token"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

// TestDispatchProductionDoesNotImportRequestJourney guards the dispatch seam
// (v4 UT-CO-08): dispatch production files may not import the projection
// contract (requestjourney), the executor layer (executors) or the provider
// registry — dispatch stays a decoupled core with injected callbacks. The
// Redis mirror's go-redis dependency is allowed (observation bypass).
func TestDispatchProductionDoesNotImportRequestJourney(t *testing.T) {
	forbidden := map[string]bool{
		"github.com/kaixuan/llm-gateway-go/domains/requestjourney":      true,
		"github.com/kaixuan/llm-gateway-go/domains/streaming/executors": true,
		"github.com/kaixuan/llm-gateway-go/domains/provider":            true,
	}
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve current file")
	}
	files, err := filepath.Glob(filepath.Join(filepath.Dir(currentFile), "*.go"))
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range files {
		if strings.HasSuffix(filepath.Base(file), "_test.go") {
			continue
		}
		parsed, err := parser.ParseFile(token.NewFileSet(), file, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("parse %s: %v", file, err)
		}
		for _, spec := range parsed.Imports {
			path, err := strconv.Unquote(spec.Path.Value)
			if err != nil {
				t.Fatalf("unquote import in %s: %v", file, err)
			}
			if forbidden[path] {
				t.Fatalf("dispatch production file imports forbidden package %s: %s", path, file)
			}
		}
	}
}
