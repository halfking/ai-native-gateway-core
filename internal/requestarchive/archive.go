// Package requestarchive tracks in-flight requests in memory and archives
// their request facts on the local disk, so a process restart can recover the
// set of requests whose facts were completed but never acknowledged by the
// database.
//
// Ownership boundaries (why this package exists beside requestfact):
// requestfact owns the versioned wire contract and codec; this package owns
// local lifecycle state — atomic file placement, stage transitions, crash
// recovery, and quarantine. The package never talks to the database (the
// caller performs the persist and only then advances the archive), never
// encrypts (encryption belongs to the durable_llm_tasks domain), and never
// touches the network.
package requestarchive

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/kaixuan/llm-gateway-go/internal/requestfact"
)

// ArchiveFormatVersionV1 versions this package's wrapper document. It is
// deliberately named and versioned independently of requestfact's four
// contract versions: the wrapper can gain meta fields without touching the
// fact wire format, and a future fact codec bump cannot silently reinterpret
// old archives.
const ArchiveFormatVersionV1 = 1

const (
	fileSuffix = ".json"
	tempSuffix = ".tmp"

	// maxFilenameIDLen leaves room for the ".json" suffix within the common
	// 255-byte filename limit so stage paths never fail at rename time.
	maxFilenameIDLen = 200

	quarantineDirName = "quarantine"
)

// Stage names the archive directory a document currently occupies. The
// directory position is the authoritative lifecycle state; the fact envelope's
// archive.state is rewritten to match it on every write so a document stays
// self-describing wherever it is found.
type Stage string

const (
	// StageActive holds documents for requests that have not been promoted
	// to terminal yet.
	StageActive Stage = "active"
	// StageTerminalPending holds terminal facts the database has not
	// acknowledged.
	StageTerminalPending Stage = "terminal_pending"
	// StageCleanupPending holds documents the database acknowledged but that
	// failed to delete; startup retries the deletion.
	StageCleanupPending Stage = "cleanup_pending"
)

var (
	// ErrMetaMismatch signals that a fact and its metadata identify
	// different requests; writing that pair would corrupt recovery.
	ErrMetaMismatch = errors.New("requestarchive: meta does not match fact identity")
	// ErrAlreadyTerminal guards the forward-only lifecycle: an active write
	// must never shadow an existing terminal document.
	ErrAlreadyTerminal = errors.New("requestarchive: request already promoted to terminal_pending")
	// ErrUnsupportedArchiveFormat is the wrapper-level analogue of
	// requestfact.ErrUnsupportedVersion.
	ErrUnsupportedArchiveFormat = errors.New("requestarchive: unsupported archive format version")
)

// ArchiveMeta is the registry metadata stored beside the fact. Recovery needs
// it independently of the fact body: tenant and session routing for
// re-persistence, and timestamps for expiry decisions.
type ArchiveMeta struct {
	RequestID    string    `json:"request_id"`
	TenantID     string    `json:"tenant_id"`
	SessionID    string    `json:"session_id,omitempty"`
	Endpoint     string    `json:"endpoint,omitempty"`
	RegisteredAt time.Time `json:"registered_at,omitempty"`
	UpdatedAt    time.Time `json:"updated_at,omitempty"`
}

// archiveDocument is the on-disk unit. Fact holds the verbatim bytes produced
// by requestfact.Encode, so the fact's payload_sha256 keeps covering exactly
// the encoded document and the fact contract stays owned by requestfact.
type archiveDocument struct {
	ArchiveVersion int             `json:"archive_version"`
	Fact           json.RawMessage `json:"fact"`
	Meta           ArchiveMeta     `json:"meta"`
}

// ArchivedRequest is one entry returned by recovery: where it was found, its
// registry metadata, and the decoded fact ready for database persistence.
type ArchivedRequest struct {
	RequestID string
	Stage     Stage
	Meta      ArchiveMeta
	Fact      requestfact.CanonicalRequestFact
}

// ArchiveConfig parameterises NewLocalRequestArchive.
type ArchiveConfig struct {
	// Root is the archive root; the four stage directories are created
	// beneath it.
	Root string
	// Logger receives non-fatal diagnostics: directory fsync failures,
	// quarantine moves, cleanup retry failures. Defaults to slog.Default().
	Logger *slog.Logger
}

