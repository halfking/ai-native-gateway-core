package main

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// buildGzip 把若干行打成一段合法的 gzip member 字节。
func buildGzip(t *testing.T, lines []string) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := gzip.NewWriter(&buf)
	for _, l := range lines {
		if _, err := w.Write([]byte(l + "\n")); err != nil {
			t.Fatalf("写 gzip 失败：%v", err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatalf("关闭 gzip 失败：%v", err)
	}
	return buf.Bytes()
}

// recLine 造一条 request_log 记录（带 request_body）。
func recLine(t *testing.T, requestID, sessionID, body string) string {
	t.Helper()
	payload := map[string]any{
		"request_id":    requestID,
		"gw_session_id": sessionID,
		"event_at":      "2026-10-01T10:00:00Z",
		"request_body":  body,
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("序列化 payload 失败：%v", err)
	}
	rec := map[string]any{
		"type":       "request_log",
		"timestamp":  "2026-10-01T10:00:00Z",
		"record_key": requestID,
		"payload":    json.RawMessage(raw),
	}
	out, err := json.Marshal(rec)
	if err != nil {
		t.Fatalf("序列化 record 失败：%v", err)
	}
	return string(out)
}

// writeBackup 把字节写成 {base}/backups/{name}。
func writeBackup(t *testing.T, base, name string, data []byte) string {
	t.Helper()
	dir := filepath.Join(base, "backups")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("建目录失败：%v", err)
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("写 %s 失败：%v", path, err)
	}
	return path
}

// TestRefuseBackupDirWrite 钉住只读护栏：输出不得落进 {base}/backups。
//
// ★ 为什么这是安全属性：兜底文件是**目前唯一还活着的历史正文载体**
//
//	（§143.70：库里没有、PG 备份没有）⇒ ⇒ 工具覆盖它 = 把恢复面自己删掉。
func TestRefuseBackupDirWrite(t *testing.T) {
	base := "/srv/gw/data"
	bad := []string{
		"/srv/gw/data/backups",
		"/srv/gw/data/backups/meta.jsonl",
		"/srv/gw/data/backups/2026-10-01/sessions.jsonl.gz",
	}
	for _, out := range bad {
		if err := refuseBackupDirWrite(base, out); err == nil {
			t.Errorf("输出 %q 竟被允许写进备份目录 ⇒ ★ 只读护栏失效", out)
		}
	}
	ok := []string{"", "/tmp/meta.jsonl", "/srv/gw/data/meta.jsonl", "/srv/gw/other/backups/x.jsonl"}
	for _, out := range ok {
		if err := refuseBackupDirWrite(base, out); err != nil {
			t.Errorf("输出 %q 被误拒：%v", out, err)
		}
	}
}

// TestBodyStats 只给长度与哈希，★ 不泄露正文。
func TestBodyStats(t *testing.T) {
	n, sum := bodyStats("")
	if n != 0 || sum != "" {
		t.Errorf("空串应得 (0, \"\")，实得 (%d, %q)", n, sum)
	}
	n, sum = bodyStats(nil)
	if n != 0 || sum != "" {
		t.Errorf("nil 应得 (0, \"\")，实得 (%d, %q)", n, sum)
	}
	n, sum = bodyStats("hello")
	if n != 5 {
		t.Errorf("\"hello\" 的长度应得 5，实得 %d", n)
	}
	if len(sum) != 64 {
		t.Errorf("sha256 应为 64 位 hex，实得 %q", sum)
	}
}

