package admin

// R82b 轮（2026-10-24）：`/api/admin/data-lifecycle/degradation/status` 的
// **会话数口径不可用**守卫。
//
// ★ 背景（154 生产实测）：
//
//	端点对外说「total_sessions: 1」，而同一份摘要有 57644 条记录。
//	⇒ 追到根因：备份记录**绝大多数是 `request_wal` / `request_log`**，
//	  它们的顶层**没有 `session_id` 字段**（`session_id` 只在 snapshot 类记录上）
//	⇒ `BackupRecord.SessionID` 恒为空 ⇒ 「唯一 session 集合」里只有**空串**
//	⇒ 「会话数 = 1」是**读数失真**，不是事实。
//
// ★ 本门钉住：**这个失真不得沉默** ——
//
//	① 摘要里必须显式标 `session_count_available=false`；
//	② 端点必须 `degraded=true` 且 reason 点名「会话数不可用」；
//	③ **阳性对照**：记录带 session_id 时**不得**报不可用（否则是恒假的门）。

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/domains/dbdegradation"
)

// r82BackupDirWithoutSessionID 造一个「记录不带 session_id」的备份目录（生产同形）。
func r82BackupDirWithoutSessionID(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	backups := filepath.Join(dir, "backups")
	if err := os.MkdirAll(backups, 0o755); err != nil {
		t.Fatalf("★ 建目录失败：%v", err)
	}
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	for i := 0; i < 3; i++ {
		line := `{"type":"request_wal","timestamp":"2026-10-23T16:12:10Z","record_key":"req-` +
			string(rune('a'+i)) + `:update","payload":{"RequestID":"req-` +
			string(rune('a'+i)) + `"}}` + "\n"
		if _, err := zw.Write([]byte(line)); err != nil {
			t.Fatalf("★ 写记录失败：%v", err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("★ 关 gzip 失败：%v", err)
	}
	path := filepath.Join(backups, "sessions-2026-10-20.jsonl.gz")
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatalf("★ 写文件失败：%v", err)
	}
	return dir
}

// r82BackupDirWithSessionID 造一个「记录带 session_id」的备份目录（阳性对照）。
func r82BackupDirWithSessionID(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	backups := filepath.Join(dir, "backups")
	if err := os.MkdirAll(backups, 0o755); err != nil {
		t.Fatalf("★ 建目录失败：%v", err)
	}
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	for _, s := range []string{"s1", "s2"} {
		if _, err := zw.Write([]byte(`{"type":"snapshot","session_id":"` + s + `"}` + "\n")); err != nil {
			t.Fatalf("★ 写记录失败：%v", err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("★ 关 gzip 失败：%v", err)
	}
	path := filepath.Join(backups, "sessions-2026-10-20.jsonl.gz")
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatalf("★ 写文件失败：%v", err)
	}
	return dir
}

func r82CallDegradationStatus(t *testing.T, dir string) map[string]any {
	t.Helper()
	h := &Handler{}
	h.degradation = &degradationControl{
		reader: dbdegradation.NewFileReader(dir),
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/admin/data-lifecycle/degradation/status", nil)
	h.handleDegradationStatus(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("★ 期望 200，实测 %d（body=%s）", rec.Code, rec.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("★ 响应不是 JSON：%v（body=%s）", err, rec.Body.String())
	}
	return body
}

// TestR82DegradationStatusMustFlagUnavailableSessionCount（★ 不变量①②）
func TestR82DegradationStatusMustFlagUnavailableSessionCount(t *testing.T) {
	body := r82CallDegradationStatus(t, r82BackupDirWithoutSessionID(t))

	backups, ok := body["backups"].(map[string]any)
	if !ok {
		t.Fatalf("★ 摘要必须返回；body=%v", body)
	}
	if avail, _ := backups["session_count_available"].(bool); avail {
		t.Errorf("★ 记录没有 session_id ⇒ 会话数口径不可用 ⇒ 必须显式标 false；backups=%v", backups)
	}
	if body["degraded"] != true {
		t.Errorf("★ 会话数口径不可用 ⇒ degraded 必须为 true（否则「会话数 0」会被读成"+
			"「备份里没有会话」—— 静默的错误数据）；body=%v", body)
	}
	reason, _ := body["degraded_reason"].(string)
	if !strings.Contains(reason, "session count unavailable") {
		t.Errorf("★ 降级原因必须点名「会话数不可用」；实际 %q", reason)
	}
}

// TestR82DegradationStatusMustNotFlagAvailableSessionCount（★ 不变量③：阳性对照）
//
// ★ 「不可用」不能是**恒真**的门 ⇒ 记录带 session_id 时**不得**报不可用。
func TestR82DegradationStatusMustNotFlagAvailableSessionCount(t *testing.T) {
	body := r82CallDegradationStatus(t, r82BackupDirWithSessionID(t))

	backups, ok := body["backups"].(map[string]any)
	if !ok {
		t.Fatalf("★ 摘要必须返回；body=%v", body)
	}
	if avail, _ := backups["session_count_available"].(bool); !avail {
		t.Errorf("★ 记录带 session_id ⇒ 会话数口径**可用**；backups=%v", backups)
	}
	reason, _ := body["degraded_reason"].(string)
	if strings.Contains(reason, "session count unavailable") {
		t.Errorf("★ 会话数可用时**不得**报「不可用」；实际 %q", reason)
	}
}
