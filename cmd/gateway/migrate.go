package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
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

// runMigrate connects DB, calls ApplyMigrations, exits.
// exit 0 = success (includes idempotent noop and no-DB cases).
// exit 1 = failure (stderr JSON, stdout nothing).
func runMigrate(databaseURL string) int {
	report, err := doMigrate(databaseURL)
	if err != nil {
		fmt.Fprintf(os.Stderr, `{"status":"error","message":%q}`+"\n", err.Error())
		return 1
	}
	out, _ := json.Marshal(report)
	fmt.Println(string(out))
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
	if err := conn.ApplyMigrations(context.Background()); err != nil {
		return migrationReport{Status: "error", HasDB: true}, fmt.Errorf("apply migrations: %w", err)
	}
	return migrationReport{Status: "ok", HasDB: true}, nil
}

// runMigrateWithCapture is the test entry point. Returns (exitCode, stdout buffer).
// Tests use this instead of calling runMigrate (which uses os.Exit/os.Stdout).
func runMigrateWithCapture(databaseURL string) (int, *bytes.Buffer) {
	var buf bytes.Buffer
	report, err := doMigrate(databaseURL)
	if err != nil {
		// Mirror runMigrate's stderr write but to buf (tests don't assert stderr)
		_, _ = fmt.Fprintf(&buf, `{"status":"error","message":%q}`+"\n", err.Error())
		return 1, &buf
	}
	out, _ := json.Marshal(report)
	buf.Write(out)
	// Mirror runMigrate's trailing newline (json + \n)
	buf.WriteByte('\n')
	return 0, &buf
}