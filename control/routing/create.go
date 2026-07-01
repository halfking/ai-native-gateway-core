// Package routing — write side (CQRS command) for routing overrides.
//
// Task B1 (PoC): this package demonstrates the control/query split
// for the autoroute subsystem. Only the CreateRoute command is
// migrated in this PoC; the remaining mutations (delete, extend)
// stay in admin/routing_overrides.go and will move in B2+.
//
// Why a separate package:
//   - admin/ already imports 30+ subsystems; moving the command
//     surface into control/routing/ lets future B2+ extractions
//     keep the import graph shallow (admin -> control/routing -> db).
//   - Validation lives next to the command struct, not in the HTTP
//     layer, so a future gRPC / queue entry point can reuse the
//     same checks.
//   - Tests can target control/routing in isolation without the
//     admin HTTP machinery.
package routing

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Profile constants — must match the routing_overrides.profile CHECK constraint.
const (
	ProfileSmart      = "smart"
	ProfileSpeedFirst = "speed_first"
	ProfileCostFirst  = "cost_first"
)

// Mode constants — must match the routing_overrides.mode CHECK constraint.
const (
	ModePin = "pin"
	ModeBan = "ban"
)

// pgUniqueViolation is SQLSTATE 23505. Defined as a constant so tests
// can assert on it without hard-coding the magic number.
const pgUniqueViolation = "23505"

// ValidationError is returned when the command payload fails input
// validation before any database call. The HTTP facade maps this to
// 400 Bad Request.
type ValidationError struct{ Msg string }

func (e *ValidationError) Error() string { return e.Msg }

// IsValidationError reports whether err is a *ValidationError. Useful
// for the admin facade to distinguish 400 from 500.
func IsValidationError(err error) bool {
	var v *ValidationError
	return errors.As(err, &v)
}

// DuplicateError is returned when the INSERT trips the
// (task_type, profile, model_chosen, mode) unique constraint. The HTTP
// facade maps this to 409 Conflict.
type DuplicateError struct{ Cause error }

func (e *DuplicateError) Error() string {
	if e.Cause == nil {
		return "duplicate routing override"
	}
	return "duplicate routing override: " + e.Cause.Error()
}
func (e *DuplicateError) Unwrap() error { return e.Cause }

// IsDuplicateError reports whether err is a *DuplicateError.
func IsDuplicateError(err error) bool {
	var d *DuplicateError
	return errors.As(err, &d)
}

// CreateRouteCommand is the input to Handler.Create.
//
// Field semantics mirror the routing_overrides table; Profile and Mode
// are validated against the table CHECK constraints, TaskType and
// Reason are non-blank strings. ModelChosen is required when Mode =
// ModeBan (ban needs a target).
type CreateRouteCommand struct {
	TaskType    string
	Profile     string
	Mode        string
	ModelChosen *string
	Reason      string
	CreatedBy   string
	ExpiresAt   *time.Time
}

// Handler executes CreateRouteCommand against the routing_overrides
// table. Safe for concurrent use — each call opens its own transaction.
type Handler struct {
	db *pgxpool.Pool
}

// NewHandler wires the command handler to a pgxpool. The pool must be
// non-nil; passing nil is a programming error and will panic on the
// first call (mirrors pgxpool idioms).
func NewHandler(db *pgxpool.Pool) *Handler {
	if db == nil {
		panic("control/routing: NewHandler requires a non-nil *pgxpool.Pool")
	}
	return &Handler{db: db}
}

// Create validates the command, opens a transaction, sets the
// app.current_admin GUC so the DB trigger can record the actor, and
// inserts a single row. Returns the new override's ID on success.
//
// Error contract:
//   - *ValidationError → caller-supplied payload is invalid (400).
//   - *DuplicateError  → unique constraint tripped (409).
//   - any other error   → database / transaction failure (500).
func (h *Handler) Create(ctx context.Context, cmd CreateRouteCommand) (int64, error) {
	if err := validateCommand(&cmd); err != nil {
		return 0, err
	}

	tx, err := h.db.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("control/routing: begin tx: %w", err)
	}
	// Best-effort rollback. If Commit succeeds this is a no-op.
	//nolint:errcheck // deferred rollback, intentional
	defer tx.Rollback(ctx)

	// P7.9: set the actor GUC so the audit trigger records the right
	// admin user. Single-quote escape is intentional — SET LOCAL
	// doesn't accept parameter binding for the value.
	escapedActor := strings.ReplaceAll(cmd.CreatedBy, "'", "''")
	if _, err := tx.Exec(ctx, "SET LOCAL app.current_admin = '"+escapedActor+"'"); err != nil {
		return 0, fmt.Errorf("control/routing: set actor guc: %w", err)
	}

	var newID int64
	err = tx.QueryRow(ctx, `
		INSERT INTO routing_overrides
		    (task_type, profile, mode, model_chosen, reason, created_by, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		RETURNING id
	`, cmd.TaskType, cmd.Profile, cmd.Mode, cmd.ModelChosen,
		cmd.Reason, cmd.CreatedBy, cmd.ExpiresAt).Scan(&newID)
	if err != nil {
		if isUniqueViolation(err) {
			return 0, &DuplicateError{Cause: err}
		}
		return 0, fmt.Errorf("control/routing: insert override: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("control/routing: commit tx: %w", err)
	}
	return newID, nil
}

// validateCommand enforces the same checks the admin facade used to
// do inline. Kept package-private so the only entry point is Create —
// callers can't bypass validation by reaching for the struct directly.
func validateCommand(cmd *CreateRouteCommand) error {
	if strings.TrimSpace(cmd.TaskType) == "" {
		return &ValidationError{Msg: "task_type is required"}
	}
	if cmd.Profile == "" {
		cmd.Profile = ProfileSmart
	}
	if cmd.Profile != ProfileSmart && cmd.Profile != ProfileSpeedFirst && cmd.Profile != ProfileCostFirst {
		return &ValidationError{Msg: "profile must be smart| speed_first| cost_first"}
	}
	if cmd.Mode != ModePin && cmd.Mode != ModeBan {
		return &ValidationError{Msg: "mode must be 'pin' or 'ban'"}
	}
	if cmd.Mode == ModeBan && (cmd.ModelChosen == nil || *cmd.ModelChosen == "") {
		return &ValidationError{Msg: "ban mode requires model_chosen"}
	}
	if strings.TrimSpace(cmd.Reason) == "" {
		return &ValidationError{Msg: "reason is required (audit trail)"}
	}
	if strings.TrimSpace(cmd.CreatedBy) == "" {
		cmd.CreatedBy = "admin"
	}
	return nil
}

// isUniqueViolation mirrors admin.isUniqueViolation but lives here so
// control/routing has no dependency on the admin package. Kept
// defensive — checks both SQLSTATE and a substring fallback for
// drivers that don't unwrap cleanly.
func isUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code == pgUniqueViolation
	}
	// Defensive fallback — pgx normally wraps, but tests and unusual
	// drivers may not.
	return strings.Contains(err.Error(), "duplicate key") ||
		strings.Contains(err.Error(), "unique constraint") ||
		strings.Contains(err.Error(), pgUniqueViolation)
}

// Compile-time guard: keep pgx imported even when this file is the only
// one using the package — the pgxpool.Pool field already references
// it transitively, but this avoids a "imported and not used" surprise
// if future refactors drop the only reference.
var _ = pgx.ErrNoRows
