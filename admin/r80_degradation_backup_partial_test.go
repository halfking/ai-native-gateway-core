package admin

// R80 轮（2026-10-24）：`/api/admin/data-lifecycle/degradation/status`
// 的**备份摘要腿**守卫。
//
// ★ 背景：R79 给端点加了 5s ctx 预算 ⇒ 备份摘要的计算**会被预算截断**
//	（生产实测：5.18GB / 100 个文件，冷路径 ~48s，远超 5s）
//	⇒ 而 R79 的实现把**截断结果当完整结果缓存** ⇒
//	⇒ 端点对外说「100 个备份 / 5.18GB / 记录数 0 / 会话数 0」——
//	★★ **静默的错误数据**（比慢更危险）。
//
// ★ 两条不变量：
//
//	① **备份腿不完整 ⇒ 端点必须 200 + degraded=true + reason 点名 backup**；
//	② **部分摘要也要给**（前端可显示「部分值」），但**必须**同时有降级标记。
//
// ★ 判据断言**真实产物**（真跑一次 handler，看真实响应体）。

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/domains/dbdegradation"
)

// r80CorruptBackupDir 造一个「读不出统计」的备份目录。
//
// ★ 用**不可解压**的内容 ⇒ 统计必然不完整 ⇒ 端点必须显式标降级。
func r80CorruptBackupDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	backups := filepath.Join(dir, "backups")
	if err := os.MkdirAll(backups, 0o755); err != nil {
		t.Fatalf("★ 建目录失败：%v", err)
	}
	// ★ 文件名必须符合备份命名约定（否则根本不会进统计）
	path := filepath.Join(backups, "sessions-2026-10-20.jsonl.gz")
	if err := os.WriteFile(path, []byte("this-is-not-gzip"), 0o644); err != nil {
		t.Fatalf("★ 写文件失败：%v", err)
	}
	return dir
}

// TestR80DegradationStatusMustReportIncompleteBackups（★ 不变量①）
func TestR80DegradationStatusMustReportIncompleteBackups(t *testing.T) {
	dir := r80CorruptBackupDir(t)

	h := &Handler{}
	h.degradation = &degradationControl{
		reader: dbdegradation.NewFileReader(dir),
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/admin/data-lifecycle/degradation/status", nil)
	h.handleDegradationStatus(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("★ 期望 200（不得因为统计不全就整块失败），实测 %d（body=%s）", rec.Code, rec.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("★ 响应不是 JSON：%v（body=%s）", err, rec.Body.String())
	}
	if body["degraded"] != true {
		t.Errorf("★ 备份摘要不完整 ⇒ degraded 必须为 true（否则「记录数 0」会被读成「备份是空的」）；body=%v", body)
	}
	reason, _ := body["degraded_reason"].(string)
	if reason == "" {
		t.Errorf("★ 降级原因不得为空；body=%v", body)
	}
	if !strings.Contains(reason, "backup") {
		t.Errorf("★ 降级原因应点名不可得的部分（backup summary）；实际 %q", reason)
	}
}

// TestR80DegradationStatusKeepsPartialBackupsButMarksDegraded（★ 不变量②）
//
// ★ 部分摘要**要给**（前端可以显示「部分值」而不是空白），
//
//	但**必须**同时带降级标记 ⇒ 「给了数据」不等于「数据可信」。
func TestR80DegradationStatusKeepsPartialBackupsButMarksDegraded(t *testing.T) {
	dir := r80CorruptBackupDir(t)

	h := &Handler{}
	h.degradation = &degradationControl{
		reader: dbdegradation.NewFileReader(dir),
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/admin/data-lifecycle/degradation/status", nil)
	h.handleDegradationStatus(rec, req)

	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("★ 响应不是 JSON：%v", err)
	}
	backups, ok := body["backups"].(map[string]any)
	if !ok {
		t.Fatalf("★ 部分摘要也应当返回（前端要能显示「部分值」）；body=%v", body)
	}
	if body["degraded"] != true {
		t.Errorf("★ 给了部分数据也**必须**标 degraded；body=%v", body)
	}
	// ★ 文件清单（便宜的 stat 结果）应当仍然可见
	files, ok := backups["files"].([]any)
	if !ok || len(files) == 0 {
		t.Errorf("★ 部分摘要里应保留文件清单（stat 结果不依赖解压）；backups=%v", backups)
	}
}
