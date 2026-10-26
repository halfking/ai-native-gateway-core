//go:build !integration

package main

// §143.85 的门：★ 把「落法 B 的设计前提」钉成门，★ 让下一轮（★ 落地那一轮）能立刻知道
// ★ 【前提有没有被改掉】。
//
// ★★★★★★ 为什么这一轮要有这道门（★ 它是【设计门】，不是【实现门】）：
//
//	★ 本轮是【设计轮】⇒ ⇒ **零代码变更**（★ 只出设计 + 门 + 变异台）⇒ ⇒
//	★ ⇒ ⇒ 门【不能】断言「两个守卫已经存在」（★ 否则必红、且是假红）
//	★ ⇒ ⇒ 门断言的是【设计所依赖的【前提事实】】+【文档登记】：
//	  ① ★★★★★ 【共同汇合点】`loadFromLegacyDB` 必须【同时】被 L-1 与 L-2 调用
//	     ⇒ ⇒ 否则「一处守卫覆盖两个落点」的设计【前提消失】
//	  ② ★★★★★ 【充分性依据】`CreateV2` 必须【总是新生成 sessionID】
//	     ⇒ ⇒ 否则「AutoCreated ⇒ 库里绝无历史正文」【立刻失效】⇒ ⇒ 必须重做论证
//	  ③ ★★★★ 【ctx 载体可覆盖三个入口】三个触发入口必须【都把 ctx 传给 GetOrLoad】
//	  ④ ★★★★ 【依赖方向】`domains/hooks/compression` 不得 import `domains/streaming`
//	     ⇒ ⇒ 否则 ctx 载体放错包 ⇒ ⇒ 【循环依赖】
//	  ⑤ ★★★★ 【注入点唯一】注入必须挂在 `assignment.AutoCreated` 上；且只有 handler.go 这一条路
//	     ⇒ ⇒ messages.go / responses.go 不用 sessionCompressor ⇒ ⇒ 不需要改
//	  ⑥ ★★★ 【文档登记】§143.85 段必须登记两个守卫 / 收益口径 / 回滚开关
//	  ⑦ ★★ 【交接面】LESSONS §276 与 AGENTS R128 必须存在
//
// ★★★★ 【★ 设计门 ≠ 实现门 ★】：
//	★ 本门【不能】证明「落地后的代码是对的」⇒ ⇒ **落地那一轮必须另写【实现门】**
//	★ （★ 断言两个守卫真的在、★ 断言短路返回 `(nil,nil,nil)`、★ 断言 `ShouldSkipLegacyLoad`
//	★   的【sessionID 相等】判定）⇒ ⇒ 两道门都要有，★ 不能互相顶替。

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	section14385Runbook = "docs/06-deployment/04-runbooks/runbooks/2026-10-04-ursm-write-amplification-measurement.md"
	section14385Lessons = "LESSONS.md"
	section14385Agents  = "AGENTS.md"

	section14385CacheLocal  = "domains/hooks/compression/session_cache.go"
	section14385CacheShared = "domains/hooks/compression/session_cache_shared.go"
	section14385Compressor  = "domains/hooks/compression/session_compressor.go"
	section14385Recovery    = "domains/hooks/compression/recovery_coordinator.go"
	section14385Forensics   = "domains/sessionforensics/replay.go"
	section14385SessionV2   = "domains/session/session_v2.go"
	section14385Handler     = "domains/streaming/handler.go"
	section14385Messages    = "domains/streaming/messages.go"
	section14385Responses   = "domains/streaming/responses.go"
	section14385Compression = "domains/hooks/compression"
)

func repoRoot14385(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func readRepoFile14385(t *testing.T, rel string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(repoRoot14385(t), rel))
	if err != nil {
		t.Fatalf("读取 %s 失败：%v", rel, err)
	}
	return string(raw)
}

