// Package migration hosts L3 machinery for the URSM delimiter-safe Redis
// key migration described in doc 14 §§2-6, doc 15 §§1-4 and doc 16 slices
// 11-13. This file implements the read-only node/window/index/dedup
// preflight slice only: classification, deterministic inventory and
// ledger; no writes to production Redis metadata, no copy, no coverage
// publication and no cleanup. Owners of subsequent slices must freeze
// the metadata key schema, ACL and CAS conventions before any follow-on
// command ships.
package migration

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/kaixuan/llm-gateway-go/domains/ursm/v2/store"
)

// SchemaMode identifies which key grammar the authoritative request path
// recognises. Schema mode is orthogonal to URSM_V2_MODE: the latter still
// defines legacy/URSM v2 routing authority (doc 14 §3, doc 15 §4).
type SchemaMode string

const (
	SchemaModeLegacy    SchemaMode = "legacy"
	SchemaModeDual      SchemaMode = "dual"
	SchemaModeCanonical SchemaMode = "canonical"
)

// Classification is the per-source-key verdict produced by Preflight.
type Classification string

const (
	ClassMigratable               Classification = "migratable"
	ClassCanonicalPresent         Classification = "canonical_present"
	ClassAmbiguous                Classification = "ambiguous"
	ClassExcludedNonAuthoritative Classification = "excluded_non_authoritative"
	ClassConflict                 Classification = "conflict"
)

// EntryStatus is the per-entry lifecycle (doc 15 §4). Preflight only
// ever sets Classified, Excluded, Ambiguous or Conflict.
type EntryStatus string

const (
	StatusClassified EntryStatus = "classified"
	StatusExcluded   EntryStatus = "excluded"
	StatusAmbiguous  EntryStatus = "ambiguous"
	StatusConflict   EntryStatus = "conflict"
)

// Checkpoint names the phases the runner moves through. Preflight sets
// "preflight"; later slices advance to copy/observe/cleanup.
type Checkpoint string

const (
	CheckpointPreflight Checkpoint = "preflight"
)

// keyKind tags the source Redis key shape so the ledger is honest about
// what parser/serialisation rule produced the entry (doc 15 §2).
type keyKind string

const (
	kindNode         keyKind = "node"
	kindWindow       keyKind = "window"
	kindIndex        keyKind = "index"
	kindBinding      keyKind = "binding"
	kindCredential   keyKind = "credential"
	kindProvider     keyKind = "provider"
	kindDedup        keyKind = "request_dedup"
	kindUnrecognised keyKind = "unrecognised"
)

// NodeTuple is the canonical representation of a URSM node identity.
type NodeTuple struct {
	TenantID     string
	CredentialID int
	RawModel     string
}

// WindowTuple is the canonical representation of a URSM window identity.
type WindowTuple struct {
	Bucket       string
	TenantID     string
	CredentialID int
	RawModel     string
}

// IndexTuple is the canonical representation of a URSM candidate index
// identity (doc 14 §2: the index has no production routing caller today,
// but its grammar is part of the public compatibility contract).
type IndexTuple struct {
	TenantID       string
	CanonicalModel string
	Profile        string
	Modality       string
}

// Options configures Preflight. Redis, Prefix, Owner and LedgerID are
// required; the other fields default to safe values (BatchSize=200,
// Now=time.Now, SchemaMode=legacy).
type Options struct {
	Redis      *redis.Client
	Prefix     string
	Owner      string
	LedgerID   string
	SchemaMode SchemaMode
	BatchSize  int
	Now        func() time.Time
	ScanLimit  int
}

// Ledger is the deterministic artifact of a Preflight run. Entries are
// sorted by SourceKey so Checksum is stable across runs.
type Ledger struct {
	Owner             string
	LedgerID          string
	SchemaMode        SchemaMode
	Prefix            string
	StartedAt         time.Time
	UpdatedAt         time.Time
	ScanRunID         string
	Checkpoint        Checkpoint
	Entries           []Entry
	PreflightChecksum string
}