// LocalRequestArchive stores one JSON document per request under
// <root>/<stage>/<request_id>.json. Every stage transition is a
// write-then-remove sequence ordered so a crash at any point leaves the
// document somewhere recovery can act on it:
//
//   - active → terminal_pending: write the terminal document first, remove the
//     active document second; a crash between the two leaves both files and
//     RecoverPending resolves the duplicate in favour of terminal_pending;
//   - terminal_pending → deleted: rename into cleanup_pending first, delete
//     second; a crash between the two leaves the file in the retry stage
//     rather than losing it or acknowledging a deletion that never happened.
type LocalRequestArchive struct {
	root   string
	logger *slog.Logger
	locks  keyedLocks
}

// NewLocalRequestArchive opens — creating if needed — the archive rooted at
// cfg.Root.
func NewLocalRequestArchive(cfg ArchiveConfig) (*LocalRequestArchive, error) {
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	archive := &LocalRequestArchive{root: cfg.Root, logger: cfg.Logger}
	for _, dir := range []string{
		archive.stageDir(StageActive),
		archive.stageDir(StageTerminalPending),
		archive.stageDir(StageCleanupPending),
		archive.quarantineDir(),
	} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("requestarchive: create stage dir: %w", err)
		}
	}
	return archive, nil
}

// Root exposes the archive root for tooling and tests.
func (a *LocalRequestArchive) Root() string { return a.root }

// ArchiveActive writes the fact into the active stage; repeated calls update
// the document in place (last write wins atomically).
//
// Contract note: requestfact's V1 payload already requires terminal lifecycle
// fields, so today this lands a built-but-unpromoted fact rather than a truly
// mid-flight request body; the active stage is the pre-promotion landing zone
// until the fact contract grows an in-flight shape.
//
// Writing active when a terminal_pending document already exists is rejected:
// the lifecycle only moves forward, and shadowing a terminal document would
// make a request that may already be DB-acknowledged look unfinished again.
func (a *LocalRequestArchive) ArchiveActive(envelope requestfact.RequestArchiveEnvelope, meta ArchiveMeta) error {
	if err := checkPair(meta, envelope); err != nil {
		return err
	}
	a.locks.Lock(meta.RequestID)
	defer a.locks.Unlock(meta.RequestID)

	if _, err := os.Stat(a.stagePath(StageTerminalPending, meta.RequestID)); err == nil {
		return fmt.Errorf("%w: %s", ErrAlreadyTerminal, meta.RequestID)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("requestarchive: stat terminal document: %w", err)
	}
	envelope.Archive.State = requestfact.ArchiveStateActive
	return a.writeStage(StageActive, envelope, meta)
}

// PromoteTerminal installs the terminal fact and clears the active document.
// The terminal document is written before the active one is removed so the
// request can never be lost by the promotion itself; the crash window between
// the two steps is closed by RecoverPending's terminal-wins dedupe. Repeated
// promotion overwrites the previous terminal document — callers retrying with
// a bumped Attempts counter rely on last-write-wins.
func (a *LocalRequestArchive) PromoteTerminal(envelope requestfact.RequestArchiveEnvelope, meta ArchiveMeta) error {
	if err := checkPair(meta, envelope); err != nil {
		return err
	}
	a.locks.Lock(meta.RequestID)
	defer a.locks.Unlock(meta.RequestID)

	envelope.Archive.State = requestfact.ArchiveStateTerminalPending
	if err := a.writeStage(StageTerminalPending, envelope, meta); err != nil {
		return err
	}
	// ErrNotExist is fine: a retried promotion finds no active document left.
	if err := os.Remove(a.stagePath(StageActive, meta.RequestID)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("requestarchive: remove active document: %w", err)
	}
	return nil
}

// ConfirmPersisted disposes of the request's documents after the caller has
// observed a successful database acknowledgement. The archive never contacts
// the database, so ordering with the commit is entirely the caller's job:
// only call this once the acknowledgement is durable, otherwise a crash could
// lose the only copy of an unacknowledged fact.
//
// The terminal document is renamed into cleanup_pending before deletion, so a
// crash between rename and delete leaves a marker that startup retries rather
// than a deletion that silently never happened. Any leftover active document
// from the promotion crash window is removed as well. The call is idempotent:
// confirming an already-cleaned request succeeds.
func (a *LocalRequestArchive) ConfirmPersisted(requestID string) error {
	if err := checkFilenameID(requestID); err != nil {
		return err
	}
	a.locks.Lock(requestID)
	defer a.locks.Unlock(requestID)

	terminalPath := a.stagePath(StageTerminalPending, requestID)
	cleanupPath := a.stagePath(StageCleanupPending, requestID)
	if _, err := os.Stat(terminalPath); err == nil {
		// Drop a stale cleanup copy from an earlier interrupted confirm;
		// rename does not replace an existing destination on every platform.
		if err := os.Remove(cleanupPath); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("requestarchive: discard stale cleanup document: %w", err)
		}
		if err := os.Rename(terminalPath, cleanupPath); err != nil {
			// The document stays in terminal_pending: recoverable, no data
			// loss, and the caller can retry the confirm.
			return fmt.Errorf("requestarchive: move to cleanup_pending: %w", err)
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("requestarchive: stat terminal document: %w", err)
	}

	if err := os.Remove(cleanupPath); err != nil && !errors.Is(err, fs.ErrNotExist) {
		// The document remains in cleanup_pending; RecoverPending retries
		// the deletion on the next startup.
		return fmt.Errorf("requestarchive: delete confirmed document: %w", err)
	}
	if err := os.Remove(a.stagePath(StageActive, requestID)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("requestarchive: remove leftover active document: %w", err)
	}
	return nil
}

