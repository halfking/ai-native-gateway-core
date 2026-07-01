package routing

import (
	"context"
	"strings"
	"testing"
	"time"
)

// ── validation tests (no DB) ──────────────────────────────────

func TestValidateCommand_TaskTypeRequired(t *testing.T) {
	cmd := CreateRouteCommand{
		Profile:     ProfileSmart,
		Mode:        ModePin,
		ModelChosen: stringPtr("gpt-4"),
		Reason:      "test",
		CreatedBy:   "admin",
	}
	if err := validateCommand(&cmd); err == nil {
		t.Fatal("expected ValidationError for empty task_type")
	} else if !IsValidationError(err) {
		t.Fatalf("expected *ValidationError, got %T: %v", err, err)
	}
}

func TestValidateCommand_InvalidProfile(t *testing.T) {
	cmd := validCommand()
	cmd.Profile = "experimental"
	err := validateCommand(&cmd)
	if !IsValidationError(err) {
		t.Fatalf("expected ValidationError for invalid profile, got %v", err)
	}
	if !strings.Contains(err.Error(), "profile") {
		t.Fatalf("expected error to mention profile, got: %v", err)
	}
}

func TestValidateCommand_ProfileDefaultsToSmart(t *testing.T) {
	cmd := validCommand()
	cmd.Profile = ""
	if err := validateCommand(&cmd); err != nil {
		t.Fatalf("expected empty profile to default to smart, got %v", err)
	}
	if cmd.Profile != ProfileSmart {
		t.Fatalf("expected profile defaulted to %q, got %q", ProfileSmart, cmd.Profile)
	}
}

func TestValidateCommand_InvalidMode(t *testing.T) {
	cmd := validCommand()
	cmd.Mode = "redirect"
	err := validateCommand(&cmd)
	if !IsValidationError(err) {
		t.Fatalf("expected ValidationError for invalid mode, got %v", err)
	}
}

func TestValidateCommand_BanRequiresModelChosen(t *testing.T) {
	cmd := validCommand()
	cmd.Mode = ModeBan
	cmd.ModelChosen = nil
	err := validateCommand(&cmd)
	if !IsValidationError(err) {
		t.Fatalf("expected ValidationError for ban without model_chosen, got %v", err)
	}

	cmd.ModelChosen = stringPtr("")
	err = validateCommand(&cmd)
	if !IsValidationError(err) {
		t.Fatalf("expected ValidationError for ban with empty model_chosen, got %v", err)
	}
}

func TestValidateCommand_ReasonRequired(t *testing.T) {
	cmd := validCommand()
	cmd.Reason = "   "
	err := validateCommand(&cmd)
	if !IsValidationError(err) {
		t.Fatalf("expected ValidationError for blank reason, got %v", err)
	}
}

func TestValidateCommand_CreatedByDefaultsToAdmin(t *testing.T) {
	cmd := validCommand()
	cmd.CreatedBy = ""
	if err := validateCommand(&cmd); err != nil {
		t.Fatalf("expected empty created_by to default, got %v", err)
	}
	if cmd.CreatedBy != "admin" {
		t.Fatalf("expected created_by defaulted to admin, got %q", cmd.CreatedBy)
	}
}

func TestValidateCommand_HappyPath_Pin(t *testing.T) {
	cmd := validCommand()
	cmd.Mode = ModePin
	cmd.ModelChosen = stringPtr("gpt-4o")
	if err := validateCommand(&cmd); err != nil {
		t.Fatalf("expected pin override to validate, got %v", err)
	}
}

func TestValidateCommand_HappyPath_Ban(t *testing.T) {
	cmd := validCommand()
	cmd.Mode = ModeBan
	cmd.ModelChosen = stringPtr("gpt-3.5-turbo")
	if err := validateCommand(&cmd); err != nil {
		t.Fatalf("expected ban override to validate, got %v", err)
	}
}

// ── NewHandler guard ──────────────────────────────────────────

func TestNewHandler_NilPoolPanics(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic on nil pool")
		}
	}()
	_ = NewHandler(nil)
}

// ── Handler.Create validation path (no DB) ────────────────────
//
// The validation runs before any DB call, so we can exercise it with
// a nil pool — but NewHandler panics, so we construct the struct
// directly via a tiny internal helper.

func TestHandler_Create_ValidationErrorBeforeDB(t *testing.T) {
	h := &Handler{db: nil}                  //nolint:exhaustruct // validation runs first
	cmd := CreateRouteCommand{TaskType: ""} // triggers ValidationError
	_, err := h.Create(context.Background(), cmd)
	if !IsValidationError(err) {
		t.Fatalf("expected ValidationError before DB call, got %T: %v", err, err)
	}
}

// ── integration test (skipped unless TEST_DATABASE_URL is set) ──

func TestHandler_Create_Integration(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}
	url := testDatabaseURL()
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	// Real DB path — out of scope for the unit-test PoC. Kept here
	// as a placeholder so future CI wiring can drop in a connection
	// string without re-deriving the test skeleton.
	t.Skip("integration harness not wired up in PoC; see TEST_DATABASE_URL")
}

// ── helpers ──────────────────────────────────────────────────

func validCommand() CreateRouteCommand {
	return CreateRouteCommand{
		TaskType:    "summarize",
		Profile:     ProfileSmart,
		Mode:        ModePin,
		ModelChosen: stringPtr("claude-opus-4"),
		Reason:      "pin because of high quality on long context",
		CreatedBy:   "test-admin",
		ExpiresAt:   nil,
	}
}

func stringPtr(s string) *string { return &s }

// testDatabaseURL mirrors admin.testDBURL but lives in the control
// package so it has no admin dependency. Returns empty when no DB is
// configured so callers can t.Skip cleanly.
func testDatabaseURL() string {
	// Kept stub for the PoC — wiring up a real connection string is
	// the B1 follow-up.
	_ = time.Now()
	return ""
}
