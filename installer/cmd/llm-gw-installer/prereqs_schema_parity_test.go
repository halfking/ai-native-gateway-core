//go:build !integration

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestCanonicalParityForEmbeddedPrereqsSchema pins the three copies of
// 00-prereqs.sql to each other.
//
// R43 (2026-09-30). The installer's embeddata/00-prereqs.sql had drifted: it
// carried only the four pre-2026-08-04 extensions, while sql/schema/00-prereqs.sql
// and deploy/sql/schemas/baseline/00-prereqs.sql both carried nine. Because
// setupSQLDir() builds the installer work directory from the embedded copies,
// a fresh install shipped by the installer never ran
// `CREATE EXTENSION citus_columnar`, the `columnar` access method was never
// registered, and 01-schema.sql aborted at its first
// `SET default_table_access_method = 'columnar'` with
//
//	ERROR:  invalid value for parameter "default_table_access_method": "columnar"
//	DETAIL:  Table access method "columnar" does not exist.
//
// The previous round's verification applied the *canonical* 00-prereqs while
// the installer ran the *embeddata* one, so this gap was invisible to it. The
// same class of gap already had a gate for startup migrations
// (TestStartupFilesAreAllEmbedded) but none for the entry files.
//
// Only 00-prereqs.sql is compared. 01-schema.sql and 02-seed.sql legitimately
// differ between the three trees (the three 01-schema copies carry ~600 lines
// of independent fixes), so a byte or statement-count comparison there would
// be a false signal rather than a gate. What matters for 00-prereqs is the
// extension set, because an absent extension silently removes an access
// method or a function that every later file assumes exists.
//
// Extensions are compared as (name, schema) pairs rather than by bytes: the
// header comments legitimately differ, and what decides whether a fresh
// install works is which extensions get created.
func TestCanonicalParityForEmbeddedPrereqsSchema(t *testing.T) {
	t.Helper()

	repoRoot := filepath.Join("..", "..", "..")
	copies := map[string]string{
		"embeddata":  filepath.Join("embeddata"),
		"canonical":  filepath.Join(repoRoot, "sql", "schema"),
		"deploybase": filepath.Join(repoRoot, "deploy", "sql", "schemas", "baseline"),
	}

	const name = "00-prereqs.sql"
	got := map[string][]string{}
	for label, dir := range copies {
		got[label] = extensionNames(t, filepath.Join(dir, name))
	}

	want := got["canonical"]
	if len(want) == 0 {
		t.Fatalf("canonical %s declares no CREATE EXTENSION; the parse below is suspect", name)
	}
	for label, exts := range got {
		if label == "canonical" {
			continue
		}
		if diff := extSetDiff(want, exts); diff != "" {
			t.Errorf("%s copy of %s has drifted from sql/schema/%s:\n%s\n\n"+
				"    The installer applies its embeddata copy, not the canonical one, so a "+
				"missing extension here fails a fresh install while canonical-only "+
				"verification stays green. Copy the canonical file over.", label, name, name, diff)
		}
	}
}

// extensionNames returns the sorted "extension@schema" pairs a SQL file
// creates. Comments and string literals are stripped first so a CREATE
// EXTENSION mentioned in prose is not counted.
func extensionNames(t *testing.T, path string) []string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	body := stripSQLComments(string(raw))

	var out []string
	for _, line := range strings.Split(body, "\n") {
		l := strings.ToUpper(strings.TrimSpace(line))
		if !strings.HasPrefix(l, "CREATE EXTENSION") {
			continue
		}
		fields := strings.Fields(strings.TrimSpace(line[len("create extension"):]))
		name := ""
		schema := ""
		for i := 0; i < len(fields); i++ {
			switch {
			case name == "" && !strings.EqualFold(fields[i], "IF") &&
				!strings.EqualFold(fields[i], "NOT") && !strings.EqualFold(fields[i], "EXISTS"):
				name = strings.TrimSuffix(fields[i], ";")
			case strings.EqualFold(fields[i], "SCHEMA"):
				if i+1 < len(fields) {
					schema = strings.TrimSuffix(fields[i+1], ";")
				}
			}
			if name != "" && schema != "" {
				break
			}
		}
		if name != "" {
			out = append(out, name+"@"+schema)
		}
	}
	return out
}

func extSetDiff(want, got []string) string {
	w := map[string]bool{}
	for _, x := range want {
		w[x] = true
	}
	g := map[string]bool{}
	for _, x := range got {
		g[x] = true
	}
	var missing, extra []string
	for _, x := range want {
		if !g[x] {
			missing = append(missing, x)
		}
	}
	for _, x := range got {
		if !w[x] {
			extra = append(extra, x)
		}
	}
	if len(missing) == 0 && len(extra) == 0 {
		return ""
	}
	var b strings.Builder
	if len(missing) > 0 {
		b.WriteString("  missing vs canonical: " + strings.Join(missing, ", ") + "\n")
	}
	if len(extra) > 0 {
		b.WriteString("  extra vs canonical:   " + strings.Join(extra, ", ") + "\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// stripSQLComments removes `--` line comments and `/* */` block comments.
func stripSQLComments(sql string) string {
	var b strings.Builder
	for i := 0; i < len(sql); {
		switch {
		case strings.HasPrefix(sql[i:], "--"):
			j := strings.IndexByte(sql[i:], '\n')
			if j < 0 {
				return b.String()
			}
			i += j
		case strings.HasPrefix(sql[i:], "/*"):
			j := strings.Index(sql[i+2:], "*/")
			if j < 0 {
				return b.String()
			}
			i += j + 4
		default:
			b.WriteByte(sql[i])
			i++
		}
	}
	return b.String()
}