// TestScanRecoversFromCorruptGzip 是本工具的**存在理由**的单测：
//
//	一个「完整 member + 截断 member」的混合体（= 生产现场形态：
//	多进程 O_APPEND + 容器硬杀跳过 gzip.Close）⇒
//	★ 单遍 gzip.NewReader 整体失败（0 条可恢复）
//	★ 仓内打捞通道仍能捞出两段 member 的记录。
func TestScanRecoversFromCorruptGzip(t *testing.T) {
	base := t.TempDir()
	clean := buildGzip(t, []string{
		recLine(t, "req-clean-1", "gw_sess_a", `{"messages":[{"role":"user","content":"BODY-CONTENT-MARKER-UNIQUE-9f3a71c2"}]}`),
		recLine(t, "req-clean-2", "gw_sess_a", `{"messages":[{"role":"user","content":"c2"}]}`),
	})
	truncated := buildGzip(t, []string{
		recLine(t, "req-dirty-1", "gw_sess_b", `{"messages":[{"role":"user","content":"BODY-CONTENT-MARKER-UNIQUE-9f3a71c2"}]}`),
		recLine(t, "req-dirty-2", "gw_sess_b", `{"messages":[{"role":"user","content":"d2"}]}`),
	})
	// 截断第二个 member：保留头 + 半个 deflate 流（无 trailer）
	corrupt := append(append([]byte{}, clean...), truncated[:len(truncated)/2]...)

	cleanPath := writeBackup(t, base, "sessions-2026-10-01.jsonl.gz", clean)
	corruptPath := writeBackup(t, base, "sessions-2026-10-02.jsonl.gz", corrupt)

	// ★ 对照：单遍 gzip 读那个损坏文件 ⇒ 整体失败。
	f, err := os.Open(corruptPath)
	if err != nil {
		t.Fatalf("打开损坏文件失败：%v", err)
	}
	zr, err := gzip.NewReader(f)
	if err == nil {
		_, readErr := io.ReadAll(zr)
		if readErr == nil {
			t.Errorf("★ 对照臂失效：这个损坏文件竟能被单遍 gzip 完整读出 ⇒ 测试前提不成立")
		}
	}
	f.Close()

	// ★ 本工具的打捞路径 ⇒ 应捞出两段 member 的全部记录。
	metaPath := filepath.Join(t.TempDir(), "meta.jsonl")
	sum, err := runScan(context.Background(), base, "", metaPath, "")
	if err != nil {
		t.Fatalf("扫描失败：%v", err)
	}
	if sum.Totals.Records < 4 {
		t.Errorf("打捞只捞回 %d 条记录（期望 ≥ 4：干净文件 2 条 + 损坏文件 2 条）⇒ ★ 打捞通道没起作用",
			sum.Totals.Records)
	}
	if sum.Totals.ReqBodyRecords < 4 {
		t.Errorf("带 request_body 的记录只有 %d 条（期望 ≥ 4）", sum.Totals.ReqBodyRecords)
	}
	// 损坏文件必须被标记为「有丢失」⇒ 归档护栏（调用方不得删它）。
	var dirty fileStat
	for _, fs := range sum.Files {
		if fs.File == "sessions-2026-10-02.jsonl.gz" {
			dirty = fs
		}
	}
	if dirty.File == "" {
		t.Fatalf("汇总里没有损坏文件那一条")
	}
	if !dirty.HasLoss {
		t.Errorf("损坏文件未被标记 has_loss ⇒ ★ 归档护栏失效（调用方会以为它可以删）")
	}
	// 干净文件不应被标记有丢失。
	for _, fs := range sum.Files {
		if fs.File == filepath.Base(cleanPath) && fs.HasLoss {
			t.Errorf("干净文件被标记 has_loss ⇒ 打捞统计有误")
		}
	}

	// 元数据必须是「每条一行」且含 request_id / gw_session_id。
	raw, err := os.ReadFile(metaPath)
	if err != nil {
		t.Fatalf("读元数据失败：%v", err)
	}
	lines := strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
	if len(lines) < 4 {
		t.Fatalf("元数据只有 %d 行（期望 ≥ 4）", len(lines))
	}
	if !strings.Contains(lines[0], "gw_session_id") {
		t.Errorf("元数据行缺少 gw_session_id 字段：%s", lines[0])
	}
	// ★ 元数据里不得出现正文内容
	// ★★ 用【唯一长标记】而不是短串：短串（如 "c1"）会命中 sha256 的十六进制
	//    （⇒ 断言假阳性 ⇒ 这是本仓反复踩的「子串断言」族坑）。
	const bodyMarker = "BODY-CONTENT-MARKER-UNIQUE-9f3a71c2"
	if strings.Contains(string(raw), bodyMarker) {
		t.Errorf("★ 元数据里出现了正文标记串 ⇒ 本工具承诺「只出元数据」被破坏")
	}
}

// TestBodyMsgCounts 钉住「累积深度」计数口径。
//
// ★ 为什么这个口径重要（§143.71）：仓内 `DeriveTurnDeltas` 是【前缀差分】⇒
//
//	一条累积到第 K 轮的请求体含 K 条 user 消息 ⇒ K = 该记录能拆出的轮数上限。
//	⇒ 这个计数直接决定「能恢复多少轮正文」的量级。
//
// ★★ 本单测钉住一条容易被合并掉的区分：
//
//		「没统计出来」（msgs_field 为空）≠「统计出来了但确实没有 user 消息」
//		（msgs_field 非空、user_msgs=0）
//
//	  ⇒ 若把两者混为一谈，「没统计出来」会被读成「这条不是累积体」⇒
//	  ⇒ 多轮占比会被系统性低估 ⇒ 恢复面被低估。
func TestBodyMsgCounts(t *testing.T) {
	cases := []struct {
		name      string
		body      string
		wantUser  int
		wantTotal int
		wantField string
	}{
		{"messages 形态", `{"messages":[{"role":"user","content":"a"},{"role":"assistant","content":"b"},{"role":"tool","content":"c"}]}`, 1, 3, "messages"},
		{"裸数组形态", `[{"role":"user"},{"role":"user"}]`, 2, 2, "array"},
		{"gemini contents 形态", `{"contents":[{"role":"user"}]}`, 1, 1, "contents"},
		{"★ 统计出来了但没有 user 消息", `{"messages":[]}`, 0, 0, "messages"},
		{"★ 没统计出来", `not-json-at-all`, 0, 0, ""},
		{"★ 空字符串", ``, 0, 0, ""},
	}
	for _, c := range cases {
		user, total, field := bodyMsgCounts(c.body)
		if user != c.wantUser || total != c.wantTotal || field != c.wantField {
			t.Errorf("%s：实得 (user=%d, total=%d, field=%q)，期望 (user=%d, total=%d, field=%q)",
				c.name, user, total, field, c.wantUser, c.wantTotal, c.wantField)
		}
	}
	// ★ 正常形态的对照：★ 必须能数出非 0 ⇒ 否则「计数恒为 0」会伪装成「都是单轮体」。
	user, total, field := bodyMsgCounts(`{"messages":[{"role":"user"},{"role":"user"},{"role":"assistant"}]}`)
	if user != 2 || total != 3 || field != "messages" {
		t.Errorf("★ 正常形态应数出 user=2 total=3，实得 (%d, %d, %q) ⇒ 计数实现坏了", user, total, field)
	}
}

