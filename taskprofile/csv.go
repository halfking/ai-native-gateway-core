package taskprofile

import (
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
)

// csv.go — bulk CSV export/import of task-type corrections, closing the data
// loop with the offline annotation workflow (P2.1 exporter/importer shape):
// export low-confidence windows for human review, re-import the verdicts.
//
// CSV columns (header required, order fixed):
//
//	request_id,auto_task_type,human_task_type,agrees,classifier_confidence,profile,annotator,reason,created_at
//
// Import semantics:
//   - human_task_type MUST be registry-valid (the correction feeds the
//     suggestion engine); auto_task_type is free-form (historical classifier
//     output predating the registry).
//   - agrees is recomputed as auto==human (imported verdicts cannot smuggle
//     in a disagrees=true row where the labels match).
//   - request_id conflicts with existing rows are SKIPPED, not errors —
//     re-running an import is idempotent.

// CorrectionCSVHeader is the exact header row of the export/import format.
const CorrectionCSVHeader = "request_id,auto_task_type,human_task_type,agrees,classifier_confidence,profile,annotator,reason,created_at"

// correctionCSVTime is the canonical timestamp layout (RFC3339; parse is
// lenient via time.RFC3339 after trimming).
const correctionCSVTime = time.RFC3339

// ExportCorrectionsCSV streams all corrections since `since` (limit rows) as
// CSV into w. Pure formatting over Recent — no extra SQL.
func (s *CorrectionStore) ExportCorrectionsCSV(ctx context.Context, w io.Writer, since time.Time, limit int) (int, error) {
	if s.pool == nil {
		return 0, errors.New("taskprofile: no DB pool")
	}
	if limit <= 0 || limit > 50000 {
		limit = 10000
	}
	rows, err := s.pool.Query(ctx, `
		SELECT id, request_id, auto_task_type, human_task_type, agrees,
		       classifier_confidence, profile, annotator, reason, created_at
		FROM task_type_corrections
		WHERE created_at >= $1
		ORDER BY created_at ASC
		LIMIT $2
	`, since, limit)
	if err != nil {
		return 0, err
	}
	defer rows.Close()

	cw := csv.NewWriter(w)
	count := 0
	if err := cw.Write(strings.Split(CorrectionCSVHeader, ",")); err != nil {
		return 0, err
	}
	for rows.Next() {
		var c Correction
		if err := rows.Scan(&c.ID, &c.RequestID, &c.AutoTaskType, &c.HumanTaskType, &c.Agrees,
			&c.ClassifierConfidence, &c.Profile, &c.Annotator, &c.Reason, &c.CreatedAt); err != nil {
			return count, err
		}
		record := []string{
			csvSafeFormula(c.RequestID),
			csvSafeFormula(c.AutoTaskType),
			csvSafeFormula(c.HumanTaskType),
			strconv.FormatBool(c.Agrees),
			fmtCSVFloat(c.ClassifierConfidence),
			csvSafeFormula(csvOrEmpty(c.Profile)),
			csvSafeFormula(c.Annotator),
			csvSafeFormula(c.Reason),
			c.CreatedAt.UTC().Format(correctionCSVTime),
		}
		if err := cw.Write(record); err != nil {
			return count, err
		}
		count++
	}
	if err := rows.Err(); err != nil {
		return count, err
	}
	cw.Flush()
	return count, cw.Error()
}

// CorrectionImportRow is one validated CSV row ready for insertion.
type CorrectionImportRow struct {
	RequestID     string
	AutoTaskType  string
	HumanTaskType string
	Agrees        bool
	Confidence    *float64
	Profile       *string
	Annotator     string
	Reason        string
	CreatedAt     time.Time
}

// ParseCorrectionsCSV validates a whole CSV payload and returns the import
// rows. It returns (rows, err): err is non-nil only for structural failures
// (wrong header, no rows); per-row problems are collected in the returned
// per-row error list so a bulk file can be partially imported.
func ParseCorrectionsCSV(r io.Reader, maxRows int) ([]CorrectionImportRow, []CorrectionRowError, error) {
	if maxRows <= 0 || maxRows > 50000 {
		maxRows = 10000
	}
	cr := csv.NewReader(r)
	cr.FieldsPerRecord = -1 // validated per row for a precise message
	header, err := cr.Read()
	if err != nil {
		return nil, nil, fmt.Errorf("read CSV header: %w", err)
	}
	if strings.Join(header, ",") != CorrectionCSVHeader {
		return nil, nil, fmt.Errorf("unexpected CSV header %q, want %q",
			strings.Join(header, ","), CorrectionCSVHeader)
	}

	var (
		rows   []CorrectionImportRow
		errs   []CorrectionRowError
		seen   = map[string]bool{}
		lineNo = 1 // header consumed
	)
	for {
		record, err := cr.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		lineNo++
		if err != nil {
			errs = append(errs, CorrectionRowError{Line: lineNo, Message: "csv parse: " + err.Error()})
			continue
		}
		if len(rows)+len(errs) >= maxRows {
			errs = append(errs, CorrectionRowError{Line: lineNo, Message: fmt.Sprintf("row limit %d reached", maxRows)})
			break
		}
		row, rerr := parseCorrectionRecord(record)
		if rerr != nil {
			errs = append(errs, CorrectionRowError{Line: lineNo, Message: rerr.Error()})
			continue
		}
		if seen[row.RequestID] {
			errs = append(errs, CorrectionRowError{Line: lineNo, Message: "duplicate request_id in file: " + row.RequestID})
			continue
		}
		seen[row.RequestID] = true
		rows = append(rows, row)
	}
	if len(rows) == 0 && len(errs) == 0 {
		return nil, nil, errors.New("CSV contains no data rows")
	}
	return rows, errs, nil
}

