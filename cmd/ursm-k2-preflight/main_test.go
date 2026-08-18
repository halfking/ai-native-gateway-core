package main

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

// TestApplyRemainsBlockedBeforeRedisConnection ensures the T0 NO-GO
// fail-closed gate fires before flag.Parse / redis.ParseURL / rdb.Ping
// so a misconfigured --redis cannot trigger a network round trip. The
// preflight binary is read-only by default; passing --apply must still
// be refused with the stable T0 marker text.
func TestApplyRemainsBlockedBeforeRedisConnection(t *testing.T) {
	command := exec.Command(os.Args[0], "-test.run=TestK2PreflightHelper", "--", "--apply", "--redis=redis://127.0.0.1:1/2")
	command.Env = append(os.Environ(), "K2_PREFLIGHT_HELPER=1")
	output, err := command.CombinedOutput()
	if err == nil {
		t.Fatal("--apply must be blocked while T0 is NO-GO")
	}
	if !strings.Contains(string(output), "T0 is BLOCKED / NO-GO") {
		t.Fatalf("apply refusal = %q", output)
	}
}

func TestShortApplyRemainsBlockedBeforeRedisConnection(t *testing.T) {
	command := exec.Command(os.Args[0], "-test.run=TestK2PreflightShortApplyHelper", "--", "-apply", "--redis=redis://127.0.0.1:1/2")
	command.Env = append(os.Environ(), "K2_PREFLIGHT_SHORT_APPLY_HELPER=1")
	output, err := command.CombinedOutput()
	if err == nil {
		t.Fatal("-apply must be blocked while T0 is NO-GO")
	}
	if !strings.Contains(string(output), "T0 is BLOCKED / NO-GO") {
		t.Fatalf("short apply refusal = %q", output)
	}
}

func TestK2PreflightHelper(t *testing.T) {
	if os.Getenv("K2_PREFLIGHT_HELPER") != "1" {
		return
	}
	os.Args = []string{"ursm-k2-preflight", "--apply", "--redis=redis://127.0.0.1:1/2"}
	main()
}

func TestK2PreflightShortApplyHelper(t *testing.T) {
	if os.Getenv("K2_PREFLIGHT_SHORT_APPLY_HELPER") != "1" {
		return
	}
	os.Args = []string{"ursm-k2-preflight", "-apply", "--redis=redis://127.0.0.1:1/2"}
	main()
}
