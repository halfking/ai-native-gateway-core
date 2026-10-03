package sessionv2mirror

import (
	"bytes"
	"log/slog"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/domains/hooks/observability/telemetry"
)

// captureSlog 把 slog 默认 logger 接到 buffer 上，返回 buffer 与还原函数。
func captureSlog(t *testing.T) (*bytes.Buffer, func()) {
	t.Helper()
	buf := &bytes.Buffer{}
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	return buf, func() { slog.SetDefault(prev) }
}

// TestPersistHookNilWriterIsNotSilent（§9.93，2026-10-03）
//
// 一个 nil writer 几乎总是**初始化失败**，不是「有意关闭镜像」。
// 修复前 `PersistHook(nil)` 返回一个空函数：不写、不记、不打日志，
// **在可观测性上与「影子写开关被关掉」完全等价**。
//
// §9.59–§9.92 连续四轮追查一批「v1 有、会话族无孪生」的请求时，
// 这条路径是所有候选里**唯一无法用任何现有证据排除**的那类
// ——因为它不留任何痕迹。这道门把它变成启动日志里的一件事。
//
// 变异：把 slog.Error 那一行删掉 ⇒ 本门红。
func TestPersistHookNilWriterIsNotSilent(t *testing.T) {
	buf, restore := captureSlog(t)
	defer restore()

	// 关键：必须真的构造，而不是只读源码。
	hook := PersistHook(nil)
	if hook == nil {
		t.Fatal("PersistHook(nil) 返回了 nil —— 调用方会 panic，而不是静默")
	}
	// 调用它，确认「静默」的是写入而不是「调用即崩」
	hook(&telemetry.RequestLogEntry{RequestID: "probe-nil-writer"})

	out := buf.String()
	if out == "" {
		t.Fatal("PersistHook(nil) 一条日志都没打 —— 这正是本门要防的形态：" +
			"镜像被静默关掉，在日志里与「正常关闭」无法区分")
	}
	if !strings.Contains(out, "SILENTLY disabled") {
		t.Errorf("有日志但没点明这是静默失效，实际输出：%s", out)
	}
	// 必须是 ERROR：它不是一条信息，是「整个进程生命周期的镜像写入都没了」
	if !strings.Contains(out, "ERROR") {
		t.Errorf("这条应当是 ERROR 级（整进程镜像失效），实际：%s", out)
	}
}

// TestNilProcessedRequestPathIsNotSilent（§9.93）
//
// `if req == nil { return }` 此前是一次**逐条**丢弃且零痕迹：
// 没有日志、没有指标、没有 outbox 行。
//
// 为什么用结构断言而不是行为断言：当前 `entryToProcessedRequest` 只在
// `entry == nil || sessionID == ""` 时返回 nil，而 sessionID 为空要求
// `entry == nil`（入口已挡）**或** SyntheticSessionID 返回 ""（非 nil entry 时不可能）。
// ⇒ 这条路径对合法输入**实际不可达**，无法用行为测试触发。
//
// 那它为什么还值得钉：**它是未来最容易被无意改宽的一道门。**
// 只要有人放宽了 sessionID 的计算，这里会从「几乎不可能」变成
// 「静默丢一大片」——而那时没有任何门会红，除了这一条。
//
// 所以判据钉的是**这条分支不许静默**，不是「它一定会发生」。
//
// 变异：把该分支里的 slog.Warn 删掉 ⇒ 本门红。
func TestNilProcessedRequestPathIsNotSilent(t *testing.T) {
	data, err := os.ReadFile("hook.go")
	if err != nil {
		t.Fatalf("读不到 hook.go: %v", err)
	}
	src := string(data)

	idx := strings.Index(src, "if req == nil {")
	if idx < 0 {
		t.Fatal("hook.go 里找不到 `if req == nil {` —— 判据失效，它现在什么也没验")
	}
	// 取该分支的函数体：从 if 起到与之配对的 "return" 之后
	rest := src[idx:]
	end := strings.Index(rest, "\n\t\t}")
	if end < 0 {
		t.Fatal("找不到 `if req == nil` 分支的结束位置 —— hook.go 结构变了，请更新本门")
	}
	body := rest[:end]

	if !strings.Contains(body, "slog.Warn") {
		t.Error("`if req == nil` 分支里没有任何日志 —— 这是一条逐条丢弃且零痕迹的路径。" +
			"一旦有人放宽 sessionID 的计算，它会静默丢一大批而无人知晓")
	}
	// 逐条丢弃的日志必须带 request_id，否则无法定位到具体那一条
	if !strings.Contains(body, "request_id") {
		t.Error("该分支的日志没带 request_id —— 逐条丢弃的告警不带定位键等于没打")
	}
}

