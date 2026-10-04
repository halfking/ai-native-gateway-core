package sanitize

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Audit 245 (R89-FI) — the production rule set is selected by the deployment
// layout, and every layout we ship says "use the built-in fallback".
//
// cmd/gateway/goal_control.go:517-526 constructs the gateway's only detector
// like this:
//
//	const configPath = "configs/sensitive_patterns.yaml"   // RELATIVE
//	detector, err := sanitize.NewPatternDetectorFromFile(configPath)
//	if err != nil {
//	    slog.Warn("sanitize pattern config unavailable; using built-in fallback", ...)
//	    return sanitize.NewPatternDetector()
//	}
//
// Two consequences, both load-bearing:
//
//  1. RELATIVE means the rule set is a function of the process working
//     directory. A deployment that puts the binary in one directory and the
//     config in another does not fail — it silently runs a different rule set.
//  2. The two rule sets are not equivalent. The built-in set catches `ak-…`
//     and the yaml set does not; the yaml set catches IBAN and the built-in
//     set does not. Audit 244's table pinned to one of them described a system
//     that may not be the one running.
//
// R42: this test now BITES on state changes. It pins the audited current
// state ("all containerized paths fall back to the built-in rule set") and
// fails loudly if that state changes — because a silent fallback is
// indistinguishable from a working one at runtime, the only safe direction
// for a gate is "conclusion expired -> revisit decision 107". While 107 is
// pending, MISSING/UNREACHABLE are the expected (green) readings.
func Test245DeploymentShipsThePatternConfig(t *testing.T) {
	root := repoRoot(t)

	t.Log("--- Dockerfile runtime stage: does any COPY put configs/ into the image? ---")
	runtime := dockerfileRuntimeStage(t, filepath.Join(root, "Dockerfile"))
	if runtime == "" {
		t.Log("  (could not locate a runtime stage; nothing asserted)")
	} else if ok, how := runtimeCopiesConfigs(runtime); ok {
		// R42 加牙：审计 245 的结论是「容器化路径全部落到 fallback」。这
		// 个状态一旦被打破（R-E1 类部署修复落地），结论与挂起的 107 号
		// 裁决前提同时过期——必须红下来要求重审，而不是静默变绿。
		t.Fatalf("audit 245 conclusion expired: the Dockerfile runtime stage now ships "+
			"configs/ (%s) — the 「all containerized paths fall back to the built-in "+
			"rule set」 claim no longer holds; revisit pending decision 107 and flip "+
			"this gate together with the fix", how)
	} else {
		t.Log("  MISSING (as audited, decision 107 pending): the runtime stage never copies "+
			"configs/ — the image ships no configs/sensitive_patterns.yaml, so "+
			"newSanitizePatternDetector() takes its fallback branch for the whole life "+
			"of the container")
	}

	t.Log("--- compose files: is configs/ mounted where the relative path can reach it? ---")
	for _, name := range composeFiles(t, root) {
		body, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		text := string(body)
		if !strings.Contains(text, "configs") {
			t.Logf("  %-38s no configs reference", name)
			continue
		}
		if ok, detail := composeMountReachesRelativePath(text); ok {
			// R42 加牙：同上——挂载一旦变得可达，245 号结论过期，需重审 107。
			t.Fatalf("audit 245 conclusion expired: %s now mounts configs/ where the "+
				"relative path reaches it (%s) — the fallback claim no longer holds for "+
				"this layout; revisit pending decision 107 and flip this gate", name, detail)
		} else {
			t.Logf("  %-38s UNREACHABLE (as audited, decision 107 pending): %s", name, detail)
		}
	}
}

// Test245FallbackDetectorCannotHotReload pins the other half of the same
// finding, and it is executable rather than inferred.
//
// newSanitizePatternDetector()'s fallback branch calls NewPatternDetector(),
// which never sets configPath. ReloadFromFile() therefore has nothing to
// reload and returns an error — the admin endpoint
// /api/admin/sensitive-words/reload (admin/sensitive_words_handler.go:46-57)
// can only ever answer 500 on that path.
//
// This test asserts the CURRENT behaviour on purpose: flipping it to a
// failure is the fix, and that is pending decision 107. What it guards is the
// claim in audit 110 ("规则集在生产确实可热更"), which held only for the file
// path and was written without checking the fallback.
func Test245FallbackDetectorCannotHotReload(t *testing.T) {
	fallback := NewPatternDetector()
	if err := fallback.ReloadFromFile(); err == nil {
		t.Fatal("premise broken: a built-in fallback detector is expected NOT to be reloadable; " +
			"if it now is, it gained a config path and pending decision 107 needs revisiting")
	} else {
		t.Logf("built-in fallback detector: ReloadFromFile -> %v", err)
		t.Log("  => /api/admin/sensitive-words/reload can only answer 500 on this path, and " +
			"editing the yaml file has no effect at all (it is never read)")
	}
}

