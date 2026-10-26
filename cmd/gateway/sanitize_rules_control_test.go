package main

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
	"github.com/kaixuan/llm-gateway-go/domains/sanitizerules"
	"github.com/kaixuan/llm-gateway-go/domains/streaming"
	"github.com/kaixuan/llm-gateway-go/security/sanitize"
)

func TestSanitizeRulesInstallsLocalManagerWhenLegacyImportFails(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mock.MatchExpectationsInOrder(false)
	mock.ExpectQuery("FROM sanitize_rule_versions").WillReturnRows(sqlmock.NewRows([]string{
		"id", "parent_id", "rollback_of", "config", "actor", "comment", "created_at", "review_links",
	}))
	mock.ExpectQuery("FROM pii_patterns").WillReturnError(errors.New("temporary database failure with synthetic-secret"))
	mock.ExpectExec("INSERT INTO sanitize_rule_instances").WillReturnResult(sqlmock.NewResult(0, 1))
	published := sanitizerules.Config{Rules: []sanitizerules.Rule{{ID: "published.rule", Name: "Published policy", Type: "custom", Kind: "pattern", Pattern: "published-value", Scopes: []string{"output"}, Action: "mask", Enabled: true}}}
	encoded, _ := json.Marshal(published)
	mock.ExpectQuery("FROM sanitize_rule_versions").WillReturnRows(sqlmock.NewRows([]string{
		"id", "parent_id", "rollback_of", "config", "actor", "comment", "created_at", "review_links",
	}).AddRow(7, 6, 0, encoded, "admin", "published elsewhere", time.Now(), []byte("[]")))
	mock.ExpectExec("INSERT INTO sanitize_rule_instances").WillReturnResult(sqlmock.NewResult(0, 1))
	detector, err := sanitize.NewPatternDetectorFromFile("../../configs/sensitive_patterns.yaml")
	if err != nil {
		t.Fatal(err)
	}
	oldDetector, oldStop := activeRuleDetector.Load(), sanitizeRulesStop
	defer func() { stopSanitizeRules(); activeRuleDetector.Store(oldDetector); sanitizeRulesStop = oldStop }()
	h := streaming.NewChatHandler(nil, nil, nil, nil, nil, nil)
	installSanitizeRuleManager(h, detector, db, nil)
	m := detector.RuleManager()
	if m == nil || activeRuleDetector.Load() != detector || managedOutputRuleSource() == nil {
		t.Fatal("temporary legacy import failure left gateway without installed manager/policy source")
	}
	deadline := time.Now().Add(2 * time.Second)
	for m.Current().Version != 7 || mock.ExpectationsWereMet() != nil {
		if time.Now().After(deadline) {
			t.Fatalf("installed manager did not adopt recovered DB head: version=%d error=%s", m.Current().Version, m.Status().Error)
		}
		time.Sleep(time.Millisecond)
	}
	if got := m.Current().Rules; len(got) != 1 || got[0].ID != "published.rule" {
		t.Fatal("legacy baseline replaced the recovered published head")
	}
}

func TestSanitizeRulesRetriesLegacyImportBeforeSeedingCompleteBaseline(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mock.ExpectQuery("FROM pii_patterns").WillReturnError(errors.New("legacy import temporarily unavailable synthetic-secret"))
	mock.ExpectQuery("FROM pii_patterns").WillReturnRows(sqlmock.NewRows([]string{"id", "pattern_name", "pattern_type", "regex_pattern", "enabled"}).
		AddRow(71, "Custom legacy identity", "custom", `legacy=\w+`, true).
		AddRow(72, "Disabled legacy bank card", "bank_card", `card=\d+`, false))
	detector, err := sanitize.NewPatternDetectorFromFile("../../configs/sensitive_patterns.yaml")
	if err != nil {
		t.Fatal(err)
	}
	store := sanitizerules.NewMemoryStore()
	m, err := newSanitizeRuleManager(detector, db, store, sanitizerules.Options{InstanceID: "startup-import", PollInterval: func() time.Duration { return 10 * time.Millisecond }})
	if err != nil || m == nil {
		t.Fatal("local manager not created", err)
	}
	if m.Current().Version != 0 || len(m.Current().Rules) <= len(detector.RuleConfig().Rules) {
		t.Fatal("offline snapshot must retain local input and built-in output protection")
	}
	pinned := m.Pin(context.Background())
	if err := m.Refresh(context.Background()); err == nil || strings.Contains(err.Error(), "synthetic-secret") {
		t.Fatal("failed import must be observable without raw database values", err)
	}
	if head, _ := store.Current(context.Background()); head.ID != 0 || m.Current().Version != 0 {
		t.Fatal("failed legacy import published a partial baseline")
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); m.Run(ctx) }()
	defer func() { cancel(); <-done }()
	deadline := time.Now().Add(2 * time.Second)
	for m.Current().Version == 0 {
		if time.Now().After(deadline) {
			t.Fatal("background polling did not retry the failed legacy import")
		}
		time.Sleep(time.Millisecond)
	}
	head, err := store.Current(ctx)
	if err != nil || head.ID != 1 || len(head.Config.Rules) != len(detector.RuleConfig().Rules)+3 {
		t.Fatal("recovered baseline lost local/legacy/telemetry rules", head.ID, err)
	}
	found := map[string]sanitizerules.Rule{}
	for _, rule := range head.Config.Rules {
		found[rule.ID] = rule
	}
	if rule := found["compliance.pii.71"]; !rule.Enabled || rule.Pattern != `legacy=\w+` || rule.Scopes[0] != "output" {
		t.Fatal("enabled custom legacy PII lost", rule)
	}
	if rule := found["compliance.pii.72"]; rule.Enabled || rule.Type != "credit_card" {
		t.Fatal("disabled legacy PII or canonical type lost", rule)
	}
	if found["compliance.internal_ip"].Action != "log" || m.SnapshotFor(pinned).Version != 0 {
		t.Fatal("baseline telemetry or request pinning lost")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestSanitizeRulesLegacyImportHonorsContextDeadline(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mock.ExpectQuery("FROM pii_patterns").WillDelayFor(time.Second).WillReturnRows(sqlmock.NewRows([]string{"id", "pattern_name", "pattern_type", "regex_pattern", "enabled"}))
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	started := time.Now()
	if _, err := loadSanitizeRulesBaseline(ctx, db, sanitizerules.Config{}); err == nil || time.Since(started) > 500*time.Millisecond {
		t.Fatal("legacy import failed to honor refresh deadline", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
