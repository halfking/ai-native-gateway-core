package ursm_test

// §143.104 的量具门：**parse_pg_kills.py 必须折叠 conmon `stderr P` 分块**。
//
// # 这道门补的是哪块缺口（§143.104 实测）
//
// 2026-10-23 的 ctrlog 单文件 **529,695,442 bytes**（此前正常日 20–90 MB）。
// 字节会计（§143.104 pass4）拆出真相：
//
//	no-prefix(F 之外的行):  n=46,390  bytes=270.7MB   ← 其实是 `stderr P ` 分块
//	rec-start:              n=36,097  bytes=8.9MB
//	continuation:           n=2,374,805 bytes=250.1MB
//	  其中「无 open record 被丢弃」: n=1,263,347 bytes=142.2MB
//
// ⇒ 旧解析器 `CONTAINER_PREFIX = "stderr F "` 只认 F 行 ⇒ **P 分块把 open record
//   打断**（被当成「不是 PG 行 ⇒ 记录结束」），其后所有续行成孤儿（142.2MB 丢弃）
//   ⇒ ⇒ **当日大多数字节对审计不可见**，而且这是**静默的**。
//
// # P 分块的来源（为什么以前从没炸过）
//
// 巨型单行语句（>journald 行分块 ~8KiB）：omp 会话的 session_turns 镜像 INSERT
// 携带 MB 级内联 prompt（MCP 工具目录）⇒ PG 慢查日志整条打印 ⇒ conmon 拆成
// 一串 `stderr P` 块。普通日没有这种语句 ⇒ 量具看起来一直是好的。
//
// # 门的三条腿
//
// ① **行为腿**：合成夹具（含 P 分块 + 块内藏一条「PG 记录长相」的行）直接跑
//   **仓内真解析器**（门与量具同一个实现，零漂移）⇒ 断言：
//   分块内容进得了归属指纹、块内假记录**不分裂**记录、对照组 kill 不受影响。
// ② **源码腿**：解析器源码必须钉住 `stderr P ` 的处理（防退化成 F-only）。
// ③ **变异腿**：把 P 分支废掉（副本上做）⇒ 行为断言**必须翻红**
//   ⇒ 「门有牙」不依赖真实数据碰巧出现。
//
// ★ 变异在**副本**上做（R98 模式）⇒ 仓内真文件零改动。

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const pgKillParser143105 = "../../scripts/audit/parse_pg_kills.py"

// stderrPFixture143105 构造合成 ctrlog，覆盖 P 分块的**两种**真实形态：
//
//	rec1  user_request kill（对照组，无分块）
//	rec2  LOG duration 2500ms 的 INSERT；P 分块里**块中部**藏一行
//	        「2026-…ERROR:  fake inner record」⇒ 粘贴进语句体的日志长相文本
//	        必须折叠（§143.26 污染家族），不得分裂记录
//	rec3  ★ first-chunk-P：整条记录的第一物理行本身超大 ⇒ 记录起点就在
//	        P 分块的**位置 0** ⇒ 必须开新记录（否则巨语句被并进上一条，
//	        252 实测 6 条 mark UPDATE 被撑到 100KB+ 就是这个误折叠）
//	rec4  statement_timeout kill，STATEMENT 体跨两个 P 分块
const stderrPFixture143105 = `2026-10-23T08:00:00.000000001+08:00 stderr F 2026-10-23 08:00:00.001 CST [111] ERROR:  canceling statement due to user request
2026-10-23T08:00:00.000000002+08:00 stderr F 2026-10-23 08:00:00.001 CST [111] STATEMENT:  SELECT 1 FROM control_table
2026-10-23T08:01:00.000000001+08:00 stderr F 2026-10-23 08:01:00.001 CST [222] LOG:  duration: 2500.000 ms  statement: INSERT INTO public.session_turns_hot (a) SELECT '{"giant":
2026-10-23T08:01:00.000000002+08:00 stderr P 	, '{"more sql before the paste"}' 2026-10-23 08:01:00.002 CST [222] ERROR:  fake inner record that must NOT start a record
2026-10-23T08:01:00.000000003+08:00 stderr P 	...giant chunk two of the insert line...
2026-10-23T08:01:00.000000004+08:00 stderr F 	...final piece of the line}
2026-10-23T09:00:00.000000001+08:00 stderr P 2026-10-23 09:00:00.001 CST [333] LOG:  duration: 1800.000 ms  statement: INSERT INTO public.assets ( kind, metadata ) VALUES ('{"asset":
2026-10-23T09:00:00.000000002+08:00 stderr P 	...asset chunk two, still mid-line...
2026-10-23T09:00:00.000000003+08:00 stderr F 	...asset final piece}
2026-10-23T08:01:00.000000005+08:00 stderr F 2026-10-23 08:01:00.003 CST [222] ERROR:  canceling statement due to statement timeout
2026-10-23T08:01:00.000000006+08:00 stderr F 2026-10-23 08:01:00.003 CST [222] STATEMENT:  INSERT INTO public.session_turns_hot (a) SELECT '{"giant":
2026-10-23T08:01:00.000000007+08:00 stderr P 	, '{"pad"}' 2026-10-23 08:01:00.004 CST [222] ERROR:  fake inner record two
2026-10-23T08:01:00.000000008+08:00 stderr P 	...statement chunk two continues here...
`