func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for i := 0; i < 6; i++ {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		dir = filepath.Dir(dir)
	}
	t.Fatal("could not locate repo root (no go.mod within 6 levels up)")
	return ""
}

// dockerfileRuntimeStage returns the lines after the LAST `FROM`, which is
// the stage that actually ships. Copying configs/ into the builder stage is
// not enough: builder artefacts are not in the final image.
func dockerfileRuntimeStage(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Logf("read %s: %v", path, err)
		return ""
	}
	lines := strings.Split(string(data), "\n")
	lastFrom := -1
	for i, l := range lines {
		if strings.HasPrefix(strings.ToUpper(strings.TrimSpace(l)), "FROM ") {
			lastFrom = i
		}
	}
	if lastFrom < 0 {
		return ""
	}
	return strings.Join(lines[lastFrom:], "\n")
}

func runtimeCopiesConfigs(runtime string) (bool, string) {
	for _, raw := range strings.Split(runtime, "\n") {
		l := strings.TrimSpace(raw)
		if u := strings.ToUpper(l); !strings.HasPrefix(u, "COPY ") {
			continue
		}
		fields := strings.Fields(l)
		// Drop COPY itself, every --flag, and — crucially — the LAST token,
		// which is the destination. `COPY --from=builder /app/bin/llm-gateway .`
		// copies ONE file; the trailing "." is the destination directory, not
		// a source. Treating it as a source makes the whole check pass on
		// exactly the line it exists to catch.
		var args []string
		for i, tok := range fields {
			if i == 0 || strings.HasPrefix(tok, "--") {
				continue
			}
			args = append(args, tok)
		}
		if len(args) < 2 {
			continue // no source or no destination: not a real COPY
		}
		sources := args[:len(args)-1]
		for _, src := range sources {
			clean := strings.TrimSuffix(src, "/")
			if clean == "configs" || clean == "." {
				return true, l + "  (source list: " + strings.Join(sources, " ") + ")"
			}
		}
	}
	return false, ""
}

func composeFiles(t *testing.T, root string) []string {
	t.Helper()
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("readdir %s: %v", root, err)
	}
	var out []string
	for _, e := range entries {
		n := e.Name()
		if e.IsDir() || !strings.HasPrefix(n, "docker-compose") {
			continue
		}
		if strings.HasSuffix(n, ".yml") || strings.HasSuffix(n, ".yaml") {
			out = append(out, n)
		}
	}
	return out
}

// composeMountReachesRelativePath checks the thing that actually decides
// whether the mount helps: where the volume lands, relative to the image's
// WORKDIR. Mounting ./configs at /configs does nothing for a process whose
// working directory is /app, because the code asks for "configs/…", which
// resolves to /app/configs/….
func composeMountReachesRelativePath(text string) (bool, string) {
	var evidence []string
	for _, raw := range strings.Split(text, "\n") {
		l := strings.Trim(strings.TrimSpace(raw), "- ")
		if !strings.HasPrefix(l, "./configs:") && !strings.HasPrefix(l, "configs:") {
			continue
		}
		parts := strings.Split(l, ":")
		if len(parts) < 2 {
			continue
		}
		target := strings.TrimSpace(parts[1])
		evidence = append(evidence, target)
		if target == "/app/configs" || target == "/app/configs/" {
			return true, "mounted at " + target + " (matches WORKDIR /app)"
		}
	}
	if len(evidence) == 0 {
		return true, "configs reference present but not a volume mount"
	}
	return false, "mounted at " + strings.Join(evidence, ", ") +
		" but the image WORKDIR is /app, so the relative path resolves to /app/configs/…"
}