// Entry is one observation against one source key. All fields are
// populated regardless of classification so the ledger can drive every
// later slice (copy, observe, cleanup, audit).
type Entry struct {
	SourceKey            string
	KeyKind              keyKind
	KeyType              string
	Schema               store.Schema
	Classification       Classification
	ClassificationReason string
	Status               EntryStatus
	NodeTuple            *NodeTuple
	WindowTuple          *WindowTuple
	IndexTuple           *IndexTuple
	TargetKey            string
	Generation           string
	FieldChecksum        string
	MemberCount          int64
	MemberChecksum       string
	PTTLMS               int64
	ReadAt               time.Time
	ScanBatch            int
	RunID                string
}

// Report is the per-run summary that drives the GO / NO-GO decision.
type Report struct {
	Ledger          Ledger
	Counts          map[Classification]int
	Decisions       []Decision
	MigrationGoable bool
	Reasons         []string
}

// Decision is a single source key's contribution to the verdict.
type Decision struct {
	SourceKey      string
	Classification Classification
	Reason         string
}

type runner struct {
	opts  Options
	now   func() time.Time
	runID string
}

// Preflight runs a deterministic, read-only inventory of the four
// authoritative Redis key patterns plus the request_dedup derived key
// (doc 15 §2) and returns the ledger and report. It performs no writes.
func Preflight(ctx context.Context, opts Options) (*Report, error) {
	if err := opts.applyDefaultsAndValidate(); err != nil {
		return nil, err
	}
	r := &runner{
		opts:  opts,
		now:   opts.Now,
		runID: newScanRunID(opts.Now),
	}
	if r.now == nil {
		r.now = time.Now
	}
	return r.runPreflight(ctx)
}

func (o *Options) applyDefaultsAndValidate() error {
	if o.Redis == nil {
		return errors.New("migration preflight: Redis client is required")
	}
	if strings.TrimSpace(o.Prefix) == "" {
		return errors.New("migration preflight: prefix is required")
	}
	if strings.TrimSpace(o.Owner) == "" {
		return errors.New("migration preflight: owner is required (doc 14 §0)")
	}
	if !isLedgerID(o.LedgerID) {
		return fmt.Errorf("migration preflight: ledger_id %q is not a one-shot UUIDv4-style identifier", o.LedgerID)
	}
	if o.BatchSize <= 0 {
		o.BatchSize = 200
	}
	if o.ScanLimit < 0 {
		return fmt.Errorf("migration preflight: scan limit %d must be non-negative", o.ScanLimit)
	}
	if o.SchemaMode == "" {
		o.SchemaMode = SchemaModeLegacy
	}
	switch o.SchemaMode {
	case SchemaModeLegacy, SchemaModeDual, SchemaModeCanonical:
	default:
		return fmt.Errorf("migration preflight: unknown schema mode %q", o.SchemaMode)
	}
	return nil
}

func isLedgerID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i, r := range s {
		switch i {
		case 8, 13, 18, 23:
			if r != '-' {
				return false
			}
		default:
			if !isHex(r) {
				return false
			}
		}
	}
	return true
}

func isHex(r rune) bool {
	switch {
	case r >= '0' && r <= '9':
		return true
	case r >= 'a' && r <= 'f':
		return true
	case r >= 'A' && r <= 'F':
		return true
	}
	return false
}

func newScanRunID(now func() time.Time) string {
	if now == nil {
		now = time.Now
	}
	t := now().UTC()
	var buf [16]byte
	binary.BigEndian.PutUint64(buf[0:8], uint64(t.UnixNano()))
	binary.BigEndian.PutUint64(buf[8:16], uint64(t.UnixNano()/17))
	sum := sha256.Sum256(buf[:])
	return hex.EncodeToString(sum[:8])
}