// CorrectionRowError is one rejected CSV line.
type CorrectionRowError struct {
	Line    int    `json:"line"`
	Message string `json:"message"`
}

func parseCorrectionRecord(rec []string) (CorrectionImportRow, error) {
	if len(rec) != 9 {
		return CorrectionImportRow{}, fmt.Errorf("want 9 columns, got %d", len(rec))
	}
	trim := func(i int) string { return strings.TrimSpace(rec[i]) }
	row := CorrectionImportRow{
		RequestID:     trim(0),
		AutoTaskType:  trim(1),
		HumanTaskType: trim(2),
		Annotator:     trim(6),
		Reason:        trim(7),
	}
	if row.RequestID == "" {
		return CorrectionImportRow{}, errors.New("request_id required")
	}
	if row.AutoTaskType == "" {
		return CorrectionImportRow{}, errors.New("auto_task_type required")
	}
	if !IsValidTaskType(row.HumanTaskType) {
		return CorrectionImportRow{}, fmt.Errorf("human_task_type %q not in registry", row.HumanTaskType)
	}
	if row.Annotator == "" {
		return CorrectionImportRow{}, errors.New("annotator required")
	}
	if !IsValidReason(row.Reason) {
		return CorrectionImportRow{}, fmt.Errorf("reason %q invalid; valid: %s", row.Reason, reasonsCSV())
	}
	agrees, err := strconv.ParseBool(trim(3))
	if err != nil {
		return CorrectionImportRow{}, fmt.Errorf("agrees %q must be true/false", trim(3))
	}
	row.Agrees = agrees
	// Freeze the verdict semantics: agrees must match the labels.
	if want := row.AutoTaskType == row.HumanTaskType; agrees != want {
		return CorrectionImportRow{}, fmt.Errorf("agrees=%v contradicts auto=%q human=%q",
			agrees, row.AutoTaskType, row.HumanTaskType)
	}
	if v := trim(4); v != "" {
		f, err := strconv.ParseFloat(v, 64)
		if err != nil || f < 0 || f > 1 {
			return CorrectionImportRow{}, fmt.Errorf("classifier_confidence %q must be a float in [0,1]", v)
		}
		row.Confidence = &f
	}
	if v := trim(5); v != "" {
		row.Profile = &v
	}
	if v := trim(8); v != "" {
		t, err := time.Parse(correctionCSVTime, v)
		if err != nil {
			return CorrectionImportRow{}, fmt.Errorf("created_at %q must be RFC3339", v)
		}
		row.CreatedAt = t
	} else {
		row.CreatedAt = time.Now()
	}
	return row, nil
}

// ImportCorrectionsCSV parses and inserts; existing request_ids are skipped.
// Returns the import summary for the API response.
func (s *CorrectionStore) ImportCorrectionsCSV(ctx context.Context, r io.Reader, maxRows int) (ImportResultSummary, error) {
	rows, rowErrs, err := ParseCorrectionsCSV(r, maxRows)
	if err != nil {
		return ImportResultSummary{}, err
	}
	if s.pool == nil {
		return ImportResultSummary{}, errors.New("taskprofile: no DB pool")
	}
	summary := ImportResultSummary{TotalRows: len(rows) + len(rowErrs), RowErrors: rowErrs}
	for _, row := range rows {
		tag, err := s.pool.Exec(ctx, `
			INSERT INTO task_type_corrections
				(request_id, auto_task_type, human_task_type, agrees,
				 classifier_confidence, profile, annotator, reason, created_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
			ON CONFLICT (request_id) DO NOTHING
		`, row.RequestID, row.AutoTaskType, row.HumanTaskType, row.Agrees,
			row.Confidence, row.Profile, row.Annotator, row.Reason, row.CreatedAt)
		if err != nil {
			summary.RowErrors = append(summary.RowErrors,
				CorrectionRowError{Message: fmt.Sprintf("insert %s: %v", row.RequestID, err)})
			continue
		}
		if tag.RowsAffected() == 0 {
			summary.Skipped++
		} else {
			summary.Imported++
			if s.recorder != nil {
				// Same feedback path as Record: every imported verdict is one
				// classification observation for the aggregator/Prometheus.
				s.recorder.RecordFeedback(row.AutoTaskType, row.Agrees)
			}
		}
	}
	return summary, nil
}

