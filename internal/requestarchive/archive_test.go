package requestarchive

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/internal/requestfact"
)

func newTestArchive(t *testing.T) *LocalRequestArchive {
	t.Helper()
	archive, err := NewLocalRequestArchive(ArchiveConfig{
		Root:   t.TempDir(),
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatalf("NewLocalRequestArchive() error = %v", err)
	}
	return archive
}

func testFact(tenantID, requestID string) requestfact.CanonicalRequestFact {
	createdAt := time.Date(2026, time.August, 27, 9, 0, 0, 0, time.UTC)
	requestBody := json.RawMessage(`{"model":"gpt-test","messages":[{"role":"user","content":"hi"}]}`)
	return requestfact.CanonicalRequestFact{
		Identity: requestfact.Identity{
			TenantID:  tenantID,
			RequestID: requestID,
			SessionID: "session-1",
		},
		Lifecycle: requestfact.Lifecycle{
			Status:      "completed",
			CreatedAt:   createdAt,
			StartedAt:   createdAt.Add(time.Second),
			CompletedAt: createdAt.Add(2 * time.Second),
		},
		Routing: requestfact.Routing{ClientProtocol: "openai-chat", ClientModel: "gpt-test"},
		Request: requestfact.ContentDocument{
			RawBody:     requestBody,
			CanonicalIR: json.RawMessage(`{"model":"gpt-test","messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`),
			Protocol:    "openai-chat",
		},
		Integrity: requestfact.Integrity{RequestBodySHA256: requestfact.BodySHA256(requestBody)},
	}
}

func testEnvelope(t *testing.T, state requestfact.ArchiveState, fact requestfact.CanonicalRequestFact) requestfact.RequestArchiveEnvelope {
	t.Helper()
	envelope := requestfact.NewEnvelope(time.Date(2026, time.August, 27, 9, 5, 0, 0, time.UTC), state, fact)
	if _, err := requestfact.Encode(envelope); err != nil {
		t.Fatalf("fixture envelope does not encode: %v", err)
	}
	return envelope
}

func testMeta(requestID string) ArchiveMeta {
	return ArchiveMeta{
		RequestID:    requestID,
		TenantID:     "tenant-a",
		SessionID:    "session-1",
		Endpoint:     "/v1/chat/completions",
		RegisteredAt: time.Date(2026, time.August, 27, 9, 0, 1, 0, time.UTC),
		UpdatedAt:    time.Date(2026, time.August, 27, 9, 4, 0, 0, time.UTC),
	}
}

func requireState(t *testing.T, path string, wantExists bool) {
	t.Helper()
	_, err := os.Stat(path)
	switch {
	case wantExists && err != nil:
		t.Fatalf("expected %s to exist, got %v", path, err)
	case !wantExists && !errors.Is(err, fs.ErrNotExist):
		t.Fatalf("expected %s to be absent, got %v", path, err)
	}
}

func countJSONFiles(t *testing.T, dir string) int {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir(%s) error = %v", dir, err)
	}
	count := 0
	for _, entry := range entries {
		if !entry.IsDir() && filepath.Ext(entry.Name()) == ".json" {
			count++
		}
	}
	return count
}

// writeWrapper installs a fixture wrapper verbatim (bypassing the atomic
// writer) so tests can reproduce files a crash or a foreign writer produced.
func writeWrapper(t *testing.T, path string, document archiveDocument) {
	t.Helper()
	body, err := json.Marshal(document)
	if err != nil {
		t.Fatalf("marshal wrapper: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir fixture dir: %v", err)
	}
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatalf("write fixture %s: %v", path, err)
	}
}

func encodeFact(t *testing.T, envelope requestfact.RequestArchiveEnvelope) json.RawMessage {
	t.Helper()
	encoded, err := requestfact.Encode(envelope)
	if err != nil {
		t.Fatalf("Encode() error = %v", err)
	}
	return encoded
}