// stderrPProbe143105 直接 import 仓内解析器模块并 dump iter_records 的结果：
// 门与量具**同一个实现**（import，不是重写），断言落在**记录级**而不是
// 解析器的打印格式上（打印指纹截断在 90 字符，够不着分块内容）。
const stderrPProbe143105 = `
import importlib.util, json, sys
spec = importlib.util.spec_from_file_location("ppk", sys.argv[1])
m = importlib.util.module_from_spec(spec); spec.loader.exec_module(m)
recs = list(m.iter_records(sys.argv[2]))
print(json.dumps({"n": len(recs),
                  "recs": [[r[2], len(r[3])] for r in recs],
                  "joined": "\n@@REC@@\n".join(r[3] for r in recs)}))
`

func runParser143105(t *testing.T, parserPath string, fixture string) string {
	t.Helper()
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skipf("python3 不存在 —— 解析器行为腿无法执行（显式 SKIP，理由：门依赖 python3）")
	}
	dir := t.TempDir()
	log := filepath.Join(dir, "ctr.log")
	if err := os.WriteFile(log, []byte(fixture), 0o644); err != nil {
		t.Fatalf("写夹具失败：%v", err)
	}
	probe := filepath.Join(dir, "probe.py")
	if err := os.WriteFile(probe, []byte(stderrPProbe143105), 0o644); err != nil {
		t.Fatalf("写探针失败：%v", err)
	}
	out, err := exec.Command(python, probe, parserPath, log).CombinedOutput()
	if err != nil {
		t.Fatalf("解析器探针执行失败：%v\n%s", err, out)
	}
	return string(out)
}

// TestParsePgKillsFoldsStderrPartialChunks —— 行为腿：跑**仓内真解析器**的
// iter_records，记录级断言。
func TestParsePgKillsFoldsStderrPartialChunks(t *testing.T) {
	out := runParser143105(t, pgKillParser143105, stderrPFixture143105)

	// ① 夹具恰好 6 条记录：ERROR+STATEMENT（对照 kill）、LOG 慢查（跨块）、
	//    LOG 巨记录（first-chunk-P）、ERROR(timeout)+STATEMENT（跨块）。
	if !strings.Contains(out, `"n": 6`) {
		t.Fatalf("iter_records 记录数不是 6 —— first-chunk-P 巨记录没有独立成条，或夹具/解析器形态变了：\n%s", out)
	}
	// ② 记录级别序列必须精确：两个 LOG 各自成条（巨记录没并进慢查记录）。
	if !strings.Contains(out, `[["ERROR", `) || !strings.Contains(out, `["STATEMENT", `) ||
		strings.Count(out, `["LOG", `) != 2 {
		t.Fatalf("记录级别序列不对（应为 ERROR/STATEMENT/LOG/LOG/ERROR/STATEMENT）——\n%s", out)
	}
	// ③ P 分块内容必须折叠进记录（旧解析器静默丢弃）。
	for _, chunk := range []string{
		"giant chunk two of the insert line",
		"final piece of the line",
		"asset chunk two, still mid-line",
		"asset final piece",
		"statement chunk two continues here",
	} {
		if !strings.Contains(out, chunk) {
			t.Fatalf("stderr P 分块内容 %q 没有折叠进记录：\n%s", chunk, out)
		}
	}
	// ④ 块中部「PG 记录长相」的行不得分裂记录：它只能作为语句体的一部分出现。
	if !strings.Contains(out, "fake inner record that must NOT start a record") {
		t.Fatalf("块内假记录内容丢了（既不该分裂也不该丢）：\n%s", out)
	}
	// ⑤ 逐记录核对：没有一条记录以 fake 开头（mid-chunk 粘贴不得篡位）。
	//    ⚠ 探针输出是 JSON：记录文本里的换行是**转义的两字符** `\n`，
	//    逐记录切分后每段以它开头 —— 不剥掉它，这条 HasPrefix 永远假
	//    （第一次写就是空转通过，被本轮自检抓住）。
	for _, rec := range strings.Split(out, "@@REC@@")[1:] {
		rec = strings.TrimPrefix(rec, "\\n")
		if strings.HasPrefix(rec, "2026-10-23 08:01:00.002") ||
			strings.HasPrefix(rec, "2026-10-23 08:01:00.004") {
			t.Fatalf("块中部假记录以记录身份出现 —— P 分块被当成了记录起点：\n%s", out)
		}
	}
	// ⑥ first-chunk-P 巨记录必须是独立 LOG 记录，且以自己的 duration 行开头
	//    （被并进上一条 = 252 上 mark UPDATE 被撑到 100KB+ 的误折叠形态）。
	idx := strings.Index(out, "INSERT INTO public.assets")
	if idx < 0 {
		t.Fatalf("first-chunk-P 巨记录的 assets 语句丢了：\n%s", out)
	}
	segStart := strings.LastIndex(out[:idx], "@@REC@@")
	if segStart < 0 {
		t.Fatalf("assets 记录找不到记录分隔符：\n%s", out)
	}
	recText := strings.TrimPrefix(out[segStart+len("@@REC@@"):], "\\n")
	recText = strings.TrimPrefix(recText, "\n")
	if !strings.HasPrefix(recText, "duration: 1800.000 ms") {
		t.Fatalf("first-chunk-P 巨记录没有以自己的 duration 行开头 —— 被并进了上一条记录：\n%s", out)
	}
}