// ImportResultSummary is the JSON shape of the import endpoint response.
type ImportResultSummary struct {
	TotalRows int                  `json:"total_rows"`
	Imported  int                  `json:"imported"`
	Skipped   int                  `json:"skipped"`
	RowErrors []CorrectionRowError `json:"row_errors"`
}

// AppliedTierConfig is one task_type_tier_config row written by ApplySuggestions.
type AppliedTierConfig struct {
	TaskType      string  `json:"task_type"`
	PreferredTier string  `json:"preferred_tier"`
	MinConfidence float64 `json:"min_confidence"`
	TierSource    string  `json:"tier_source"`
}

// ApplySuggestions writes the current Suggestion for each requested task type
// into task_type_tier_config. Empty taskTypes defaults to the types whose
// current suggestion is correction_escalation — the operator-visible "apply
// the correction-driven escalations" action. The write is explicit by design
// (design doc §5.2): the suggestion engine never mutates tier config on its
// own. R43 note: no runtime component reads this table yet
// (autoroute.NewTierSelector has zero production constructors), so applying
// persists operator intent without changing routing behavior.
func (s *CorrectionStore) ApplySuggestions(ctx context.Context, taskTypes []string) ([]AppliedTierConfig, error) {
	if s.pool == nil {
		return nil, errors.New("taskprofile: no DB pool")
	}
	stats, err := s.Stats(ctx, time.Now().Add(-30*24*time.Hour))
	if err != nil {
		return nil, err
	}
	if len(taskTypes) == 0 {
		for _, tt := range TaskTypes() {
			if sug := Suggest(tt, 1.0, stats); sug.TierSource == "correction_escalation" {
				taskTypes = append(taskTypes, tt)
			}
		}
	}
	applied := make([]AppliedTierConfig, 0, len(taskTypes))
	for _, tt := range taskTypes {
		if !IsValidTaskType(tt) {
			return applied, fmt.Errorf("unknown task_type %q", tt)
		}
		sug := Suggest(tt, 1.0, stats)
		// R43 (2026-09-18): ON CONFLICT 推断必须匹配表的**真实**唯一索引
		// （deploy V370：tenant_id BIGINT + COALESCE(tenant_id, 0) 哨兵）。
		// 原来按 202609_02 的 COALESCE(tenant_id,'') 推断，真表上 42P10。
		tag, err := s.pool.Exec(ctx, `
			INSERT INTO task_type_tier_config
				(task_type, preferred_tier, fallback_tiers, min_confidence, tenant_id, enabled, description)
			VALUES ($1, $2, $3, $4, NULL, TRUE, $5)
			ON CONFLICT (task_type, COALESCE(tenant_id, 0)) DO UPDATE SET
				preferred_tier = EXCLUDED.preferred_tier,
				fallback_tiers = EXCLUDED.fallback_tiers,
				min_confidence = EXCLUDED.min_confidence,
				enabled = TRUE,
				description = EXCLUDED.description,
				updated_at = NOW()
		`, tt, sug.Tier, sug.FallbackTiers, sug.MinConfidence,
			fmt.Sprintf("taskprofile suggestion (source=%s, registry=%s)", sug.TierSource, SnapshotVersion()))
		if err != nil {
			return applied, fmt.Errorf("upsert task_type_tier_config %s: %w", tt, err)
		}
		_ = tag
		applied = append(applied, AppliedTierConfig{
			TaskType:      tt,
			PreferredTier: sug.Tier,
			MinConfidence: sug.MinConfidence,
			TierSource:    sug.TierSource,
		})
	}
	return applied, nil
}

// ErrTierConfigMissing reports a deployment whose task_type_tier_config
// (dated migration 202609_02) is absent — apply degrades to a clear error.
func ErrTierConfigMissing(err error) bool {
	return err != nil && strings.Contains(err.Error(), "does not exist") &&
		strings.Contains(err.Error(), "task_type_tier_config")
}

func csvOrEmpty(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// csvSafeFormula neutralizes CSV formula injection (R43, 2026-09-18):
// request_id/annotator/reason are human-entered, and a value like
// `=cmd|...` or `@SUM(...)` executes when the export is opened in
// Excel/WPS. Fields starting with = + - @ tab or CR get a `'` prefix,
// which spreadsheets treat as literal text. Exports are for human
// spreadsheet consumption; re-import goes through DB-side validation
// instead of parsing this prefix back.
func csvSafeFormula(s string) string {
	if s == "" {
		return s
	}
	switch s[0] {
	case '=', '+', '-', '@', '\t', '\r':
		return "'" + s
	}
	return s
}

func fmtCSVFloat(f *float64) string {
	if f == nil {
		return ""
	}
	return strconv.FormatFloat(*f, 'g', -1, 64)
}