func TestArchiveLifecycleRoundTrip(t *testing.T) {
	t.Parallel()
	archive := newTestArchive(t)
	const requestID = "req-0001"
	fact := testFact("tenant-a", requestID)
	meta := testMeta(requestID)

	if err := archive.ArchiveActive(testEnvelope(t, requestfact.ArchiveStateActive, fact), meta); err != nil {
		t.Fatalf("ArchiveActive() error = %v", err)
	}
	requireState(t, archive.stagePath(StageActive, requestID), true)

	recovered, err := archive.RecoverPending()
	if err != nil {
		t.Fatalf("RecoverPending() error = %v", err)
	}
	if len(recovered) != 1 || recovered[0].Stage != StageActive || recovered[0].RequestID != requestID {
		t.Fatalf("RecoverPending() = %+v, want single active %s", recovered, requestID)
	}
	if got := recovered[0].Fact.Identity.TenantID; got != "tenant-a" {
		t.Fatalf("recovered tenant = %q, want %q", got, "tenant-a")
	}
	if !reflect.DeepEqual(recovered[0].Meta, meta) {
		t.Fatalf("recovered meta = %+v, want %+v", recovered[0].Meta, meta)
	}
	if string(recovered[0].Fact.Request.RawBody) != string(fact.Request.RawBody) {
		t.Fatalf("recovered raw body = %s, want %s", recovered[0].Fact.Request.RawBody, fact.Request.RawBody)
	}

	// The envelope's archive state must follow the directory it lives in, so
	// a recovered document is self-describing.
	var stored archiveDocument
	body, err := os.ReadFile(archive.stagePath(StageActive, requestID))
	if err != nil {
		t.Fatalf("read stored document: %v", err)
	}
	if err := json.Unmarshal(body, &stored); err != nil {
		t.Fatalf("decode stored document: %v", err)
	}
	decoded, err := requestfact.Decode(stored.Fact)
	if err != nil {
		t.Fatalf("Decode(stored fact) error = %v", err)
	}
	if decoded.Archive.State != requestfact.ArchiveStateActive {
		t.Fatalf("stored archive state = %q, want active", decoded.Archive.State)
	}

	if err := archive.PromoteTerminal(testEnvelope(t, requestfact.ArchiveStateTerminalPending, fact), meta); err != nil {
		t.Fatalf("PromoteTerminal() error = %v", err)
	}
	requireState(t, archive.stagePath(StageTerminalPending, requestID), true)
	requireState(t, archive.stagePath(StageActive, requestID), false)

	recovered, err = archive.RecoverPending()
	if err != nil {
		t.Fatalf("RecoverPending() after promote error = %v", err)
	}
	if len(recovered) != 1 || recovered[0].Stage != StageTerminalPending {
		t.Fatalf("RecoverPending() after promote = %+v, want single terminal_pending", recovered)
	}

	if err := archive.ConfirmPersisted(requestID); err != nil {
		t.Fatalf("ConfirmPersisted() error = %v", err)
	}
	requireState(t, archive.stagePath(StageTerminalPending, requestID), false)
	requireState(t, archive.stagePath(StageCleanupPending, requestID), false)

	recovered, err = archive.RecoverPending()
	if err != nil {
		t.Fatalf("RecoverPending() after confirm error = %v", err)
	}
	if len(recovered) != 0 {
		t.Fatalf("RecoverPending() after confirm = %+v, want empty", recovered)
	}
}

func TestArchiveRejectsMismatchedMetaAndInvalidIDs(t *testing.T) {
	t.Parallel()
	archive := newTestArchive(t)
	fact := testFact("tenant-a", "req-good")
	envelope := testEnvelope(t, requestfact.ArchiveStateActive, fact)

	// A meta/fact identity disagreement must be refused before anything
	// reaches the filesystem, otherwise the document could not be recovered
	// under either id.
	mismatched := testMeta("req-other")
	if err := archive.ArchiveActive(envelope, mismatched); !errors.Is(err, ErrMetaMismatch) {
		t.Fatalf("ArchiveActive(mismatched meta) error = %v, want ErrMetaMismatch", err)
	}
	if err := archive.PromoteTerminal(envelope, mismatched); !errors.Is(err, ErrMetaMismatch) {
		t.Fatalf("PromoteTerminal(mismatched meta) error = %v, want ErrMetaMismatch", err)
	}

	for _, invalidID := range []string{"", "../escape", "a/b", "..", strings.Repeat("x", 201)} {
		// Rejection happens on the meta id before the fact is touched, so a
		// single valid envelope exercises every invalid id.
		if err := archive.ArchiveActive(envelope, ArchiveMeta{RequestID: invalidID}); err == nil {
			t.Fatalf("ArchiveActive(id=%q) error = nil, want rejection", invalidID)
		}
		if err := archive.PromoteTerminal(envelope, ArchiveMeta{RequestID: invalidID}); err == nil {
			t.Fatalf("PromoteTerminal(id=%q) error = nil, want rejection", invalidID)
		}
		if err := archive.ConfirmPersisted(invalidID); err == nil {
			t.Fatalf("ConfirmPersisted(id=%q) error = nil, want rejection", invalidID)
		}
	}
	for _, dir := range []string{archive.stageDir(StageActive), archive.stageDir(StageTerminalPending), archive.stageDir(StageCleanupPending)} {
		if count := countJSONFiles(t, dir); count != 0 {
			t.Fatalf("%s holds %d documents after rejections, want 0", dir, count)
		}
	}
}

