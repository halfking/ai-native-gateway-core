package dbinit

import (
	"bufio"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// updateManifest is the flag the guard's own failure message tells you to pass.
//
// It has to be a REGISTERED flag, not just a substring looked for in os.Args.
// The testing package calls flag.Parse before any test body runs, and flag.Parse
// exits on an undefined flag — so a bare os.Args scan could never see a value
// that the binary had already refused to start with. Verified before this was
// added: `go test -run TestStartupManifest -update` and the `-args -update`
// form both died with "flag provided but not defined: -update", which is the
// exact command the guard printed on drift. A guard whose recovery command
// cannot run is worse than no recovery command, because it sends the reader
// down a path that dead-ends.
var updateManifest = flag.Bool("update", false,
	"regenerate the startup manifest from StartupFiles and exit")

// startupManifestPath is the derived, test-readable copy of StartupFiles.
//
// It is checked in (not generated on the fly) so that a test in ANOTHER Go module
// can replay "the state just before migration N" without being able to change what
// the installer runs. sql/migrations/startup/ holds 458 migration numbers but only
// 198 are registered, and the registered order is not numeric order — the
// session_turns_hot bootstrap sits at index 3, 715 at index 149, and 704 comes after
// 713 — so "apply everything numbered below N" is not a correct reconstruction.
const startupManifestPath = "../../../sql/schema/installed_startup_migrations.tsv"

// regenerate writes the manifest from the live StartupFiles list.
//
// The guard below is the thing that matters; this only exists so the fix is one
// command rather than a hand edit. It is NOT run by `go test`.
func regenerate() error {
	f, err := os.Create(startupManifestPath)
	if err != nil {
		return err
	}
	defer f.Close()
	w := bufio.NewWriter(f)
	fmt.Fprint(w, manifestHeader)
	for i, name := range runnerStartupFiles() {
		fmt.Fprintf(w, "%d\t%s\n", i, name)
	}
	return w.Flush()
}

const manifestHeader = `# installed_startup_migrations.tsv — the installer's ORDERED startup migration list.
#
# Generated from installer/internal/dbinit/runner.go : Runner.StartupFiles.
# DO NOT EDIT BY HAND. installer/internal/dbinit/startup_manifest_test.go compares
# this file against StartupFiles entry by entry and fails on any difference, so a
# hand edit here is a test failure, not a silent divergence.
#
# Why it exists: sql/migrations/startup/ holds 458 migration numbers but only 198
# are registered, and the registered ORDER is not numeric order (bootstrap sits at
# index 3, 715 at index 149, 704 after 713). A test that needs to replay "the state
# just before migration N" cannot work that out from filenames.
#
# Why it is a separate artifact rather than the installer reading this file: the
# installer is production wiring, and a test-only reader must never be able to change
# what the installer runs. This file is derived; StartupFiles is the source of truth.
#
# Columns: <apply-order index, 0-based>	<file name under embeddata/startup/>
`

// runnerStartupFiles builds a Runner purely to read its StartupFiles. NewRunner only
// fills struct fields — it opens no connection and touches no disk — so the empty
// arguments here are safe and the point is that the list comes from ONE place.
func runnerStartupFiles() []string {
	return NewRunner("unused-citushost", "unused-user", "unused-db", "unused-sqldir").StartupFiles
}

func readManifest(t *testing.T) []string {
	t.Helper()
	f, err := os.Open(startupManifestPath)
	if err != nil {
		t.Fatalf("open %s: %v\n"+
			"This file is derived from StartupFiles and is checked in. If it is missing, "+
			"regenerate it with:\n"+
			"    cd installer && go test ./internal/dbinit/ -run TestStartupManifest -update",
			startupManifestPath, err)
	}
	defer f.Close()

	var out []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, "#") || strings.TrimSpace(line) == "" {
			continue
		}
		idx, name, ok := strings.Cut(line, "\t")
		if !ok {
			t.Fatalf("manifest line %q has no tab separator", line)
		}
		i, err := strconv.Atoi(idx)
		if err != nil {
			t.Fatalf("manifest line %q: index %q is not a number", line, idx)
		}
		if i != len(out) {
			t.Fatalf("manifest index %d is out of order (expected %d) — the file is an ordered list, "+
				"not a set; regenerate it", i, len(out))
		}
		out = append(out, name)
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("read %s: %v", startupManifestPath, err)
	}
	return out
}

// TestStartupManifestMatchesStartupFiles is the whole point of the file: a stale
// copy is worse than no copy, because a test would replay the wrong migration
// sequence and report a confident, wrong result.
func TestStartupManifestMatchesStartupFiles(t *testing.T) {
	if regenerateRequested() {
		if err := regenerate(); err != nil {
			t.Fatalf("regenerate: %v", err)
		}
		t.Logf("regenerated %s", startupManifestPath)
		return
	}

	want := runnerStartupFiles()
	got := readManifest(t)

	if len(got) != len(want) {
		t.Fatalf("manifest has %d entries, StartupFiles has %d.\n"+
			"Regenerate with:\n    cd installer && go test ./internal/dbinit/ -run TestStartupManifest -update",
			len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("entry %d: manifest=%q StartupFiles=%q\n"+
				"Regenerate with:\n    cd installer && go test ./internal/dbinit/ -run TestStartupManifest -update",
				i, got[i], want[i])
		}
	}
}

// TestStartupManifestFilesExist checks the other half of the contract: every name in
// the manifest must resolve to a real file under embeddata/startup/. A name that
// StartupFiles carries but no file backs would be skipped silently at install time.
func TestStartupManifestFilesExist(t *testing.T) {
	const embedDir = "../../cmd/llm-gw-installer/embeddata/startup"
	for i, name := range readManifest(t) {
		path := filepath.Join(embedDir, name)
		if _, err := os.Stat(path); err != nil {
			t.Errorf("entry %d: %s is in StartupFiles but has no file at %s: %v", i, name, path, err)
		}
	}
}

func regenerateRequested() bool {
	return *updateManifest
}
