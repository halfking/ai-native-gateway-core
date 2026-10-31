package sessionv2mirror

// r1041 轮（2026-10-28）：录制同意「generation 防复活」门。
//
// ★ 起因：独立 verifier 审计发现 ——
//
//	domains/session/v2/bodies_bound.go:45-48
//	  if capturedGeneration <= 0 { return true }
//	而**两个生产写点都传 0**（bodies_writer.go:275 / :370）
//	⇒ CaptureRecordingGeneration **零调用方**
//	⇒ settings/recording_consent.go 承诺的「disable/clear 后在途请求
//	  不能回填正文」**根本没有生效** ⇒ clear 后旧请求仍可写回正文。
//
// ★⇒ 这是一个「**文档声称有、实际没有**」的隐私机制。
//
// ★ 本门盯的是**捕获点存在**且**捕获的是真实世代号**：
//   ① entryToProcessedRequest 必须把世代号写进 req（★ 捕获点）；
//   ② 写路径必须真的把它带到 RecordingConsentAllowsWrite（★ 传递链）。
//
// ★ 判据用【结构】而不是【行为】（★ 行为要真库 ⇒ CI 恒 SKIP ⇒ 等于没有）：
//   · 捕获点存在 ⇒ req.ConsentGeneration 非 0 时由 settings 决定；
//   · 传递链存在 ⇒ bodies_writer 的两处写点**不再传字面量 0**。
//
// ★★★ 关键的反向自证：★ 若有人把写点改回传 0（★ 也就是把缺陷放回去），
// ★★★ 本门必须红 —— ⇒ 这就是「防复活」这件事本身的可回归形态。

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestConsentGenerationIsCapturedAtRequestStart：★ 捕获点必须存在。
func TestConsentGenerationIsCapturedAtRequestStart(t *testing.T) {
	src := readMirrorFile(t, "hook.go")
	if !strings.Contains(src, "ConsentGeneration:") {
		t.Fatalf("★ entryToProcessedRequest 没有捕获 ConsentGeneration\n"+
			"  ⇒ 请求入口拿不到世代号 ⇒ 写点无从比对 ⇒ "+
			"「disable/clear 后在途请求不能回填正文」永远是空话")
	}
	if !strings.Contains(src, "RecordingConsentGeneration(") {
		t.Errorf("★ 捕获点没有调用 settings.RecordingConsentGeneration ⇒ " +
			"捕获到的不是真实世代号")
	}
}

// TestConsentGenerationReachesTheWriteGate：★ 传递链必须存在。
//
// ★ 判据：★ bodies_writer.go 的两处 `RecordingConsentAllowsWrite(...)` 调用
// ★ **不得**再传字面量 0 ⇒ ★ 那正是「从未生效」的形态。
func TestConsentGenerationReachesTheWriteGate(t *testing.T) {
	path := filepath.Join("..", "..", "domains", "session", "v2", "bodies_writer.go")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读 bodies_writer.go 失败：%v", err)
	}
	src := string(raw)

	calls := 0
	for _, line := range strings.Split(src, "\n") {
		if !strings.Contains(line, "RecordingConsentAllowsWrite(") {
			continue
		}
		calls++
		// ★ 去掉函数名本身再看参数 ⇒ 参数里出现 `, 0)` 即为「恒传 0」。
		rest := line[strings.Index(line, "RecordingConsentAllowsWrite(")+len("RecordingConsentAllowsWrite("):]
		if strings.Contains(rest, ", 0)") {
			t.Errorf("★ 第 %d 处 RecordingConsentAllowsWrite 仍传字面量 0 ⇒ "+
				"⇒ 「capturedGeneration <= 0 ⇒ 直接放行」⇒ "+
				"⇒ generation 校验**从未生效**（★ 这就是本轮修掉的缺陷本身）\n"+
				"  行：%s", calls, strings.TrimSpace(line))
		}
	}
	if calls == 0 {
		t.Fatalf("bodies_writer.go 里一处 RecordingConsentAllowsWrite 都没有 ⇒ 判据失效")
	}
}

// TestConsentGenerationFieldExistsOnRecords：★ 载体必须有这个字段。
func TestConsentGenerationFieldExistsOnRecords(t *testing.T) {
	for _, rel := range []string{
		filepath.Join("..", "..", "domains", "session", "v2", "bodies_writer.go"),
		filepath.Join("..", "..", "domains", "session", "v2", "session_writer_v2.go"),
	} {
		raw, err := os.ReadFile(rel)
		if err != nil {
			t.Fatalf("读 %s 失败：%v", rel, err)
		}
		if !strings.Contains(string(raw), "ConsentGeneration int") {
			t.Errorf("%s 里没有 `ConsentGeneration int` 字段 ⇒ 世代号无处安放", rel)
		}
	}
}

func readMirrorFile(t *testing.T, name string) string {
	t.Helper()
	raw, err := os.ReadFile(name)
	if err != nil {
		t.Fatalf("读 %s 失败：%v", name, err)
	}
	return string(raw)
}
