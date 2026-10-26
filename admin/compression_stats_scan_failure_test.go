package admin

import (
	"bytes"
	"errors"
	"log/slog"
	"os"
	"strings"
	"testing"
)

// ★ 本文件是 2026-10-24 12h 审计轮【问题⑲】的门。
//
// ★ 问题⑲ 是什么：`/api/admin/compression/stats` 的三个步骤里，
// agg 步的行级扫描失败有 slog.Warn，而 band / bucket 两步是**静默 continue**
// ⇒ ⇒ 同一条命令里**有两步的行级失败不留任何痕迹**
// ⇒ ⇒ 端点照样 200、`degraded=false`，而 `token_band_*` / `hourly_series` 是**少算的**
// ⇒ ⇒ 调用方无法区分「真的是 0」与「这行没读出来」
// ⇒ ⇒ ★ 这与 R77 轮在**步级**修掉的坑是同一个坑，只是下移到了**行级**。
//
// ★ 门的设计：判据①是**行为级**（helper 真的记了 WARN、点了名、返回「跳过」），
// 判据②是**结构级**（每个 Scan 站点的错误要么被 return、要么走 helper，没有第三个出口）
// —— 判据②的检查逻辑抽成纯函数，用**合成反例**做阴性对照，证明它有牙。

// findSilentScanSkips 在源码里找「扫描失败被静默跳过」的站点。
//
// ★ 判定规则（对每个 Scan 站点）：错误处理必须落在**两个出口之一**
// ① `return err`（把错误交出去 ⇒ 端点会看到它）
// ② `compressionStatsScanFailure(...)`（记 WARN 并点名是哪一步）
// ⇒ 其余写法（含裸 `continue`）都是**静默跳过** ⇒ 计入违规。
func findSilentScanSkips(src string) []string {
	lines := strings.Split(src, "\n")
	var bad []string
	for i, ln := range lines {
		if !strings.Contains(ln, "Scan(") {
			continue
		}
		// 取该站点往后的 3 行作为「错误处理窗口」
		end := i + 3
		if end > len(lines) {
			end = len(lines)
		}
		window := strings.Join(lines[i:end], "\n")
		if strings.Contains(window, "return err") ||
			strings.Contains(window, "compressionStatsScanFailure(") {
			continue
		}
		bad = append(bad, strings.TrimSpace(ln))
	}
	return bad
}

// 判据②（结构级）：本仓的 compression_stats.go 里不得再有静默跳过的扫描失败。
func TestCompressionStatsScanFailureMustNotBeSilentlySkipped(t *testing.T) {
	src, err := os.ReadFile("compression_stats.go")
	if err != nil {
		t.Fatalf("读不到 compression_stats.go：%v", err)
	}
	if bad := findSilentScanSkips(string(src)); len(bad) > 0 {
		t.Fatalf("以下扫描站点的失败被静默跳过（端点会 200 + degraded=false 但数据少算）：\n  %s",
			strings.Join(bad, "\n  "))
	}
}

// 判据②的**阴性对照**：证明 findSilentScanSkips 真的有牙，
// 而不是「对任何源码都返回空」的恒绿检查器。
func TestFindSilentScanSkipsDetectsSilentSkips(t *testing.T) {
	cases := []struct {
		name string
		src  string
		want int
	}{
		{
			name: "裸 continue 应当被抓出来",
			src:  "if err := rows.Scan(&a); err != nil {\n\tcontinue\n}\n",
			want: 1,
		},
		{
			name: "return err 是合法出口",
			src:  "if err := rows.Scan(&a); err != nil {\n\treturn err\n}\n",
			want: 0,
		},
		{
			name: "走 helper 是合法出口",
			src:  "if err := rows.Scan(&a); err != nil {\n\tcompressionStatsScanFailure(\"agg\", err)\n\tcontinue\n}\n",
			want: 0,
		},
		{
			name: "★ 三个站里只有一个走 helper ⇒ 仍应被抓出来（防「以为改了就算改全了」）",
			src: "if err := rows.Scan(&a); err != nil {\n\tcompressionStatsScanFailure(\"agg\", err)\n\tcontinue\n}\n" +
				"if err := bandRows.Scan(&b); err != nil {\n\tcontinue\n}\n",
			want: 1,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := len(findSilentScanSkips(c.src))
			if got != c.want {
				t.Fatalf("违规站点数 = %d，期望 %d（检查器可能没牙）", got, c.want)
			}
		})
	}
}

// 判据①（行为级）：helper 必须真的记 WARN、点名是哪一步、带上原始错误，
// 并返回「跳过这一行」。
func TestCompressionStatsScanFailureMustLogWarnAndNameStep(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	if !compressionStatsScanFailure("band", errors.New("boom")) {
		t.Fatal("行级扫描失败必须返回 true（= 跳过这一行）")
	}
	out := buf.String()
	if !strings.Contains(out, "band") {
		t.Fatalf("日志没有点名是哪一步：%s", out)
	}
	if !strings.Contains(out, "boom") {
		t.Fatalf("日志没有带上原始错误：%s", out)
	}
	if !strings.Contains(out, "level=WARN") {
		t.Fatalf("行级扫描失败必须记 WARN（不是 INFO/DEBUG）：%s", out)
	}
}

// 判据①的**反向自检**：三步必须**同形** ——
// 三个步骤各自的扫描失败都必须点名自己（不能三步都写同一个名字，
// 否则日志会指向错误的步骤）。
func TestCompressionStatsScanFailureMustNameEachStep(t *testing.T) {
	src, err := os.ReadFile("compression_stats.go")
	if err != nil {
		t.Fatalf("读不到 compression_stats.go：%v", err)
	}
	for _, step := range []string{"agg", "band", "bucket"} {
		if !strings.Contains(string(src), `compressionStatsScanFailure("`+step+`", err)`) {
			t.Fatalf("步骤 %q 的扫描失败没有点名自己（查 compressionStatsScanFailure(\"%s\", err)）", step, step)
		}
	}
}