func (r *runner) runPreflight(ctx context.Context) (*Report, error) {
	startedAt := r.now().UTC()
	entries, err := r.scanAll(ctx)
	if err != nil {
		return nil, err
	}
	updatedAt := r.now().UTC()
	ledger := Ledger{
		Owner:      r.opts.Owner,
		LedgerID:   r.opts.LedgerID,
		SchemaMode: r.opts.SchemaMode,
		Prefix:     r.opts.Prefix,
		StartedAt:  startedAt,
		UpdatedAt:  updatedAt,
		ScanRunID:  r.runID,
		Checkpoint: CheckpointPreflight,
		Entries:    entries,
	}
	ledger.PreflightChecksum = checksumLedger(ledger)
	report := buildReport(ledger)
	return report, nil
}

// scanAll inventories every source-key pattern that doc 15 §2 mandates:
// node, window, candidate index, and the request_dedup derived key.
// binding/credential/provider keys are detected by prefix only and
// classified as excluded non-authoritative (doc 14 §4).
func (r *runner) scanAll(ctx context.Context) ([]Entry, error) {
	patterns := []struct {
		pattern string
		kind    keyKind
	}{
		{r.opts.Prefix + "node:*", kindNode},
		{r.opts.Prefix + "win:*", kindWindow},
		{r.opts.Prefix + "idx:model:*", kindIndex},
		{r.opts.Prefix + "binding:*", kindBinding},
		{r.opts.Prefix + "credential:*", kindCredential},
		{r.opts.Prefix + "provider:*", kindProvider},
		{r.opts.Prefix + "*request_dedup*", kindDedup},
	}
	out := make([]Entry, 0, r.opts.BatchSize)
	for _, p := range patterns {
		keys, err := r.scan(ctx, p.pattern)
		if err != nil {
			return nil, err
		}
		for _, k := range keys {
			out = append(out, r.classify(ctx, k, p.kind))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].SourceKey < out[j].SourceKey })
	return out, nil
}

func (r *runner) scan(ctx context.Context, pattern string) ([]string, error) {
	cursor := uint64(0)
	iter := 0
	var keys []string
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}
		page, next, err := r.opts.Redis.Scan(ctx, cursor, pattern, int64(r.opts.BatchSize)).Result()
		if err != nil {
			return nil, fmt.Errorf("migration preflight: scan %s: %w", pattern, err)
		}
		keys = append(keys, page...)
		cursor = next
		iter++
		if cursor == 0 {
			break
		}
		if r.opts.ScanLimit > 0 && iter >= r.opts.ScanLimit {
			return nil, fmt.Errorf("migration preflight: scan %s exceeded %d iterations without exhausting cursor", pattern, r.opts.ScanLimit)
		}
	}
	return keys, nil
}

func (r *runner) classify(ctx context.Context, key string, hint keyKind) Entry {
	readAt := r.now().UTC()
	pttl, err := r.opts.Redis.PTTL(ctx, key).Result()
	if err != nil {
		return newAmbiguous(key, hint, "PTTL error: "+err.Error(), readAt)
	}
	typeResult, err := r.opts.Redis.Type(ctx, key).Result()
	if err != nil {
		return newAmbiguous(key, hint, "TYPE error: "+err.Error(), readAt)
	}
	if typeResult == "none" {
		return Entry{
			SourceKey: key, KeyKind: hint, KeyType: "NONE",
			Classification: ClassExcludedNonAuthoritative, ClassificationReason: "key disappeared after SCAN",
			Status: StatusExcluded, PTTLMS: -2, ReadAt: readAt, RunID: r.runID,
		}
	}
	kind := detectKeyKind(r.opts.Prefix, key)
	if kind == kindBinding || kind == kindCredential || kind == kindProvider || kind == kindDedup {
		return Entry{
			SourceKey: key, KeyKind: kind, KeyType: typeResult,
			Classification: ClassExcludedNonAuthoritative, ClassificationReason: "not a migration source key",
			Status: StatusExcluded, PTTLMS: msFromPTTL(pttl), ReadAt: readAt, RunID: r.runID,
		}
	}
	switch kind {
	case kindNode:
		return r.classifyNode(ctx, key, typeResult, pttl, readAt)
	case kindWindow:
		return r.classifyWindow(ctx, key, typeResult, pttl, readAt)
	case kindIndex:
		return r.classifyIndex(ctx, key, typeResult, pttl, readAt)
	}
	return newAmbiguous(key, hint, "unrecognised key shape", readAt)
}

