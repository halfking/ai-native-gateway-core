package largebody

// Rebind 之后暂存目录不得留下空壳（2026-11-23）。
//
// 211 实测：每个大请求在 `gw_staging_<uuid>/large-v1/requests/<reqid>/` 留下
// 四层空目录，一个晚上累积 1800+ 个——inode/目录块的缓慢泄漏，且 `find` 扫
// 整棵归档树要跑好几分钟。
//
// 契约：
//  1. rebind 成功 ⇒ 暂存侧的目录链（请求目录到暂存会话目录，共四层）被删干净，
//     会话侧的归档完好；
//  2. 到暂存会话目录为止：分片目录与租户目录即使此刻是空的也不碰（它们是所有
//     会话共享的，并发的 Capture 可能正在往里建目录）；
//  3. 只删空目录：链上任何一层还有别的东西时在那一层停下，一个字节都不动；
//  4. 非暂存身份的源不动目录（它的会话目录可能有并发的 Capture）；
//  5. 幂等重试（再次 rebind）不受影响。

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func stringsReader(s string) *strings.Reader { return strings.NewReader(s) }

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func TestRebindRemovesStagingChainOnly(t *testing.T) {
	root := t.TempDir() // 全空的归档树：祖先目录在清理后都是空的
	s, err := NewStore(root)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ref, err := s.Capture(context.Background(), Identity{TenantID: "tenant", SessionID: "gw_staging_probe-1"}, stringsReader(`{"messages":[{"role":"user","content":"first"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	staging := ref.Identity
	dir := requestDir(staging)
	// 布局钉测：<reqid> ← requests ← large-v1 ← <会话目录>，正好 stagingChainLevels 层。
	sessionDir := filepath.Join(staging.TenantID, staging.SessionID[:2], staging.SessionID)
	if got := filepath.Join(sessionDir, "large-v1", "requests", staging.RequestID); got != dir {
		t.Fatalf("requestDir 布局变了：stagingChainLevels 要同步改\n got %q\nwant %q", got, dir)
	}
	shardDir := filepath.Dir(sessionDir)
	if !exists(filepath.Join(root, dir)) {
		t.Fatal("precondition: staging dir must exist before rebind")
	}
	target := ref.Identity // 同一请求、同一租户，只换会话
	target.SessionID = "session-real"
	if _, err := s.Rebind(context.Background(), ref, target.SessionID); err != nil {
		t.Fatal(err)
	}
	for _, gone := range []string{dir, filepath.Dir(dir), sessionDir} {
		if exists(filepath.Join(root, gone)) {
			t.Fatalf("rebind 之后暂存目录链必须被删除: %s", gone)
		}
	}
	// 共享的祖先目录：此刻是空的，也必须留着。
	for _, kept := range []string{shardDir, staging.TenantID} {
		if !exists(filepath.Join(root, kept)) {
			t.Fatalf("分片/租户目录不得被删除（即使为空）: %s", kept)
		}
	}
	// 会话侧的归档完好可读。
	got, err := s.Recover(context.Background(), target)
	if err != nil || got.SHA256 != ref.SHA256 {
		t.Fatalf("target archive broken after rebind: %+v %v", got, err)
	}
	// 幂等重试：源已不存在，仍然返回已验证的目标。
	if again, err := s.Rebind(context.Background(), ref, target.SessionID); err != nil || again != got {
		t.Fatalf("idempotent rebind: %+v %v", again, err)
	}
}

func TestRebindKeepsNonEmptyChain(t *testing.T) {
	root := t.TempDir()
	s, err := NewStore(root)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ref, err := s.Capture(context.Background(), Identity{TenantID: "tenant", SessionID: "gw_staging_probe-2"}, stringsReader(`{"messages":[{"role":"user","content":"first"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	// 在暂存请求目录里放一个无关文件：它是这个目录里仅剩的内容 ⇒ 目录链必须
	// 在请求目录这一层停下，文件原样保留。
	sentinel := filepath.Join(root, requestDir(ref.Identity), "keep.txt")
	if err := os.WriteFile(sentinel, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Rebind(context.Background(), ref, "session-real"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(sentinel)
	if err != nil || string(data) != "keep" {
		t.Fatalf("只删空目录：无关文件被删了或读不了: %q %v", data, err)
	}
	if !exists(filepath.Dir(sentinel)) {
		t.Fatal("还有内容的目录不得被删除")
	}
}

func TestRebindLeavesNonStagingSourceDirs(t *testing.T) {
	root := t.TempDir()
	s, err := NewStore(root)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ref, err := s.Capture(context.Background(), Identity{TenantID: "tenant", SessionID: "session-old"}, stringsReader(`{"messages":[{"role":"user","content":"first"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	sourceDir := filepath.Join(root, requestDir(ref.Identity))
	if _, err := s.Rebind(context.Background(), ref, "session-new"); err != nil {
		t.Fatal(err)
	}
	// 文件已清走（源归档不可恢复），但会话目录本身留着：它不是这一次 rebind
	// 私有的，可能有并发的 Capture 正往里写。
	if exists(filepath.Join(sourceDir, "ref.json")) || exists(filepath.Join(sourceDir, "raw.json")) {
		t.Fatal("rebind 之后源归档的文件必须被清走")
	}
	if !exists(sourceDir) {
		t.Fatal("非暂存身份的源目录不得被删除")
	}
}