func TestArchiveActiveRefusesToShadowTerminal(t *testing.T) {
	t.Parallel()
	archive := newTestArchive(t)
	const requestID = "req-shadow"
	fact := testFact("tenant-a", requestID)
	meta := testMeta(requestID)

	if err := archive.PromoteTerminal(testEnvelope(t, requestfact.ArchiveStateTerminalPending, fact), meta); err != nil {
		t.Fatalf("PromoteTerminal() error = %v", err)
	}
	err := archive.ArchiveActive(testEnvelope(t, requestfact.ArchiveStateActive, fact), meta)
	if !errors.Is(err, ErrAlreadyTerminal) {
		t.Fatalf("ArchiveActive() after promote error = %v, want ErrAlreadyTerminal", err)
	}
	requireState(t, archive.stagePath(StageActive, requestID), false)
	requireState(t, archive.stagePath(StageTerminalPending, requestID), true)
}

func TestRecoverToleratesCrashLeftoverTmpFiles(t *testing.T) {
	t.Parallel()
	archive := newTestArchive(t)
	const requestID = "req-tmp"
	if err := archive.ArchiveActive(testEnvelope(t, requestfact.ArchiveStateActive, testFact("tenant-a", requestID)), testMeta(requestID)); err != nil {
		t.Fatalf("ArchiveActive() error = %v", err)
	}
	target := archive.stagePath(StageActive, requestID)
	before, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read target: %v", err)
	}

	// tmp leftovers model a write interrupted between create and rename; they
	// are inert garbage and must neither break the scan nor damage targets.
	staleTmp := []string{
		filepath.Join(archive.stageDir(StageActive), requestID+fileSuffix+".1234"+tempSuffix),
		filepath.Join(archive.stageDir(StageActive), "req-other"+fileSuffix+".5678"+tempSuffix),
		filepath.Join(archive.stageDir(StageTerminalPending), "req-term"+fileSuffix+".9999"+tempSuffix),
	}
	for _, path := range staleTmp {
		if err := os.WriteFile(path, []byte(`{"half":"written`), 0o644); err != nil {
			t.Fatalf("write stale tmp %s: %v", path, err)
		}
	}

	recovered, err := archive.RecoverPending()
	if err != nil {
		t.Fatalf("RecoverPending() error = %v", err)
	}
	if len(recovered) != 1 || recovered[0].RequestID != requestID {
		t.Fatalf("RecoverPending() = %+v, want single %s", recovered, requestID)
	}
	after, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read target after recover: %v", err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("target document changed across a read-only recovery scan")
	}
	for _, path := range staleTmp {
		requireState(t, path, false)
	}
}

