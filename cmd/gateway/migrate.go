package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/kaixuan/llm-gateway-go/db"
)

// migrationReport is the stdout JSON output of `gateway migrate`.
// Launcher parses it to record what was applied.
type migrationReport struct {
	Status  string `json:"status"`  // "ok" | "noop" | "error"
	Message string `json:"message"` // error message when status=error
	HasDB   bool   `json:"has_db"`  // whether DATABASE_URL was non-empty
}

// runMigrate connects DB, exits.
// exit 0 = success (includes idempotent noop and no-DB cases).
// exit 1 = failure (stderr JSON, stdout nothing).
func runMigrate(databaseURL string) int {
	report, err := doMigrate(databaseURL)
	return writeReport(os.Stdout, report, err)
}

func writeReport(w io.Writer, report migrationReport, err error) int {
	if err != nil {
		_, _ = fmt.Fprintf(w, `{"status":"error","message":%q}`+"\n", err.Error())
		return 1
	}
	out, _ := json.Marshal(report)
	_, _ = fmt.Fprintln(w, string(out))
	return 0
}

// doMigrate is the testable core: no os.Exit, no os.Stdout side effects.
func doMigrate(databaseURL string) (migrationReport, error) {
	if databaseURL == "" {
		return migrationReport{Status: "noop", HasDB: false}, nil
	}
	conn, err := db.Open(context.Background(), databaseURL)
	if err != nil {
		return migrationReport{Status: "error", HasDB: true}, fmt.Errorf("db open: %w", err)
	}
	if conn == nil {
		return migrationReport{Status: "noop", HasDB: false}, nil
	}
	defer conn.Close()
	// db.Open already runs ApplyMigrations internally.
	return migrationReport{Status: "ok", HasDB: true}, nil
}

// runMigrateWithCapture is the test entry point. Returns (exitCode, stdout buffer).
// Tests use this instead of calling runMigrate (which uses os.Exit/os.Stdout).
func runMigrateWithCapture(databaseURL string) (int, *bytes.Buffer) {
	var buf bytes.Buffer
	report, err := doMigrate(databaseURL)
	code := writeReport(&buf, report, err)
	return code, &buf
}