// codeLines14385 返回一个文件的【可执行行】（★ 剥掉整行注释与行尾注释）。
//
// ★★★ 为什么必须剥注释（★ 本轮又踩到一次）：
//
//	★ `domains/hooks/compression/sanitize_info.go` 的【注释】里写着
//	★ `domains/streaming/request_log_pipeline.go:1498` ⇒ ⇒ 若判据④ 不剥注释
//	★ ⇒ ⇒ 「compression 不得 import streaming」这条会【假红】。
//	★ ⇒ ⇒ ★★ 与 §143.84 同款纪律：★ 判据必须【分面】（★ 代码面剥注释、★ 登记面用原文）。
func codeLines14385(t *testing.T, rel string) []string {
	t.Helper()
	var out []string
	for _, line := range strings.Split(readRepoFile14385(t, rel), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "//") {
			continue
		}
		if idx := strings.Index(line, "//"); idx >= 0 {
			trimmed = strings.TrimSpace(line[:idx])
		}
		if trimmed == "" {
			continue
		}
		out = append(out, trimmed)
	}
	return out
}

func count14385(t *testing.T, rel, needle string) int {
	t.Helper()
	n := 0
	for _, line := range codeLines14385(t, rel) {
		if strings.Contains(line, needle) {
			n++
		}
	}
	return n
}

// TestSection14385LegacyLoadIsSharedChokePoint（★ 判据①）
//
// ★★★★★ 【共同汇合点】`loadFromLegacyDB` 必须【同时】被两个落点调用：
//
//	★ L-1 = `session_cache.go` 的 `loadFromDB` 链尾
//	★ L-2 = `session_cache_shared.go` 的 legacy 校验分支
//
// ★ ⇒ ⇒ ★★ 「在 `loadFromLegacyDB` 入口加一个守卫 ⇒ ⇒ 一处覆盖两个落点」这个设计
// ★ ⇒ ⇒ ★★ 的【前提】必须仍然成立 ⇒ ⇒ 一旦有人把其中一条路拆开 ⇒ ⇒ 门要红。
func TestSection14385LegacyLoadIsSharedChokePoint(t *testing.T) {
	local := count14385(t, section14385CacheLocal, "loadFromLegacyDB(")
	shared := count14385(t, section14385CacheShared, "loadFromLegacyDB(")

	if shared != 1 {
		t.Errorf("★ %s 里 `loadFromLegacyDB(` 出现 %d 次（期望 1）—— "+
			"★ ★★ 本轮实测：★ L-2（共享路径 legacy 校验分支）恰好 1 处调用"+
			"★ ⇒ ⇒ ★★ 若它被改掉（★ 拆开 / 改名 / 换成别的实现）⇒ ⇒ "+
			"★ ⇒ ⇒ 「一处守卫覆盖两个落点」的设计【前提消失】⇒ ⇒ §143.85 必须重做",
			section14385CacheShared, shared)
	}
	if local < 2 {
		t.Errorf("★ %s 里 `loadFromLegacyDB(` 只出现 %d 次（期望 ≥2：★ 定义 + 链尾调用）—— "+
			"★ ★★ 共同汇合点被拆开 ⇒ ⇒ §143.85.1 的 F1 失效",
			section14385CacheLocal, local)
	}
}

// TestSection14385AutoCreatedImpliesFreshSessionID（★ 判据②）
//
// ★★★★★ 【充分性依据】`CreateV2` 必须【总是新生成 sessionID】：
//
//	★ 「AutoCreated ⇒ 库里绝无该会话的历史正文」是整个落法 B 的【充分性】依据
//	★ ⇒ ⇒ ★★ 一旦 `CreateV2` 改成「可复用已有会话」⇒ ⇒ 短路会【丢掉真实历史】
//	★ ⇒ ⇒ ⇒⇒ 必须立刻变红 ⇒ ⇒ 强制重做论证。
func TestSection14385AutoCreatedImpliesFreshSessionID(t *testing.T) {
	n := count14385(t, section14385SessionV2, "sessionID := generateGwSessionID()")
	if n != 1 {
		t.Errorf("★ %s 里 `sessionID := generateGwSessionID()` 出现 %d 次（期望 1）—— "+
			"★ ★★★★★ 本轮实测：★ `CreateV2` 总是【新生成】会话 ID"+
			"★ ⇒ ⇒ ★★ 这是「AutoCreated ⇒ 库里绝无历史正文」的【充分性】依据"+
			"★ ⇒ ⇒ ★★ 若它改成可复用已有会话 ⇒ ⇒ 短路会丢掉真实历史"+
			"★ ⇒ ⇒ ⇒⇒ 门必须红 ⇒ ⇒ §143.85.5 的论证要重做",
			section14385SessionV2, n)
	}
}