// TestParsePgKillsPartialHandlingSourcePinned —— 源码腿：P 处理不许被删。
func TestParsePgKillsPartialHandlingSourcePinned(t *testing.T) {
	raw, err := os.ReadFile(pgKillParser143105)
	if err != nil {
		t.Fatalf("读解析器源码失败：%v", err)
	}
	src := string(raw)
	for _, needle := range []string{
		`CONTAINER_PREFIX_PARTIAL = "stderr P "`,
		"rec[3] += chunk",
	} {
		if !strings.Contains(src, needle) {
			t.Fatalf("解析器源码缺 %q —— stderr P 折叠被退化掉了（529MB 日志对审计不可见的根因）", needle)
		}
	}
}

// TestParsePgKillsPartialMutationHasTeeth —— 变异腿：废掉 P 分支必须翻红。
func TestParsePgKillsPartialMutationHasTeeth(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skipf("python3 不存在 —— 变异腿无法执行（显式 SKIP）")
	}
	raw, err := os.ReadFile(pgKillParser143105)
	if err != nil {
		t.Fatalf("读解析器源码失败：%v", err)
	}
	needle := "idx_p = raw.find(CONTAINER_PREFIX_PARTIAL)"
	if !strings.Contains(string(raw), needle) {
		t.Fatalf("解析器源码里找不到 P 探测行 %q —— 变异锚点失效，先修锚点再谈变异", needle)
	}
	// 变异：P 探测恒 -1 ⇒ P 行被当成「不是 PG 行」⇒ 记录被打断（旧缺陷形态）。
	mutated := strings.Replace(string(raw), needle, "idx_p = -1  # MUTATED: partial chunks dropped", 1)
	if mutated == string(raw) {
		t.Fatalf("变异没有生效 —— 替换串与原文相同")
	}
	dir := t.TempDir()
	mutPath := filepath.Join(dir, "parse_pg_kills_mutated.py")
	if err := os.WriteFile(mutPath, []byte(mutated), 0o644); err != nil {
		t.Fatalf("写变异副本失败：%v", err)
	}
	out := runParser143105(t, mutPath, stderrPFixture143105)
	// 变异后：分块内容必须从记录里消失（旧缺陷形态：P 行打断记录 ⇒ 内容被丢）。
	if strings.Contains(out, "giant chunk two of the insert line") {
		t.Fatalf("变异体 M1（P 分支废掉）仍然折叠了分块内容 —— 行为腿对这族缺陷没有牙，先修行为腿")
	}
}

// TestParsePgKillsPartialRecordStartMutationHasTeeth —— 变异腿 M2：把
// 「first-chunk-P 的位置 0 记录起点识别」废掉 ⇒ 巨记录并进上一条 ⇒ 记录数与
// assets 独立性断言必须翻红。
func TestParsePgKillsPartialRecordStartMutationHasTeeth(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skipf("python3 不存在 —— 变异腿无法执行（显式 SKIP）")
	}
	raw, err := os.ReadFile(pgKillParser143105)
	if err != nil {
		t.Fatalf("读解析器源码失败：%v", err)
	}
	needle := "m = PG_REC.match(chunk)"
	if !strings.Contains(string(raw), needle) {
		t.Fatalf("解析器源码里找不到 P 分块记录起点识别 %q —— 变异锚点失效，先修锚点", needle)
	}
	mutated := strings.Replace(string(raw), needle, "m = None  # MUTATED: first-chunk-P starts folded away", 1)
	if mutated == string(raw) {
		t.Fatalf("变异没有生效")
	}
	dir := t.TempDir()
	mutPath := filepath.Join(dir, "parse_pg_kills_mutated2.py")
	if err := os.WriteFile(mutPath, []byte(mutated), 0o644); err != nil {
		t.Fatalf("写变异副本失败：%v", err)
	}
	out := runParser143105(t, mutPath, stderrPFixture143105)
	// 变异后：assets 巨记录被并进前一条 ⇒ 记录数 6→5，且 assets 语句的记录
	// 不再以自己的 duration 行开头。
	if strings.Contains(out, `"n": 6`) {
		t.Fatalf("变异体 M2（first-chunk-P 起点识别废掉）记录数仍是 6 —— 行为腿对误折叠没有牙")
	}
}