// TestScanExportsAccumDepth 端到端：扫描导出的元数据必须带累积深度计数。
func TestScanExportsAccumDepth(t *testing.T) {
	base := t.TempDir()
	writeBackup(t, base, "sessions-2026-09-01.jsonl.gz", buildGzip(t, []string{
		recLine(t, "r1", "s1", `{"messages":[{"role":"user"},{"role":"user"}]}`),
		recLine(t, "r2", "s1", `{"messages":[{"role":"user"}]}`),
	}))
	metaPath := filepath.Join(t.TempDir(), "meta.jsonl")
	sum, err := runScan(context.Background(), base, "2026-09", metaPath, "")
	if err != nil {
		t.Fatalf("扫描失败：%v", err)
	}
	if sum.Totals.AccumRecords != 1 {
		t.Errorf("多轮累积体记录数应为 1（只有 r1 是两条 user），实得 %d", sum.Totals.AccumRecords)
	}
	if sum.Totals.AccumMaxUser != 2 {
		t.Errorf("最大累积深度应为 2，实得 %d", sum.Totals.AccumMaxUser)
	}
	raw, err := os.ReadFile(metaPath)
	if err != nil {
		t.Fatalf("读元数据失败：%v", err)
	}
	if !strings.Contains(string(raw), `"user_msgs":2`) {
		t.Errorf("元数据行里应出现 user_msgs=2：%s", string(raw))
	}
	if !strings.Contains(string(raw), `"msgs_field":"messages"`) {
		t.Errorf("元数据行里应出现 msgs_field=messages：%s", string(raw))
	}
}

// TestScanSessionFilter 钉住按会话过滤（★ 用于只导一个会话做保真度对比）。
//
// ★ 为什么需要它：`-payload-out` 导的是【含用户正文的原始 payload】
// ⇒ ⇒ 体积极大 ⇒ ⇒ 必须能只导一个会话 ⇒ ★⇒ 过滤必须真的生效。
func TestScanSessionFilter(t *testing.T) {
	base := t.TempDir()
	writeBackup(t, base, "sessions-2026-10-01.jsonl.gz", buildGzip(t, []string{
		recLine(t, "r1", "gw_sess_keep", `{"messages":[{"role":"user","content":"k1"}]}`),
		recLine(t, "r2", "gw_sess_drop", `{"messages":[{"role":"user","content":"d1"}]}`),
		recLine(t, "r3", "gw_sess_keep", `{"messages":[{"role":"user","content":"k2"}]}`),
	}))

	metaPath := filepath.Join(t.TempDir(), "meta.jsonl")
	sum, err := runScanAnalyze(context.Background(), base, "2026-10", metaPath, "", nil, "gw_sess_keep")
	if err != nil {
		t.Fatalf("扫描失败：%v", err)
	}
	if sum.Totals.Records != 2 {
		t.Errorf("过滤后应只有 2 条记录（gw_sess_keep 的 2 条），实得 %d", sum.Totals.Records)
	}
	raw, err := os.ReadFile(metaPath)
	if err != nil {
		t.Fatalf("读元数据失败：%v", err)
	}
	if strings.Contains(string(raw), "gw_sess_drop") {
		t.Errorf("★ 被过滤掉的会话仍出现在元数据里 ⇒ 过滤没生效")
	}
	if !strings.Contains(string(raw), "gw_sess_keep") {
		t.Errorf("★ 目标会话没有出现在元数据里 ⇒ 过滤过头了")
	}
}

// TestScanPrefixFilter 钉住日期前缀过滤。
func TestScanPrefixFilter(t *testing.T) {
	base := t.TempDir()
	writeBackup(t, base, "sessions-2026-10-01.jsonl.gz",
		buildGzip(t, []string{recLine(t, "r1", "s1", `{"messages":[]}`)}))
	writeBackup(t, base, "sessions-2026-09-30.jsonl.gz",
		buildGzip(t, []string{recLine(t, "r2", "s2", `{"messages":[]}`)}))

	sum, err := runScan(context.Background(), base, "2026-09", "", "")
	if err != nil {
		t.Fatalf("扫描失败：%v", err)
	}
	if len(sum.Files) != 1 || !strings.HasPrefix(sum.Files[0].File, "sessions-2026-09-30") {
		t.Errorf("前缀过滤结果不对：%+v", sum.Files)
	}
	if sum.Totals.Records != 1 {
		t.Errorf("过滤后应只有 1 条记录，实得 %d", sum.Totals.Records)
	}
}