func newAmbiguous(key string, kind keyKind, reason string, readAt time.Time) Entry {
	return Entry{
		SourceKey: key, KeyKind: kind, KeyType: "UNKNOWN",
		Classification: ClassAmbiguous, ClassificationReason: reason,
		Status: StatusAmbiguous, PTTLMS: -2, ReadAt: readAt,
	}
}

func detectKeyKind(prefix, key string) keyKind {
	switch {
	case strings.HasPrefix(key, prefix+"node:k2:"):
		return kindNode
	case strings.HasPrefix(key, prefix+"win:k2:"):
		return kindWindow
	case strings.HasPrefix(key, prefix+"idx:model:k2:"):
		return kindIndex
	case strings.HasPrefix(key, prefix+"node:"):
		return kindNode
	case strings.HasPrefix(key, prefix+"win:"):
		return kindWindow
	case strings.HasPrefix(key, prefix+"idx:model:"):
		return kindIndex
	case strings.HasPrefix(key, prefix+"binding:"):
		return kindBinding
	case strings.HasPrefix(key, prefix+"credential:"):
		return kindCredential
	case strings.HasPrefix(key, prefix+"provider:"):
		return kindProvider
	case strings.Contains(key, "request_dedup"):
		return kindDedup
	}
	return kindUnrecognised
}

func (r *runner) classifyNode(ctx context.Context, key, typeResult string, pttl time.Duration, readAt time.Time) Entry {
	parsed, schema, ok := store.ParseNodeKeyCanonical(r.opts.Prefix, key)
	if !ok {
		return Entry{
			SourceKey: key, KeyKind: kindNode, KeyType: typeResult,
			Classification: ClassAmbiguous, ClassificationReason: "node key not parseable",
			Status: StatusAmbiguous, PTTLMS: msFromPTTL(pttl), ReadAt: readAt, RunID: r.runID,
		}
	}
	tuple := &NodeTuple{TenantID: parsed.TenantID, CredentialID: parsed.CredentialID, RawModel: parsed.RawModel}
	entry := Entry{
		SourceKey: key, KeyKind: kindNode, KeyType: typeResult, Schema: schema,
		Status: StatusClassified, NodeTuple: tuple,
		PTTLMS: msFromPTTL(pttl), ReadAt: readAt, RunID: r.runID,
	}
	switch schema {
	case store.SchemaK2:
		entry.Classification = ClassCanonicalPresent
		entry.ClassificationReason = "canonical k2 grammar; first production write cannot be overwritten"
		entry.TargetKey = key
		entry = readStructure(ctx, r.opts.Redis, entry)
	case store.SchemaLegacy:
		rebuilt := store.NodeKeyForTenant(r.opts.Prefix, parsed.TenantID, parsed.CredentialID, parsed.RawModel)
		if rebuilt != key {
			return entryWithVerdict(entry, ClassAmbiguous, StatusAmbiguous, "legacy tuple does not round-trip to source bytes")
		}
		if strings.Contains(parsed.RawModel, ":") {
			return entryWithVerdict(entry, ClassAmbiguous, StatusAmbiguous, "legacy source raw contains delimiter; tuple not provably unique")
		}
		// Cross-check with k2 representation: if a canonical target already
		// exists with the same logical tuple, emit conflict on mismatch and
		// canonical_present on match (doc 14 §4, doc 15 §2).
		canonical := store.NodeKeyCanonical(r.opts.Prefix, parsed.TenantID, parsed.CredentialID, parsed.RawModel)
		entry.Classification = ClassMigratable
		entry.ClassificationReason = "legacy tuple round-trips and is provably unique"
		entry.TargetKey = canonical
		entry = readStructure(ctx, r.opts.Redis, entry)
		if conflict, ok := r.compareCanonicalNode(ctx, canonical, entry); ok {
			return conflict
		}
	default:
		return entryWithVerdict(entry, ClassAmbiguous, StatusAmbiguous, "unknown schema")
	}
	return r.applySchemaModeGate(entry)
}

