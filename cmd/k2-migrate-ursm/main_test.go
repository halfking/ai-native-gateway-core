package main

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestApplyRemainsBlockedBeforeRedisConnection(t *testing.T) {
	command := exec.Command(os.Args[0], "-test.run=TestK2MigrateHelper", "--", "copy", "--apply", "--redis=redis://127.0.0.1:1/15")
	command.Env = append(os.Environ(), "K2_MIGRATE_HELPER=1")
	output, err := command.CombinedOutput()
	if err == nil {
		t.Fatal("--apply must be blocked while T0 is NO-GO")
	}
	if !strings.Contains(string(output), "T0 is BLOCKED / NO-GO") {
		t.Fatalf("apply refusal = %q", output)
	}
}

func TestShortApplyRemainsBlockedBeforeRedisConnection(t *testing.T) {
	command := exec.Command(os.Args[0], "-test.run=TestK2MigrateShortApplyHelper", "--", "copy", "-apply", "--redis=redis://127.0.0.1:1/15")
	command.Env = append(os.Environ(), "K2_MIGRATE_SHORT_APPLY_HELPER=1")
	output, err := command.CombinedOutput()
	if err == nil {
		t.Fatal("-apply must be blocked while T0 is NO-GO")
	}
	if !strings.Contains(string(output), "T0 is BLOCKED / NO-GO") {
		t.Fatalf("short apply refusal = %q", output)
	}
}

func TestK2MigrateHelper(t *testing.T) {
	if os.Getenv("K2_MIGRATE_HELPER") != "1" {
		return
	}
	os.Args = []string{"k2-migrate-ursm", "copy", "--apply", "--redis=redis://127.0.0.1:1/15"}
	main()
}

func TestK2MigrateShortApplyHelper(t *testing.T) {
	if os.Getenv("K2_MIGRATE_SHORT_APPLY_HELPER") != "1" {
		return
	}
	os.Args = []string{"k2-migrate-ursm", "copy", "-apply", "--redis=redis://127.0.0.1:1/15"}
	main()
}