// TestSilentMirrorDropPathsAreAllDocumented（§9.93）把 PersistHook 里**所有**的
// 静默 return 列成一张清单。目的不是禁止静默（大部分静默是**按设计**的），
// 而是：每条静默路径都必须能说出它的理由；说不出理由的，就是查不出来的那条。
//
// §9.59–§9.92 连续四轮追一个查不出来的原因，根子就在**丢弃面没有清单**——
// 所以「查不出来」和「没发生」无法区分。
func TestSilentMirrorDropPathsAreAllDocumented(t *testing.T) {
	data, err := os.ReadFile("hook.go")
	if err != nil {
		t.Fatalf("读不到 hook.go: %v", err)
	}
	src := string(data)
	start := strings.Index(src, "func PersistHook(")
	if start < 0 {
		t.Fatal("hook.go 里找不到 PersistHook —— 判据失效，它现在什么也没验")
	}
	hookSrc := src[start:]

	// 清单用 `|` 分隔而不是空格：判据文本本身含空格
	// （`!entry.Success && !isTerminalFailure(entry)`）。第一版用 Fields() 切，
	// 被截成 "!Success"，门当场报「清单漂移」——这正是它第一次跑就抓到的自身缺陷。
	const manifest = "" +
		"entry == nil|入口防御\n" +
		"!entry.Success && !isTerminalFailure(entry)|按设计：幂等约束，in_progress 不可镜像（§9.59.6）\n" +
		"IsProbeSyntheticSession(entry)|按设计：探针不进 mirror（R51，§9.91.3）\n" +
		"!synthetic && telemetry.IsInternalAutoEntry(entry)|按设计：网关内部回环不是用户 turn\n" +
		"!shadowWriteEnabled()|运维主动关开关（settings_kv 可查）\n" +
		"req == nil|§9.93 本轮已补日志\n"

	conds := []string{}
	for _, line := range strings.Split(strings.TrimSpace(manifest), "\n") {
		parts := strings.SplitN(line, "|", 2)
		if len(parts) != 2 {
			t.Fatalf("清单行格式错（必须 `判据|理由`）：%q", line)
		}
		cond, reason := parts[0], parts[1]
		conds = append(conds, cond)
		if strings.TrimSpace(reason) == "" {
			t.Errorf("清单里的 %q 没写理由 —— 一条说不出理由的静默丢弃，就是查不出来的那条", cond)
		}
		if !strings.Contains(hookSrc, cond) {
			t.Errorf("清单登记的静默路径在 hook.go 里已不存在：%q（理由：%s）\n"+
				"要么代码改了（请更新清单），要么清单已漂移（它现在给人虚假的安全感）", cond, reason)
		}
	}

	// 每一条**纯丢弃** return 都必须落在清单里。
	// 「纯丢弃」= 该 if 分支体里除了 return 什么都没有。
	// 第一版把 `if !shadowWriteDispatchAsync { run(); return }` 误判为丢弃——
	// 那是先执行 run() 的控制流分支。判据必须看**整个分支体**。
	lines := strings.Split(hookSrc, "\n")
	reReturn := regexp.MustCompile(`^\s*return\s*$`)
	for i, ln := range lines {
		if !reReturn.MatchString(ln) {
			continue
		}
		cond := ""
		pure := false
		for j := i - 1; j >= 0; j-- {
			trimmed := strings.TrimSpace(lines[j])
			if !strings.HasPrefix(trimmed, "if ") {
				continue
			}
			cond = trimmed
			indent := len(lines[j]) - len(strings.TrimLeft(lines[j], "\t"))
			pure = true
			for k := j + 1; k < len(lines); k++ {
				ind := len(lines[k]) - len(strings.TrimLeft(lines[k], "\t"))
				if ind < indent {
					break
				}
				b := strings.TrimSpace(lines[k])
				// 同缩进的 `}` 是本分支的收尾，必须停在这里。
				// ★第二版漏报的根因：不看这个收尾，扫描会一路走到**下一条同缩进的
				// if**，把它的判据当成本分支的语句，于是 pure 被误判为 false，
				// 新增的静默丢弃**不空一行就检不出来**。变异实测：
				// 插入块后留空行⇒红；不留空行⇒绿。同一个变异，两种结果。
				if b == "}" && ind <= indent {
					break
				}
				if b != "" && b != "}" && !strings.HasPrefix(b, "return") {
					pure = false
					break
				}
			}
			break
		}
		if cond == "" || !pure {
			continue
		}
		matched := false
		for _, c := range conds {
			if strings.Contains(cond, c) {
				matched = true
				break
			}
		}
		if !matched {
			t.Errorf("hook.go:%d 发现一条**未登记**的纯静默丢弃：%s\n"+
				"每条静默 return 都必须能说出理由；说不出的就是 §9.59–§9.93 查了四轮的那个洞。", i+1, cond)
		}
	}
}