func (r *runner) classifyWindow(ctx context.Context, key, typeResult string, pttl time.Duration, readAt time.Time) Entry {
	parsed, ok := store.ParseWindowKeyCanonical(r.opts.Prefix, key)
	if !ok {
		// Window grammar permits only the k2 frozen form today; legacy
		// windows (when present in source Redis) are ambiguous because
		// ParseWindowKeyCanonical rejects the legacy layout outright.
		return Entry{
			SourceKey: key, KeyKind: kindWindow, KeyType: typeResult,
			Classification: ClassAmbiguous, ClassificationReason: "window key not in k2 grammar",
			Status: StatusAmbiguous, PTTLMS: msFromPTTL(pttl), ReadAt: readAt, RunID: r.runID,
		}
	}
	tuple := &WindowTuple{Bucket: parsed.Bucket, TenantID: parsed.TenantID, CredentialID: parsed.CredentialID, RawModel: parsed.RawModel}
	entry := Entry{
		SourceKey: key, KeyKind: kindWindow, KeyType: typeResult, Schema: store.SchemaK2,
		Status: StatusClassified, WindowTuple: tuple, TargetKey: key,
		Classification: ClassCanonicalPresent, ClassificationReason: "window key already in k2 grammar",
		PTTLMS: msFromPTTL(pttl), ReadAt: readAt, RunID: r.runID,
	}
	entry = readStructure(ctx, r.opts.Redis, entry)
	return r.applySchemaModeGate(entry)
}

func (r *runner) classifyIndex(ctx context.Context, key, typeResult string, pttl time.Duration, readAt time.Time) Entry {
	parsed, ok := store.ParseCandidateIndexKeyCanonical(r.opts.Prefix, key)
	if !ok {
		// Legacy candidate index keys are not yet covered by an authoritative
		// producer — doc 14 §2 freezes the grammar but says the candidate
		// index currently has no production routing caller. Treat legacy
		// indexes as excluded non-authoritative so the report stays clean;
		// the index package's K2 wiring will surface them later via L2.
		return Entry{
			SourceKey: key, KeyKind: kindIndex, KeyType: typeResult,
			Classification:       ClassExcludedNonAuthoritative,
			ClassificationReason: "candidate index in legacy grammar; rebuild required from authority (doc 14 §2)",
			Status:               StatusExcluded, PTTLMS: msFromPTTL(pttl), ReadAt: readAt, RunID: r.runID,
		}
	}
	tuple := &IndexTuple{TenantID: parsed.TenantID, CanonicalModel: parsed.CanonicalModel, Profile: parsed.Profile, Modality: parsed.Modality}
	entry := Entry{
		SourceKey: key, KeyKind: kindIndex, KeyType: typeResult, Schema: store.SchemaK2,
		Status: StatusClassified, IndexTuple: tuple, TargetKey: key,
		Classification: ClassCanonicalPresent, ClassificationReason: "candidate index key already in k2 grammar",
		PTTLMS: msFromPTTL(pttl), ReadAt: readAt, RunID: r.runID,
	}
	entry = readStructure(ctx, r.opts.Redis, entry)
	return r.applySchemaModeGate(entry)
}

