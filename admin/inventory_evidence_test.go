package admin

import (
	"go/parser"
	"go/token"
	"strings"
	"testing"
)

func inventoryEvidenceInCode(source, evidence string) bool {
	if strings.TrimSpace(evidence) == "" {
		return false
	}
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "evidence.go", source, parser.ParseComments|parser.SkipObjectResolution)
	if err != nil {
		return false
	}
	var comments [][2]int
	for _, group := range file.Comments {
		for _, comment := range group.List {
			start := fset.Position(comment.Pos()).Offset
			end := len(source)
			// ast.Comment.End uses normalized Text (without CR), not raw
			// source length. Only its start can anchor original-byte ranges.
			if strings.HasPrefix(source[start:], "//") {
				if newline := strings.IndexByte(source[start:], '\n'); newline >= 0 {
					end = start + newline
				}
			} else if close := strings.Index(source[start+2:], "*/"); close >= 0 {
				end = start + 2 + close + 2
			}
			comments = append(comments, [2]int{start, end})
		}
	}
	// Preserve exact source fragments, including SQL composition and strings
	// containing // or /*. Removing comments could join unrelated code tokens.
	for offset := 0; offset < len(source); {
		rel := strings.Index(source[offset:], evidence)
		if rel < 0 {
			break
		}
		start := offset + rel
		end := start + len(evidence)
		inComment := false
		for _, comment := range comments {
			if start < comment[1] && end > comment[0] {
				inComment = true
				break
			}
		}
		if !inComment {
			return true
		}
		offset = end
	}
	return false
}

func TestInventoryEvidenceRequiresCode(t *testing.T) {
	const evidence = "FROM _request_logs_hot"
	for _, tc := range []struct {
		name, source string
		want         bool
	}{
		{"raw SQL", "package p; var q = `SELECT ts FROM _request_logs_hot`", true},
		{"interpreted SQL", `package p; var q = "SELECT ts FROM _request_logs_hot"`, true},
		{"line comment only", "package p\n// FROM _request_logs_hot\nvar q = `SELECT 1`", false},
		{"block comment only", "package p; /* FROM _request_logs_hot */ var q = `SELECT 1`", false},
		{"line comment CR offsets", "package p\n//" + strings.Repeat("\r", 40) + evidence + "\nvar q = `SELECT 1`", false},
		{"block comment CR offsets", "package p; /*" + strings.Repeat("\r", 40) + evidence + "*/ var q = `SELECT 1`", false},
		{"CRLF then real code", "package p\r\n//" + evidence + "\r\nvar q = `SELECT ts FROM _request_logs_hot`", true},
		{"real after stale comment", "package p\n// FROM _request_logs_hot\nvar q = `SELECT ts FROM _request_logs_hot`", true},
		{"SQL comment within literal", "package p; var q = `SELECT ts FROM _request_logs_hot -- sqlreadguard:allow`", true},
		{"slash within literal", `package p; var q = "http://example.internal FROM _request_logs_hot"`, true},
		{"invalid source", "package p; var q = `FROM _request_logs_hot", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := inventoryEvidenceInCode(tc.source, evidence); got != tc.want {
				t.Fatalf("code evidence = %v, want %v", got, tc.want)
			}
		})
	}
	for _, evidence := range []string{"", " \n\t"} {
		if inventoryEvidenceInCode("package p; var q = `SELECT 1`", evidence) {
			t.Fatal("empty evidence accepted")
		}
	}
}