func TestRecoverQuarantinesUnreadableDocuments(t *testing.T) {
	t.Parallel()
	archive := newTestArchive(t)
	const goodID = "req-good"
	if err := archive.ArchiveActive(testEnvelope(t, requestfact.ArchiveStateActive, testFact("tenant-a", goodID)), testMeta(goodID)); err != nil {
		t.Fatalf("ArchiveActive() error = %v", err)
	}

	garbagePath := archive.stagePath(StageActive, "req-garbage")
	if err := os.WriteFile(garbagePath, []byte("not json at all{"), 0o644); err != nil {
		t.Fatalf("write garbage fixture: %v", err)
	}

	tamperedFact := encodeFact(t, testEnvelope(t, requestfact.ArchiveStateActive, testFact("tenant-a", "req-tampered")))
	writeWrapper(t, archive.stagePath(StageActive, "req-tampered"), archiveDocument{
		ArchiveVersion: ArchiveFormatVersionV1,
		Fact:           json.RawMessage(bytes.ReplaceAll(tamperedFact, []byte("gpt-test"), []byte("gpt-tamprd"))),
		Meta:           testMeta("req-tampered"),
	})

	// A valid document for req-b stored under req-a's filename must not be
	// recovered as req-a.
	writeWrapper(t, archive.stagePath(StageActive, "req-a"), archiveDocument{
		ArchiveVersion: ArchiveFormatVersionV1,
		Fact:           encodeFact(t, testEnvelope(t, requestfact.ArchiveStateActive, testFact("tenant-a", "req-b"))),
		Meta:           testMeta("req-b"),
	})

	writeWrapper(t, archive.stagePath(StageActive, "req-future"), archiveDocument{
		ArchiveVersion: ArchiveFormatVersionV1 + 9,
		Fact:           encodeFact(t, testEnvelope(t, requestfact.ArchiveStateActive, testFact("tenant-a", "req-future"))),
		Meta:           testMeta("req-future"),
	})

	recovered, err := archive.RecoverPending()
	if err != nil {
		t.Fatalf("RecoverPending() error = %v", err)
	}
	if len(recovered) != 1 || recovered[0].RequestID != goodID {
		t.Fatalf("RecoverPending() = %+v, want only %s", recovered, goodID)
	}

	for _, quarantined := range []string{"req-garbage", "req-tampered", "req-a", "req-future"} {
		requireState(t, archive.stagePath(StageActive, quarantined), false)
		requireState(t, filepath.Join(archive.quarantineDir(), quarantined+fileSuffix), true)
	}
	if count := countJSONFiles(t, archive.stageDir(StageActive)); count != 1 {
		t.Fatalf("active stage holds %d documents, want the single good one", count)
	}
}