// RecoverPending is the startup scan. It first retries cleanup of documents
// whose acknowledgement succeeded but whose deletion failed, deletes stale
// tmp files left by interrupted atomic writes, then returns every document
// still waiting on the database — terminal_pending and active alike.
//
// A document that fails to parse, fails requestfact.Decode, disagrees with its
// filename, or uses an unknown archive format is moved into quarantine/ and
// the scan continues: one corrupt file must not block recovery of the rest.
//
// The scan is not synchronised with concurrent writers; call it during
// startup, before the archive serves traffic.
func (a *LocalRequestArchive) RecoverPending() ([]ArchivedRequest, error) {
	a.retryCleanup()
	a.removeStaleTempFiles()

	// terminal_pending is scanned first so a request present in both stages
	// (the promotion crash window) resolves to its terminal document; the
	// superseded active copy is left for ConfirmPersisted to delete, keeping
	// the scan itself free of mutation beyond quarantine and tmp cleanup.
	recovered := make(map[string]ArchivedRequest)
	for _, stage := range []Stage{StageTerminalPending, StageActive} {
		for _, requestID := range a.listStage(stage) {
			if _, seen := recovered[requestID]; seen {
				continue
			}
			archived, err := a.readDocument(stage, requestID)
			if err != nil {
				a.quarantine(stage, requestID, err)
				continue
			}
			recovered[requestID] = archived
		}
	}

	result := make([]ArchivedRequest, 0, len(recovered))
	for _, archived := range recovered {
		result = append(result, archived)
	}
	// Deterministic order keeps repeat scans comparable and tests stable.
	sort.Slice(result, func(i, j int) bool { return result[i].RequestID < result[j].RequestID })
	return result, nil
}

// listStage returns the request ids present in a stage, sorted. Only *.json
// documents are considered; tmp leftovers are removeStaleTempFiles' business.
// A listing failure is logged and treated as an empty stage: the scan must
// keep making progress for the remaining stages.
func (a *LocalRequestArchive) listStage(stage Stage) []string {
	entries, err := os.ReadDir(a.stageDir(stage))
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			a.logger.Error("requestarchive: list stage dir failed", "stage", stage, "error", err)
		}
		return nil
	}
	ids := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), fileSuffix) {
			continue
		}
		ids = append(ids, strings.TrimSuffix(entry.Name(), fileSuffix))
	}
	sort.Strings(ids)
	return ids
}

// readDocument loads and fully validates one archived document: the wrapper
// version, the fact codec (requestfact.Decode verifies the payload hash), and
// the agreement between filename, meta, and fact identity. Any disagreement
// means the file was misplaced or tampered with and must not be recovered
// blindly.
func (a *LocalRequestArchive) readDocument(stage Stage, requestID string) (ArchivedRequest, error) {
	body, err := os.ReadFile(a.stagePath(stage, requestID))
	if err != nil {
		return ArchivedRequest{}, fmt.Errorf("read document: %w", err)
	}
	var document archiveDocument
	if err := json.Unmarshal(body, &document); err != nil {
		return ArchivedRequest{}, fmt.Errorf("decode archive wrapper: %w", err)
	}
	if document.ArchiveVersion != ArchiveFormatVersionV1 {
		return ArchivedRequest{}, fmt.Errorf("%w: %d", ErrUnsupportedArchiveFormat, document.ArchiveVersion)
	}
	envelope, err := requestfact.Decode(document.Fact)
	if err != nil {
		return ArchivedRequest{}, fmt.Errorf("decode fact: %w", err)
	}
	if document.Meta.RequestID != requestID || envelope.Payload.Identity.RequestID != requestID {
		return ArchivedRequest{}, ErrMetaMismatch
	}
	if envelope.Archive.State != requestfact.ArchiveState(stage) {
		return ArchivedRequest{}, fmt.Errorf("fact archive state %q does not match stage %q", envelope.Archive.State, stage)
	}
	return ArchivedRequest{
		RequestID: requestID,
		Stage:     stage,
		Meta:      document.Meta,
		Fact:      envelope.Payload,
	}, nil
}

