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

func TestDispatchProductionDoesNotImportRequestJourney(t *testing.T) {
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
			if path == "github.com/kaixuan/llm-gateway-go/domains/requestjourney" {
				t.Fatalf("dispatch production file imports requestjourney: %s", file)
			}
		}
	}
}
