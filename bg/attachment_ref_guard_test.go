package bg

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pashagolub/pgxmock/v4"
)

// §三#2（2026-09-30 三十七轮续）：附件 LRU 悬挂删除守卫钉测。
// cleanupAttachmentsLRU 必须在 os.Remove 前反查 request_attachments 存活
// 引用，被引用文件跳过；checker 缺席/报错时 fail-closed 全跳过。

const refGuardTestHash = "a3b1c2d3e4f5061728394a5b6c7d8e9f0a1b2c3d4e5f60718293a4b5c6d7e8f9"

// fakeRefChecker 记录候选并回放预置判定。
type fakeRefChecker struct {
	referenced map[string]bool
	err        error
	got        []AttachmentRef
}

func (f *fakeRefChecker) ReferencedPaths(ctx context.Context, cands []AttachmentRef) (map[string]bool, error) {
	f.got = append(f.got, cands...)
	if f.err != nil {
		return nil, f.err
	}
	return f.referenced, nil
}

// seedExpiredAttachment 落一个 TTL 过期的附件文件（mtime 回拨 40 天）。
func seedExpiredAttachment(t *testing.T, dir, rel string) string {
	t.Helper()
	p := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(p), err)
	}
	if err := os.WriteFile(p, []byte("attachment-bytes"), 0o600); err != nil {
		t.Fatalf("write %s: %v", p, err)
	}
	old := time.Now().AddDate(0, 0, -40)
	if err := os.Chtimes(p, old, old); err != nil {
		t.Fatalf("chtimes %s: %v", p, err)
	}
	return p
}

func newRefGuardWorker(checker AttachmentReferenceChecker) *StorageRetentionWorker {
	w := NewStorageRetentionWorker(nil, "", nil)
	w.AttachmentTTLDays = 30
	w.RefChecker = checker
	return w
}

// TestCleanupAttachmentsLRU_ReferencedFileSurvives 钉死 §三#2 根修复：
// 同一轮 sweep 中，被引用候选必须存活、无引用候选被删。变异验证锚——
// 若删除前不查引用（回退盲删），本测试必红。
func TestCleanupAttachmentsLRU_ReferencedFileSurvives(t *testing.T) {
	dir := t.TempDir()
	keptRel := "2026/07/" + refGuardTestHash[:2] + "/" + refGuardTestHash[2:4] + "/" + refGuardTestHash + ".png"
	deletedRel := "2026/06/ff/01/" + strings.Repeat("ab", 32) + ".png"
	keptPath := seedExpiredAttachment(t, dir, keptRel)
	deletedPath := seedExpiredAttachment(t, dir, deletedRel)

	fake := &fakeRefChecker{referenced: map[string]bool{keptRel: true}}
	w := newRefGuardWorker(fake)
	w.cleanupAttachmentsLRU(context.Background(), dir, 0)

	if _, err := os.Stat(keptPath); err != nil {
		t.Fatalf("referenced attachment must survive LRU cleanup: %v", err)
	}
	if _, err := os.Stat(deletedPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unreferenced attachment must be deleted, stat err=%v", err)
	}
	// 候选身份必须以存储相对路径 + 内容哈希上交（路径 ToSlash、哈希自
	// 内容寻址文件名提取），checker 收不到绝对路径。
	var sawKeptRef bool
	for _, ref := range fake.got {
		if ref.RelPath == keptRel {
			sawKeptRef = true
			if ref.Hash != refGuardTestHash {
				t.Fatalf("checker must receive content hash from filename, got %q", ref.Hash)
			}
		}
		if ref.RelPath == deletedRel && ref.Hash != strings.Repeat("ab", 32) {
			t.Fatalf("checker must receive hash for unreferenced candidate too, got %q", ref.Hash)
		}
	}
	if !sawKeptRef {
		t.Fatalf("checker must receive the referenced candidate rel path %q, got %+v", keptRel, fake.got)
	}
}

// TestCleanupAttachmentsLRU_HashOnlyReferenceSurvives 钉 hash 臂语义：
// 路径未命中但内容哈希仍被引用（同内容跨月复本/legacy NULL hash 行的
// 保守过保护面由 checker 实现，worker 只认 checker 判定）。
func TestCleanupAttachmentsLRU_HashOnlyReferenceSurvives(t *testing.T) {
	dir := t.TempDir()
	rel := "2026/07/" + refGuardTestHash[:2] + "/" + refGuardTestHash[2:4] + "/" + refGuardTestHash + ".png"
	p := seedExpiredAttachment(t, dir, rel)

	// checker 判定存活（实现层的 hash 臂命中），worker 侧必须尊重
	fake := &fakeRefChecker{referenced: map[string]bool{rel: true}}
	w := newRefGuardWorker(fake)
	w.cleanupAttachmentsLRU(context.Background(), dir, 0)

	if _, err := os.Stat(p); err != nil {
		t.Fatalf("hash-referenced attachment must survive: %v", err)
	}
}

