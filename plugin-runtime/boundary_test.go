package pluginruntime_test

import (
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// TestPluginsImportOnlyCore locks the magpie boundary for this repo:
// core never imports a plugins/* package (except plugins/all from cmd/gateway),
// and a plugin never imports another plugin.
func TestPluginsImportOnlyCore(t *testing.T) {
	root := filepath.Join("..")
	const mod = "github.com/kaixuan/llm-gateway-go/plugins/"
	pluginsRoot := filepath.Join(root, "plugins")
	if _, err := os.Stat(pluginsRoot); err != nil {
		t.Skip("plugins/ not present")
	}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			base := d.Name()
			if rel == "vendor" || rel == "web" || rel == "node_modules" || rel == ".git" ||
				(strings.HasPrefix(base, ".") && rel != ".") ||
				rel == "site" || rel == "build" || rel == ".worktrees" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		f, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
		if err != nil {
			return err
		}
		for _, im := range f.Imports {
			p, _ := strconv.Unquote(im.Path.Value)
			if !strings.HasPrefix(p, mod) {
				continue
			}
			switch {
			case strings.HasPrefix(rel, "cmd/gateway/") && p == mod+"all":
			case strings.HasPrefix(rel, "plugins/all/"):
			case strings.HasPrefix(rel, "plugins/"):
				t.Errorf("%s imports %s: a plugin never imports another", rel, p)
			default:
				t.Errorf("%s imports %s: core never imports a plugin", rel, p)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	dirs, err := os.ReadDir(pluginsRoot)
	if err != nil {
		t.Fatal(err)
	}
	allBytes, err := os.ReadFile(filepath.Join(pluginsRoot, "all", "all.go"))
	if err != nil {
		t.Fatal(err)
	}
	all := string(allBytes)
	for _, d := range dirs {
		if d.IsDir() && d.Name() != "all" && !strings.Contains(all, `"`+mod+d.Name()+`"`) {
			t.Errorf("plugins/all doesn't import plugins/%s", d.Name())
		}
	}
}