// applySchemaModeGate enforces doc 14 §3: in canonical mode every non-k2
// source is excluded (legacy keys are out of scope). Other modes leave the
// verdict unchanged so preflight can drive both legacy and dual migrations.
func (r *runner) applySchemaModeGate(entry Entry) Entry {
	if entry.Schema == "" {
		return entry
	}
	if r.opts.SchemaMode == SchemaModeCanonical && entry.Schema != store.SchemaK2 {
		return entryWithVerdict(entry, ClassExcludedNonAuthoritative, StatusExcluded,
			"schema mode canonical; source not in k2 namespace")
	}
	return entry
}

// compareCanonicalNode checks whether a canonical target already exists
// for the legacy tuple, and emits a conflict verdict if its generation
// or field checksum disagrees with the legacy source (doc 14 §4,
// doc 15 §2). When no canonical target exists, the entry is unchanged.
func (r *runner) compareCanonicalNode(ctx context.Context, canonical string, legacy Entry) (Entry, bool) {
	exists, err := r.opts.Redis.Exists(ctx, canonical).Result()
	if err != nil || exists == 0 {
		return legacy, false
	}
	canonicalEntry := Entry{
		SourceKey: canonical, KeyKind: kindNode, RunID: r.runID,
		ReadAt: r.now().UTC(),
	}
	typeResult, err := r.opts.Redis.Type(ctx, canonical).Result()
	if err != nil {
		return legacy, false
	}
	canonicalEntry.KeyType = typeResult
	canonicalEntry = readStructure(ctx, r.opts.Redis, canonicalEntry)
	if canonicalEntry.FieldChecksum != "" && legacy.FieldChecksum != "" && canonicalEntry.FieldChecksum != legacy.FieldChecksum {
		return entryWithVerdict(legacy, ClassConflict, StatusConflict,
			"canonical target exists with diverging field checksum"), true
	}
	if canonicalEntry.Generation != "" && legacy.Generation != "" && canonicalEntry.Generation != legacy.Generation {
		return entryWithVerdict(legacy, ClassConflict, StatusConflict,
			"canonical target exists with diverging generation"), true
	}
	return legacy, false
}

func entryWithVerdict(entry Entry, class Classification, status EntryStatus, reason string) Entry {
	entry.Classification = class
	entry.ClassificationReason = reason
	entry.Status = status
	return entry
}

func readStructure(ctx context.Context, rdb *redis.Client, entry Entry) Entry {
	switch entry.KeyType {
	case "hash":
		all, err := rdb.HGetAll(ctx, entry.SourceKey).Result()
		if err != nil {
			entry.ClassificationReason += fmt.Sprintf("; HGETALL error: %v", err)
			return entry
		}
		entry.FieldChecksum = fieldChecksum(all)
		if gen, ok := all["generation"]; ok {
			entry.Generation = gen
		}
	case "zset":
		members, err := rdb.ZRangeWithScores(ctx, entry.SourceKey, 0, -1).Result()
		if err != nil {
			entry.ClassificationReason += fmt.Sprintf("; ZRANGE error: %v", err)
			return entry
		}
		entry.MemberCount = int64(len(members))
		entry.MemberChecksum = memberChecksum(members)
	}
	return entry
}

func fieldChecksum(fields map[string]string) string {
	names := make([]string, 0, len(fields))
	for name := range fields {
		names = append(names, name)
	}
	sort.Strings(names)
	h := sha256.New()
	for _, name := range names {
		h.Write([]byte(name))
		h.Write([]byte{0})
		h.Write([]byte(fields[name]))
	}
	return hex.EncodeToString(h.Sum(nil))
}

func memberChecksum(members []redis.Z) string {
	sorted := make([]redis.Z, len(members))
	copy(sorted, members)
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].Score != sorted[j].Score {
			return sorted[i].Score < sorted[j].Score
		}
		return sorted[i].Member.(string) < sorted[j].Member.(string)
	})
	h := sha256.New()
	for _, m := range sorted {
		scoreStr := fmt.Sprintf("%g", m.Score)
		h.Write([]byte(m.Member.(string)))
		h.Write([]byte{0})
		h.Write([]byte(scoreStr))
	}
	return hex.EncodeToString(h.Sum(nil))
}