// TestSection14385AllTriggerEntriesPassContext（★ 判据③）
//
// ★★★★ 【ctx 载体可覆盖三个触发入口】三个入口必须【都把 ctx 传给 `GetOrLoad`】：
//
//	★ 压缩器（`session_compressor.go`）／恢复协调器（`recovery_coordinator.go`）／取证回放（`replay.go`）
//
// ★ ⇒ ⇒ ★★ ctx 下传是落法 B 的【载体前提】⇒ ⇒ 任何一处改成不传 ctx ⇒ ⇒ 门要红。
func TestSection14385AllTriggerEntriesPassContext(t *testing.T) {
	for _, rel := range []string{section14385Compressor, section14385Recovery, section14385Forensics} {
		if count14385(t, rel, ".GetOrLoad(ctx") == 0 {
			t.Errorf("★ %s 里没有 `.GetOrLoad(ctx` —— "+
				"★ ★★ 本轮实测：★ 三个触发入口都把 ctx 传给 GetOrLoad"+
				"★ ⇒ ⇒ ★★ ctx 是落法 B 的【载体】⇒ ⇒ 一旦某处不传 ctx"+
				"★ ⇒ ⇒ ⇒⇒ 那个入口就【拿不到标志】⇒ ⇒ 覆盖率下降 ⇒ ⇒ §143.85.4 的矩阵要重算",
				rel)
		}
	}
}