func TestUnconfirmedTerminalDocumentIsRetained(t *testing.T) {
	t.Parallel()
	archive := newTestArchive(t)
	const requestID = "req-retain"
	if err := archive.PromoteTerminal(
		testEnvelope(t, requestfact.ArchiveStateTerminalPending, testFact("tenant-a", requestID)),
		testMeta(requestID),
	); err != nil {
		t.Fatalf("PromoteTerminal() error = %v", err)
	}

	// No ConfirmPersisted: the fact is exactly the "DB unacknowledged" case
	// recovery exists for, so the document must survive repeated scans.
	first, err := archive.RecoverPending()
	if err != nil {
		t.Fatalf("first RecoverPending() error = %v", err)
	}
	second, err := archive.RecoverPending()
	if err != nil {
		t.Fatalf("second RecoverPending() error = %v", err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("repeated RecoverPending() differ: %+v vs %+v", first, second)
	}
	if len(first) != 1 || first[0].Stage != StageTerminalPending || first[0].RequestID != requestID {
		t.Fatalf("RecoverPending() = %+v, want single terminal_pending %s", first, requestID)
	}
	requireState(t, archive.stagePath(StageTerminalPending, requestID), true)

	if err := archive.ConfirmPersisted(requestID); err != nil {
		t.Fatalf("ConfirmPersisted() error = %v", err)
	}
	requireState(t, archive.stagePath(StageTerminalPending, requestID), false)
	if recovered, err := archive.RecoverPending(); err != nil || len(recovered) != 0 {
		t.Fatalf("RecoverPending() after confirm = (%+v, %v), want empty", recovered, err)
	}
}

func TestConfirmCleanupFailureKeepsDocumentForRetry(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("chmod-based failure injection is not available on windows")
	}
	if os.Geteuid() == 0 {
		t.Skip("chmod-based failure injection is ineffective for root")
	}
	archive := newTestArchive(t)
	const requestID = "req-cleanup"
	if err := archive.PromoteTerminal(
		testEnvelope(t, requestfact.ArchiveStateTerminalPending, testFact("tenant-a", requestID)),
		testMeta(requestID),
	); err != nil {
		t.Fatalf("PromoteTerminal() error = %v", err)
	}

	// Reproduce the state a crash between the cleanup rename and the delete
	// would leave behind: the document already sits in cleanup_pending.
	if err := os.Rename(archive.stagePath(StageTerminalPending, requestID), archive.stagePath(StageCleanupPending, requestID)); err != nil {
		t.Fatalf("stage crash-window fixture: %v", err)
	}
	cleanupDir := archive.stageDir(StageCleanupPending)
	if err := os.Chmod(cleanupDir, 0o500); err != nil {
		t.Fatalf("chmod cleanup dir read-only: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(cleanupDir, 0o755) })

	// The delete fails; the document must remain in cleanup_pending for the
	// startup retry rather than being treated as confirmed-and-forgotten.
	if err := archive.ConfirmPersisted(requestID); err == nil {
		t.Fatal("ConfirmPersisted() error = nil, want cleanup failure")
	}
	requireState(t, archive.stagePath(StageCleanupPending, requestID), true)

	if err := os.Chmod(cleanupDir, 0o755); err != nil {
		t.Fatalf("restore cleanup dir perms: %v", err)
	}
	recovered, err := archive.RecoverPending()
	if err != nil {
		t.Fatalf("RecoverPending() error = %v", err)
	}
	if len(recovered) != 0 {
		t.Fatalf("RecoverPending() = %+v, want empty after cleanup retry", recovered)
	}
	requireState(t, archive.stagePath(StageCleanupPending, requestID), false)
}

func TestConfirmRenameFailureKeepsDocumentRecoverable(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("chmod-based failure injection is not available on windows")
	}
	if os.Geteuid() == 0 {
		t.Skip("chmod-based failure injection is ineffective for root")
	}
	archive := newTestArchive(t)
	const requestID = "req-rename"
	if err := archive.PromoteTerminal(
		testEnvelope(t, requestfact.ArchiveStateTerminalPending, testFact("tenant-a", requestID)),
		testMeta(requestID),
	); err != nil {
		t.Fatalf("PromoteTerminal() error = %v", err)
	}

	terminalDir := archive.stageDir(StageTerminalPending)
	if err := os.Chmod(terminalDir, 0o500); err != nil {
		t.Fatalf("chmod terminal dir read-only: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(terminalDir, 0o755) })

	// The terminal→cleanup rename fails; the document must stay in
	// terminal_pending so recovery still reports it as DB-unacknowledged.
	if err := archive.ConfirmPersisted(requestID); err == nil {
		t.Fatal("ConfirmPersisted() error = nil, want rename failure")
	}
	requireState(t, archive.stagePath(StageTerminalPending, requestID), true)

	if err := os.Chmod(terminalDir, 0o755); err != nil {
		t.Fatalf("restore terminal dir perms: %v", err)
	}
	recovered, err := archive.RecoverPending()
	if err != nil {
		t.Fatalf("RecoverPending() error = %v", err)
	}
	if len(recovered) != 1 || recovered[0].Stage != StageTerminalPending {
		t.Fatalf("RecoverPending() = %+v, want single terminal_pending entry", recovered)
	}
	if err := archive.ConfirmPersisted(requestID); err != nil {
		t.Fatalf("retry ConfirmPersisted() error = %v", err)
	}
	requireState(t, archive.stagePath(StageTerminalPending, requestID), false)
}

func TestRecoverResolvesCrashWindowDuplicate(t *testing.T) {
	t.Parallel()
	archive := newTestArchive(t)
	const requestID = "req-dup"
	fact := testFact("tenant-a", requestID)
	meta := testMeta(requestID)
	if err := archive.ArchiveActive(testEnvelope(t, requestfact.ArchiveStateActive, fact), meta); err != nil {
		t.Fatalf("ArchiveActive() error = %v", err)
	}
	activeBytes, err := os.ReadFile(archive.stagePath(StageActive, requestID))
	if err != nil {
		t.Fatalf("read active document: %v", err)
	}
	if err := archive.PromoteTerminal(testEnvelope(t, requestfact.ArchiveStateTerminalPending, fact), meta); err != nil {
		t.Fatalf("PromoteTerminal() error = %v", err)
	}
	// Reinstall the active copy to model a crash between the terminal write
	// and the active removal.
	if err := os.WriteFile(archive.stagePath(StageActive, requestID), activeBytes, 0o644); err != nil {
		t.Fatalf("reinstall active copy: %v", err)
	}

	recovered, err := archive.RecoverPending()
	if err != nil {
		t.Fatalf("RecoverPending() error = %v", err)
	}
	if len(recovered) != 1 || recovered[0].Stage != StageTerminalPending || recovered[0].RequestID != requestID {
		t.Fatalf("RecoverPending() = %+v, want one terminal_pending %s", recovered, requestID)
	}

	// Confirm must clear both the authoritative terminal document and the
	// superseded active copy.
	if err := archive.ConfirmPersisted(requestID); err != nil {
		t.Fatalf("ConfirmPersisted() error = %v", err)
	}
	requireState(t, archive.stagePath(StageTerminalPending, requestID), false)
	requireState(t, archive.stagePath(StageActive, requestID), false)
	requireState(t, archive.stagePath(StageCleanupPending, requestID), false)
}

func TestConcurrentDistinctRequestWriters(t *testing.T) {
	t.Parallel()
	archive := newTestArchive(t)
	const writers = 16

	var wg sync.WaitGroup
	errs := make(chan error, writers)
	for i := 0; i < writers; i++ {
		requestID := "req-concurrent-" + string(rune('a'+i))
		wg.Add(1)
		go func() {
			defer wg.Done()
			fact := testFact("tenant-a", requestID)
			meta := testMeta(requestID)
			if err := archive.ArchiveActive(testEnvelope(t, requestfact.ArchiveStateActive, fact), meta); err != nil {
				errs <- err
				return
			}
			if err := archive.PromoteTerminal(testEnvelope(t, requestfact.ArchiveStateTerminalPending, fact), meta); err != nil {
				errs <- err
				return
			}
			if err := archive.ConfirmPersisted(requestID); err != nil {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent writer failed: %v", err)
	}

	for _, stage := range []Stage{StageActive, StageTerminalPending, StageCleanupPending} {
		if count := countJSONFiles(t, archive.stageDir(stage)); count != 0 {
			t.Fatalf("stage %s holds %d documents after full lifecycles, want 0", stage, count)
		}
	}
	if recovered, err := archive.RecoverPending(); err != nil || len(recovered) != 0 {
		t.Fatalf("RecoverPending() after concurrent runs = (%+v, %v), want empty", recovered, err)
	}
}

func TestConcurrentSameRequestInterleaving(t *testing.T) {
	t.Parallel()
	archive := newTestArchive(t)
	const requestID = "req-same"
	fact := testFact("tenant-a", requestID)
	meta := testMeta(requestID)
	if err := archive.ArchiveActive(testEnvelope(t, requestfact.ArchiveStateActive, fact), meta); err != nil {
		t.Fatalf("ArchiveActive() error = %v", err)
	}

	const workers = 8
	const iterations = 25
	var wg sync.WaitGroup
	errs := make(chan error, workers)
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(offset int) {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				var err error
				if (i+offset)%2 == 0 {
					err = archive.PromoteTerminal(testEnvelope(t, requestfact.ArchiveStateTerminalPending, fact), meta)
				} else {
					// Legitimate until the first promotion lands; afterwards
					// the forward-only guard rejects it.
					err = archive.ArchiveActive(testEnvelope(t, requestfact.ArchiveStateActive, fact), meta)
					if errors.Is(err, ErrAlreadyTerminal) {
						err = nil
					}
				}
				if err != nil {
					errs <- err
					return
				}
			}
		}(w)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("interleaved writer failed: %v", err)
	}

	requireState(t, archive.stagePath(StageTerminalPending, requestID), true)
	recovered, err := archive.RecoverPending()
	if err != nil {
		t.Fatalf("RecoverPending() error = %v", err)
	}
	if len(recovered) != 1 || recovered[0].RequestID != requestID || recovered[0].Stage != StageTerminalPending {
		t.Fatalf("RecoverPending() = %+v, want one terminal_pending %s", recovered, requestID)
	}
	if err := archive.ConfirmPersisted(requestID); err != nil {
		t.Fatalf("ConfirmPersisted() error = %v", err)
	}
	for _, stage := range []Stage{StageActive, StageTerminalPending, StageCleanupPending} {
		if count := countJSONFiles(t, archive.stageDir(stage)); count != 0 {
			t.Fatalf("stage %s holds %d documents after confirm, want 0", stage, count)
		}
	}
}