func msFromPTTL(pttl time.Duration) int64 {
	switch {
	case pttl == -2*time.Second:
		return -2
	case pttl == -1*time.Second:
		return -1
	}
	return pttl.Milliseconds()
}

func checksumLedger(ledger Ledger) string {
	h := sha256.New()
	write := func(label, value string) {
		h.Write([]byte(label))
		h.Write([]byte{0})
		h.Write([]byte(value))
		h.Write([]byte{0})
	}
	write("owner", ledger.Owner)
	write("ledger_id", ledger.LedgerID)
	write("schema_mode", string(ledger.SchemaMode))
	write("prefix", ledger.Prefix)
	write("scan_run_id", ledger.ScanRunID)
	write("checkpoint", string(ledger.Checkpoint))
	write("started_at", ledger.StartedAt.UTC().Format(time.RFC3339Nano))
	write("updated_at", ledger.UpdatedAt.UTC().Format(time.RFC3339Nano))
	for _, e := range ledger.Entries {
		write("entry.source", e.SourceKey)
		write("entry.kind", string(e.KeyKind))
		write("entry.type", e.KeyType)
		write("entry.schema", string(e.Schema))
		write("entry.class", string(e.Classification))
		write("entry.reason", e.ClassificationReason)
		write("entry.status", string(e.Status))
		if e.NodeTuple != nil {
			write("entry.tenant", e.NodeTuple.TenantID)
			write("entry.cred", fmt.Sprintf("%d", e.NodeTuple.CredentialID))
			write("entry.raw", e.NodeTuple.RawModel)
		} else {
			write("entry.tenant", "")
			write("entry.cred", "")
			write("entry.raw", "")
		}
		if e.WindowTuple != nil {
			write("entry.bucket", e.WindowTuple.Bucket)
		} else {
			write("entry.bucket", "")
		}
		if e.IndexTuple != nil {
			write("entry.canonical_model", e.IndexTuple.CanonicalModel)
			write("entry.profile", e.IndexTuple.Profile)
			write("entry.modality", e.IndexTuple.Modality)
		} else {
			write("entry.canonical_model", "")
			write("entry.profile", "")
			write("entry.modality", "")
		}
		write("entry.target", e.TargetKey)
		write("entry.generation", e.Generation)
		write("entry.field_checksum", e.FieldChecksum)
		write("entry.member_count", fmt.Sprintf("%d", e.MemberCount))
		write("entry.member_checksum", e.MemberChecksum)
		write("entry.pttl", fmt.Sprintf("%d", e.PTTLMS))
		write("entry.read_at", e.ReadAt.UTC().Format(time.RFC3339Nano))
		write("entry.run_id", e.RunID)
	}
	return hex.EncodeToString(h.Sum(nil))
}

func buildReport(ledger Ledger) *Report {
	counts := map[Classification]int{}
	decisions := make([]Decision, 0, len(ledger.Entries))
	reasons := []string{}
	for _, e := range ledger.Entries {
		counts[e.Classification]++
		decisions = append(decisions, Decision{SourceKey: e.SourceKey, Classification: e.Classification, Reason: e.ClassificationReason})
		if e.Classification == ClassAmbiguous || e.Classification == ClassConflict {
			reasons = append(reasons, fmt.Sprintf("%s: %s (%s)", e.SourceKey, e.Classification, e.ClassificationReason))
		}
	}
	goable := counts[ClassAmbiguous] == 0 && counts[ClassConflict] == 0
	return &Report{
		Ledger:          ledger,
		Counts:          counts,
		Decisions:       decisions,
		MigrationGoable: goable,
		Reasons:         reasons,
	}
}