// TestSection14385CompressionMustNotImportStreaming（★ 判据④）
//
// ★★★★ 【依赖方向】`domains/hooks/compression` 不得 import `domains/streaming`：
//
//	★ ctx 载体的定义与读取都放在 compression 包 ⇒ ⇒ 依赖方向是 streaming → compression
//	★ ⇒ ⇒ ★★ 一旦反向依赖 ⇒ ⇒ 【循环依赖】⇒ ⇒ 载体必须换包、§143.85.2 段① 要重做。
func TestSection14385CompressionMustNotImportStreaming(t *testing.T) {
	root := repoRoot14385(t)
	base := filepath.Join(root, section14385Compression)
	err := filepath.Walk(base, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		raw, rerr := os.ReadFile(path)
		if rerr != nil {
			return nil
		}
		for _, line := range strings.Split(string(raw), "\n") {
			trimmed := strings.TrimSpace(line)
			if trimmed == "" || strings.HasPrefix(trimmed, "//") {
				continue
			}
			if idx := strings.Index(line, "//"); idx >= 0 {
				trimmed = strings.TrimSpace(line[:idx])
			}
			if trimmed == "" {
				continue
			}
			if strings.Contains(trimmed, "domains/streaming") {
				rel, _ := filepath.Rel(root, path)
				t.Errorf("★ %s 里有可执行代码 import `domains/streaming`（%q）—— "+
					"★ ★★★★ 本轮实测：★ compression 包【不得】反向依赖 streaming"+
					"★ ⇒ ⇒ ★★ ctx 载体（★ `domains/hooks/compression/auto_created.go`）"+
					"★ ⇒ ⇒ 依赖方向必须是 streaming → compression"+
					"★ ⇒ ⇒ ⇒⇒ 一旦反向 ⇒ ⇒ 【循环依赖】⇒ ⇒ §143.85.2 段① 必须重做",
					rel, trimmed)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("遍历 %s 失败：%v", base, err)
	}
}

// TestSection14385InjectionPointIsAutoCreatedAndUnique（★ 判据⑤）
//
// ★★★★ 【注入点唯一且必须挂在正确的标志上】：
//
//	★ 注入必须发生在 `if assignment.AutoCreated {` 里（★ 而不是 `Resumed` / `ShouldPersist`）
//	★ ⇒ ⇒ ★★ 因为【自动新建】才是「库里必然没有历史正文」的那一个 ⇒ ⇒ 挂错标志 ⇒ ⇒ 误伤
//	★ ⇒ ⇒ ★★ 且【只有 handler.go 这一条路需要改】（★ messages.go / responses.go 不用 sessionCompressor）。
func TestSection14385InjectionPointIsAutoCreatedAndUnique(t *testing.T) {
	if n := count14385(t, section14385Handler, "if assignment.AutoCreated {"); n != 1 {
		t.Errorf("★ %s 里 `if assignment.AutoCreated {` 出现 %d 次（期望 1）—— "+
			"★ ★★ 本轮实测：★ 注入点【恰好 1 处】，★ 且必须挂在【AutoCreated】上"+
			"★ ⇒ ⇒ ★★ 挂到 `Resumed` / `ShouldPersist` 上 ⇒ ⇒ 会把【复用来的会话】当成新会话"+
			"★ ⇒ ⇒ ⇒⇒ 那样短路就会【丢掉真实历史】⇒ ⇒ 必须红",
			section14385Handler, n)
	}
	for _, rel := range []string{section14385Messages, section14385Responses} {
		if count14385(t, rel, "sessionCompressor") != 0 {
			t.Errorf("★ %s 里出现了 `sessionCompressor` —— "+
				"★ ★★ 本轮实测：★ messages.go / responses.go 都【不用】sessionCompressor"+
				"★ ⇒ ⇒ ★★ 它们是独立 handler、★ 不经过 serveWithExecutor"+
				"★ ⇒ ⇒ ⇒⇒ 若它们开始用压缩器 ⇒ ⇒ 【注入点就不再唯一】⇒ ⇒ §143.85.2 段② 必须重做",
				rel)
		}
	}
}

// TestSection14385RunbookRegistersDesign（★ 判据⑥）
//
// ★★★ 【文档登记】§143.85 段必须登记：★ 两个守卫、★ 收益口径、★ 零误伤护栏、★ 回滚开关。
func TestSection14385RunbookRegistersDesign(t *testing.T) {
	runbook := readRepoFile14385(t, section14385Runbook)
	i := strings.Index(runbook, "### §143.85")
	if i < 0 {
		t.Fatal("★ runbook 里找不到 §143.85 段 —— ★ 本轮的设计必须落在仓内")
	}
	seg := runbook[i:]
	for _, needle := range []string{
		"G-1",                             // ★ 守卫 1（loadFromDB 入口）
		"G-2",                             // ★ 守卫 2（loadFromLegacyDB 入口）
		"ShouldSkipLegacyLoad",            // ★ 零误伤护栏（sessionID 相等判定）
		"session_cache_skip_auto_created", // ★ 回滚开关名
		"16.0",                            // ★ 只做 G-2 的收益口径（49 分钟窗口）
		"17.7",                            // ★ G-1+G-2 的收益口径（49 分钟窗口）
		"语义等价",                            // ★ 正确性论证
		"回滚",                              // ★ 回滚方案
	} {
		if !strings.Contains(seg, needle) {
			t.Errorf("★ §143.85 段里没有 %q —— "+
				"★ ★★ 本轮的两个守卫、★ 收益口径、★ 零误伤护栏与★ 回滚开关必须可复核"+
				"★ ⇒ ⇒ ★★ 属主要按这些数字拍板 ⇒ ⇒ 缺一项就少一个决策依据",
				needle)
		}
	}
}

// TestSection14385HandoffSurfacesExist（★ 判据⑦）
func TestSection14385HandoffSurfacesExist(t *testing.T) {
	if !strings.Contains(readRepoFile14385(t, section14385Lessons), "## §276") {
		t.Error("★ LESSONS.md 里没有 §276 —— " +
			"★ ⇒ ⇒ 「设计门 ≠ 实现门」与「前提事实必须被门守住」这两条通用式必须落进去")
	}
	if !strings.Contains(readRepoFile14385(t, section14385Agents), "R128") {
		t.Error("★ AGENTS.md 里没有 R128 —— " +
			"★ ⇒ ⇒ 本轮的四段形态（载体/注入/守卫/开关）必须落在交接段（★ 否则下一轮会重新设计一遍）")
	}
}