// quarantine moves an unreadable document out of the scan path instead of
// deleting it: the bytes may still be manually repairable, and keeping them
// makes the failure auditable. If the move itself fails the file stays in
// place and the next scan retries; returning the error would abort recovery
// of healthy documents, which is the worse trade.
func (a *LocalRequestArchive) quarantine(stage Stage, requestID string, reason error) {
	source := a.stagePath(stage, requestID)
	target := filepath.Join(a.quarantineDir(), filepath.Base(source))
	if err := os.Remove(target); err != nil && !errors.Is(err, fs.ErrNotExist) {
		a.logger.Error("requestarchive: discard previous quarantine copy failed", "path", target, "error", err)
	}
	if err := os.Rename(source, target); err != nil {
		a.logger.Error("requestarchive: quarantine move failed", "from", source, "to", target, "error", err)
		return
	}
	a.logger.Warn("requestarchive: quarantined unreadable document", "from", source, "to", target, "reason", reason.Error())
}

// retryCleanup deletes documents whose database acknowledgement succeeded but
// whose earlier deletion failed. Failures are logged and the file is kept for
// the next startup: cleanup is best-effort because the database already owns
// the authoritative copy — the archive only owes eventual deletion.
func (a *LocalRequestArchive) retryCleanup() {
	for _, requestID := range a.listStage(StageCleanupPending) {
		if err := os.Remove(a.stagePath(StageCleanupPending, requestID)); err != nil && !errors.Is(err, fs.ErrNotExist) {
			a.logger.Error("requestarchive: cleanup retry failed", "request_id", requestID, "error", err)
		}
	}
}

// removeStaleTempFiles deletes *.tmp leftovers from writes interrupted by a
// crash. They are inert by construction — the atomic write installs the final
// name only via rename — so deleting them can never lose a document.
func (a *LocalRequestArchive) removeStaleTempFiles() {
	dirs := []string{
		a.stageDir(StageActive),
		a.stageDir(StageTerminalPending),
		a.stageDir(StageCleanupPending),
		a.quarantineDir(),
	}
	for _, dir := range dirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue // a missing dir holds no leftovers; stage scans tolerate it
		}
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), tempSuffix) {
				continue
			}
			path := filepath.Join(dir, entry.Name())
			if err := os.Remove(path); err != nil {
				a.logger.Error("requestarchive: remove stale tmp file failed", "path", path, "error", err)
			}
		}
	}
}

// writeStage encodes the envelope through the requestfact codec, wraps it with
// the archive version and meta, and installs it atomically at the stage path.
func (a *LocalRequestArchive) writeStage(stage Stage, envelope requestfact.RequestArchiveEnvelope, meta ArchiveMeta) error {
	encoded, err := requestfact.Encode(envelope)
	if err != nil {
		return fmt.Errorf("requestarchive: encode fact: %w", err)
	}
	body, err := json.Marshal(archiveDocument{
		ArchiveVersion: ArchiveFormatVersionV1,
		Fact:           encoded,
		Meta:           meta,
	})
	if err != nil {
		return fmt.Errorf("requestarchive: encode archive document: %w", err)
	}
	return a.writeFileAtomic(a.stagePath(stage, meta.RequestID), body)
}