// TestCleanupAttachmentsLRU_NilCheckerFailClosed 无 checker 时禁止任何
// 附件删除（fail-closed：无引用核验能力就绝不 mtime 盲删）。
func TestCleanupAttachmentsLRU_NilCheckerFailClosed(t *testing.T) {
	dir := t.TempDir()
	rel := "2026/07/" + refGuardTestHash[:2] + "/" + refGuardTestHash[2:4] + "/" + refGuardTestHash + ".png"
	p := seedExpiredAttachment(t, dir, rel)

	w := newRefGuardWorker(nil)
	w.cleanupAttachmentsLRU(context.Background(), dir, 0)

	if _, err := os.Stat(p); err != nil {
		t.Fatalf("nil checker must fail closed (no deletion): %v", err)
	}
}

// TestCleanupAttachmentsLRU_CheckerErrorFailClosed 反查报错时本轮不删
// 任何附件——磁盘压力留给告警面，绝不用数据损坏换空间。
func TestCleanupAttachmentsLRU_CheckerErrorFailClosed(t *testing.T) {
	dir := t.TempDir()
	rel := "2026/07/" + refGuardTestHash[:2] + "/" + refGuardTestHash[2:4] + "/" + refGuardTestHash + ".png"
	p := seedExpiredAttachment(t, dir, rel)

	w := newRefGuardWorker(&fakeRefChecker{err: errors.New("db down")})
	w.cleanupAttachmentsLRU(context.Background(), dir, 0)

	if _, err := os.Stat(p); err != nil {
		t.Fatalf("checker error must fail closed (no deletion): %v", err)
	}
}

// TestAttachmentContentHash_FromContentAddressedName 钉哈希提取：内容寻址
// 文件名（<64 小写 hex><ext>）提取 sha256；legacy req_<id>/ 布局与非 hex
// 名返回空串（仅路径臂反查）。
func TestAttachmentContentHash_FromContentAddressedName(t *testing.T) {
	hashed := "2026/07/" + refGuardTestHash[:2] + "/" + refGuardTestHash[2:4] + "/" + refGuardTestHash + ".png"
	if got := attachmentContentHash(hashed); got != refGuardTestHash {
		t.Fatalf("content-addressed name must yield sha256, got %q", got)
	}
	legacy := "req_abc123/photo.jpeg"
	if got := attachmentContentHash(legacy); got != "" {
		t.Fatalf("legacy layout must yield empty hash, got %q", got)
	}
	upper := "2026/07/AB/CD/" + strings.ToUpper(refGuardTestHash) + ".png"
	if got := attachmentContentHash(upper); got != "" {
		t.Fatalf("non-lowercase-hex name must yield empty hash, got %q", got)
	}
	if got := attachmentContentHash("2026/07/ab/cd/short.png"); got != "" {
		t.Fatalf("short stem must yield empty hash, got %q", got)
	}
}

// TestPGAttachmentRefChecker_TwoArmLookup 钉 checker 的两臂反查与合并：
// 路径臂命中、哈希臂命中、双臂全空的候选各自归位；SQL 形态漂移即红。
func TestPGAttachmentRefChecker_TwoArmLookup(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	pathHit := AttachmentRef{RelPath: "2026/07/aa/bb/" + refGuardTestHash + ".png", Hash: refGuardTestHash}
	hashHit := AttachmentRef{RelPath: "2026/08/cc/dd/" + strings.Repeat("ef", 32) + ".png", Hash: strings.Repeat("ef", 32)}
	miss := AttachmentRef{RelPath: "req_legacy/name.png"}

	mock.ExpectQuery("SELECT storage_path FROM public\\.request_attachments WHERE storage_path = ANY\\(\\$1\\)").
		WithArgs([]string{pathHit.RelPath, hashHit.RelPath, miss.RelPath}).
		WillReturnRows(pgxmock.NewRows([]string{"storage_path"}).AddRow(pathHit.RelPath))
	mock.ExpectQuery("SELECT hash FROM public\\.request_attachments WHERE hash = ANY\\(\\$1\\)").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"hash"}).AddRow(hashHit.Hash))

	c := &PGAttachmentRefChecker{pool: mock}
	got, err := c.ReferencedPaths(context.Background(), []AttachmentRef{pathHit, hashHit, miss})
	if err != nil {
		t.Fatalf("ReferencedPaths: %v", err)
	}
	if !got[pathHit.RelPath] {
		t.Fatalf("path-arm hit must be referenced: %v", got)
	}
	if !got[hashHit.RelPath] {
		t.Fatalf("hash-arm hit must be referenced: %v", got)
	}
	if got[miss.RelPath] {
		t.Fatalf("candidate with no arm hit must not be referenced: %v", got)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

// TestPGAttachmentRefChecker_EmptyCandidatesNoQuery 空候选零查询短路。
func TestPGAttachmentRefChecker_EmptyCandidatesNoQuery(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	c := &PGAttachmentRefChecker{pool: mock}
	got, err := c.ReferencedPaths(context.Background(), nil)
	if err != nil {
		t.Fatalf("ReferencedPaths(nil): %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("empty candidates must return empty map, got %v", got)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("no query expected for empty candidates: %v", err)
	}
}