// writeFileAtomic durably installs body at path: it creates a uniquely named
// tmp file in the same directory (same filesystem, so the rename cannot
// tear), fsyncs the file, then renames it over the final name. A crash at any
// point therefore leaves either the previous complete document or garbage
// under a *.tmp name — never a truncated document at the final name.
func (a *LocalRequestArchive) writeFileAtomic(path string, body []byte) error {
	dir, name := filepath.Split(path)
	tmp, err := os.CreateTemp(dir, name+".*"+tempSuffix)
	if err != nil {
		return fmt.Errorf("requestarchive: create tmp file: %w", err)
	}
	tmpPath := tmp.Name()
	defer func() {
		// No-op after a successful rename; otherwise the write aborted and
		// the partial tmp file is pure garbage.
		if _, statErr := os.Stat(tmpPath); statErr == nil {
			if removeErr := os.Remove(tmpPath); removeErr != nil {
				a.logger.Error("requestarchive: remove aborted tmp file failed", "path", tmpPath, "error", removeErr)
			}
		}
	}()

	if _, err := tmp.Write(body); err != nil {
		tmp.Close()
		return fmt.Errorf("requestarchive: write tmp file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("requestarchive: fsync tmp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("requestarchive: close tmp file: %w", err)
	}
	// CreateTemp mandates 0600; align with the repository's 0644 convention so
	// operators can inspect archived documents.
	if err := os.Chmod(tmpPath, 0o644); err != nil {
		return fmt.Errorf("requestarchive: chmod tmp file: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("requestarchive: install document: %w", err)
	}
	// Best-effort: on filesystems where directory fsync fails, the worst
	// outcome is a document reappearing in an earlier lifecycle stage after a
	// crash, which recovery already treats as pending.
	a.fsyncDir(dir)
	return nil
}

// fsyncDir makes a preceding rename durable. Failure is logged, never fatal,
// for the reason given at the writeFileAtomic call site.
func (a *LocalRequestArchive) fsyncDir(dir string) {
	handle, err := os.Open(dir)
	if err != nil {
		a.logger.Warn("requestarchive: open dir for fsync failed", "dir", dir, "error", err)
		return
	}
	defer handle.Close()
	if err := handle.Sync(); err != nil {
		a.logger.Warn("requestarchive: dir fsync failed", "dir", dir, "error", err)
	}
}

// checkPair enforces the pairing invariant before anything touches the
// filesystem: meta and fact must identify the same, filename-safe request,
// otherwise the document could never be recovered under its id.
func checkPair(meta ArchiveMeta, envelope requestfact.RequestArchiveEnvelope) error {
	if err := checkFilenameID(meta.RequestID); err != nil {
		return err
	}
	if meta.RequestID != envelope.Payload.Identity.RequestID {
		return fmt.Errorf("%w: meta=%s fact=%s", ErrMetaMismatch, meta.RequestID, envelope.Payload.Identity.RequestID)
	}
	return nil
}

// checkFilenameID rejects ids that would escape a stage directory or address
// special paths once embedded in a filename. Validating at this boundary
// keeps every later path join free of sanitisation concerns.
func checkFilenameID(requestID string) error {
	if requestID == "" {
		return ErrMissingRequestID
	}
	if requestID == "." || requestID == ".." || len(requestID) > maxFilenameIDLen ||
		strings.ContainsAny(requestID, `/\`) || strings.ContainsRune(requestID, 0) {
		return fmt.Errorf("%w: %q", ErrInvalidRequestID, requestID)
	}
	for _, r := range requestID {
		if r < 0x20 || r == 0x7f {
			return fmt.Errorf("%w: %q", ErrInvalidRequestID, requestID)
		}
	}
	return nil
}

func (a *LocalRequestArchive) stageDir(stage Stage) string {
	return filepath.Join(a.root, string(stage))
}

func (a *LocalRequestArchive) stagePath(stage Stage, requestID string) string {
	return filepath.Join(a.stageDir(stage), requestID+fileSuffix)
}

func (a *LocalRequestArchive) quarantineDir() string {
	return filepath.Join(a.root, quarantineDirName)
}

// keyedLocks serialises operations per request id while unrelated requests
// proceed in parallel — a slow fact write for one request must not head-of-
// line block the rest. Entries are refcounted and deleted when the last
// holder releases, so a long-lived process does not accumulate one mutex per
// historical request id. The table mutex and an entry mutex are never held
// simultaneously, so no lock-order cycle exists.
type keyedLocks struct {
	mu    sync.Mutex
	locks map[string]*keyedLockEntry
}

type keyedLockEntry struct {
	mu   sync.Mutex
	refs int
}

func (k *keyedLocks) Lock(id string) {
	k.mu.Lock()
	if k.locks == nil {
		k.locks = make(map[string]*keyedLockEntry)
	}
	entry, ok := k.locks[id]
	if !ok {
		entry = &keyedLockEntry{}
		k.locks[id] = entry
	}
	entry.refs++
	k.mu.Unlock()
	entry.mu.Lock()
}

func (k *keyedLocks) Unlock(id string) {
	k.mu.Lock()
	entry, ok := k.locks[id]
	if !ok {
		k.mu.Unlock()
		panic("requestarchive: unlock of id that is not locked: " + id)
	}
	entry.refs--
	if entry.refs == 0 {
		// Safe to delete: refs only reaches zero when no holder and no
		// waiter remain, because both increment refs before touching the
		// entry mutex.
		delete(k.locks, id)
	}
	k.mu.Unlock()
	entry.mu.Unlock()
}
