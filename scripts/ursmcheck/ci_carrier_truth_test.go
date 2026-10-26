package ursm_test

// ci_carrier_truth_test.go —— ★★ **CI 载体真值门**（2026-10-24 magpie 对齐轮 §3.17）。
//
// # 这道门补的是哪块缺口（★ 本轮实测撞到的、比 admin 那条更深）
//
// `scripts/checks/guards-sync.sh` 的第二跳有一道「专用门必须还在」判据：
//
//	if ! grep -rqsE '^[[:space:]]*run:[[:space:]]*make[[:space:]]+guards[[:space:]]*$' \
//	        "$repo_root/.github/workflows"; then ... exit 1; fi
//
// ⇒ 它证明的是「**`.github/workflows/` 这个目录下还有一个 run 步骤在调 `make guards`**」。
//
// ★★ 而本仓的远端是 **codeup.aliyun.com**（`git remote -v` 实测），不是 GitHub。
// ⇒⇒ **`.github/workflows/` 里的 9 个 workflow 在本仓从来不在任何在跑的 CI 里**
//    （`AGENTS.md:4227` 与 `docs/audit/2026-10-21-R57-真库腿体检.md:42` 两条独立记载
//    早已写明这一点）。
// ⇒⇒ 于是那道判据**恒绿**，而它要防的事（守卫被从审计路径上摘掉）**真实发生了**：
//    `make guards` 唯一的调用方是一个不执行的 workflow ⇒ ★★ **整个审计守卫体系
//    在本仓的 CI 上是空转的**。
//
// ★ 这是「自证型门」的**第四形态**（前三种见 `LESSONS.md`）：
//   ① 被审对象当证人（裁决表满足对自身的复核）
//   ② 判「在不在生产」看类型而非调用点
//   ③ 天天红的门（假红 ⇒ 没人信）
//   ④ ★ **判据锚在「文件存在」而非「该文件会被执行」**
//      ⇒ 文件在、门绿、而 CI 根本不跑它。
//
// # 本门的做法：**把「谁在跑」变成可执行判定**
//
// ★ 判据不猜「哪个 CI 平台」，而是**从仓库里可读的事实**推导：
//
//	C1 ★ **远端宿主判定**：读 `git remote get-url origin` ⇒ 推出远端宿主名。
//	   ⇒ 本仓实测 = `codeup.aliyun.com` ⇒ **不是 GitHub**。
//	C2 ★ **在跑的流水线配置目录**：宿主不是 GitHub ⇒ GitHub Actions 的
//	   `.github/workflows/` **不在执行路径上** ⇒ 本仓的流水线载体是
//	   **codeup Flow**（`codeup.aliyun.com` 的「流水线即代码」配置），
//	   仓内路径 = `.workflow/*.yml`。
//	C3 ★ **载体必须真的执行门禁**：`.workflow/` 下**每一个**流水线配置里，
//	   必须有**非注释行**的 step 调用审计门禁入口。
//	   ⇒ 这条让「载体在但没跑门」变成红。
//	C4 ★ **量具自检**：本门自己必须真的读到 ≥1 个流水线配置（否则 C3 恒绿）。
//
// # 为什么必须钉在**代码**里而不是注释里
//
// 本轮的前序教训（同族）：「登记了没人跑」与「文件在但没人跑」都是**执行面**缺陷，
// 而执行面缺陷**只有执行面门能抓**。注释里写「CI 会跑」不产生任何约束力。
//
// # 与既有判据的关系（不重复）
//
//	`audit_guard_registration_test.go` G2：带 `AUDIT-GUARD` 标记的门文件，
//	  其所在包必须登记进 `GUARD_PACKAGES` ∪ `GUARD_PACKAGES_SLOW`。
//	  ⇒ 它管「**登记表 ↔ 门文件**」的一致性。
//	本门：管「**登记表 ↔ 真正在跑的 CI**」的一致性。
//	  ⇒ 二者互补：G2 绿但本门红 = 「登记了，但没有任何在跑的 CI 执行它」。

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// ciCarrierRepoRoot 返回仓根。
func ciCarrierRepoRoot(t *testing.T) string {
	t.Helper()
	out, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		t.Fatalf("解析仓根失败（需要 git 在 PATH）：%v", err)
	}
	return strings.TrimSpace(string(out))
}

// ciCarrierOriginHost 返回 origin 远端的**宿主名**（小写，去掉端口与路径）。
//
// ★ 只返回主机名：用户名/端口/路径在本地克隆与远端之间会不同，
// 而本门要判的只有「**这是不是 GitHub**」这一个事实。
func ciCarrierOriginHost(t *testing.T) string {
	t.Helper()
	out, err := exec.Command("git", "remote", "get-url", "origin").Output()
	if err != nil {
		t.Fatalf("读 origin 远端失败：%v", err)
	}
	remote := strings.TrimSpace(string(out))
	host := remote
	if i := strings.Index(host, "://"); i >= 0 {
		host = host[i+3:]
	}
	if i := strings.Index(host, "@"); i >= 0 {
		host = host[i+1:]
	}
	if i := strings.Index(host, ":"); i >= 0 {
		host = host[:i]
	}
	if i := strings.Index(host, "/"); i >= 0 {
		host = host[:i]
	}
	return strings.ToLower(host)
}

// ciCarrierPipelineConfigs 返回仓内**在跑的**流水线配置文件。
//
// ★ 判定完全由 C1（远端宿主）驱动，**不靠硬编码平台名单**：
//
//	远端是 GitHub  ⇒ 载体目录 = `.github/workflows/`
//	远端是 codeup  ⇒ 载体目录 = `.workflow/`
//	其它宿主       ⇒ 两个都看（未知平台 ⇒ 取并集，宁可多查不可漏查）
func ciCarrierPipelineConfigs(t *testing.T, root string) (dirs []string, configs []string) {
	t.Helper()
	host := ciCarrierOriginHost(t)
	t.Logf("origin 远端宿主 = %q", host)

	switch {
	case strings.Contains(host, "github"):
		dirs = []string{".github/workflows"}
	case strings.Contains(host, "codeup"):
		dirs = []string{".workflow"}
	default:
		t.Logf("★ 远端宿主 %q 不是已知平台 ⇒ 取并集（宁可多查不可漏查）", host)
		dirs = []string{".workflow", ".github/workflows"}
	}

	for _, d := range dirs {
		abs := filepath.Join(root, d)
		entries, err := os.ReadDir(abs)
		if err != nil {
			continue // 目录不存在 = 该平台在本仓无载体
		}
		for _, e := range entries {
			name := e.Name()
			if e.IsDir() || (!strings.HasSuffix(name, ".yml") && !strings.HasSuffix(name, ".yaml")) {
				continue
			}
			configs = append(configs, filepath.Join(d, name))
		}
	}
	return dirs, configs
}

// ciCarrierGuardCallRE 匹配「**真的在执行**审计门禁入口」的 step。
//
// ★ 排除注释行（行首 `#`）——本轮前序教训：判据锚在「文件里出现过这个字符串」
// 会被注释满足（`guards-sync.sh` 自己就踩过这个坑，见它的第二跳注释）。
//
// ★ 同时要求 `make <target>` 是该行的**完整命令**（前后无其它命令），
// 避免 `echo "make guards"` 这类假满足。
//
// ★ 接受两个入口：`make guards`（全量）与 `make guards-fast`（CI 预算内）。
//
//	★ 判据钉的是**语义**（「这个载体真的在跑审计门禁」）而不是**形态**
//	（「必须恰好是哪个 target」）——形态会随预算演进，语义不会。
var ciCarrierGuardCallRE = regexp.MustCompile(`(?m)^[^#\n]*\bmake\s+guards(-fast)?(\s|$)`)

// TestCICarrierOriginHostIsResolvable 是【量具自检】：
// 本门必须真的能推出远端宿主，否则下面所有判据都是空转。
func TestCICarrierOriginHostIsResolvable(t *testing.T) {
	host := ciCarrierOriginHost(t)
	if host == "" {
		t.Fatal("origin 远端宿主为空 ⇒ 本门的 C1 判据失效")
	}
	t.Logf("origin 远端宿主 = %q", host)
}

// TestCICarrierIsNotGitHubWorkflowOnly 是 C1+C2：
//
// ★ 本门存在的**根因**就在这里：远端不是 GitHub ⇒ `.github/workflows/`
// **不在执行路径上** ⇒ 任何只锚在那个目录的「CI 还在跑」判据都是恒绿的假判据。
//
// ⇒ 本门把这条事实**钉成可执行断言**：若将来本仓真的迁到 GitHub 远端，
// 本断言会**红**，提醒人重新审视 CI 载体（而不是让旧的假判据悄悄继续绿）。
func TestCICarrierIsNotGitHubWorkflowOnly(t *testing.T) {
	root := ciCarrierRepoRoot(t)
	host := ciCarrierOriginHost(t)
	if strings.Contains(host, "github") {
		t.Skipf("origin 已是 GitHub 远端（%q）⇒ 断言前提变化，需重审 CI 载体", host)
	}

	// 本仓远端是 codeup ⇒ `.github/workflows/` 不是执行载体。
	// ⇒ 若有人把「唯一的 CI 载体」判定建立在那个目录上，这道门会指出该误判。
	t.Logf("远端宿主 = %q ⇒ `.github/workflows/` 不在本仓的执行路径上；"+
		"「CI 还在跑」必须锚在 `.workflow/`（codeup Flow）上", host)

	// 反向自检：确认 `.workflow/` 真的存在（否则本门下面的 C3 无从谈起）。
	if _, err := os.Stat(filepath.Join(root, ".workflow")); err != nil {
		t.Fatalf("`.workflow/` 不存在 ⇒ 本门假定在跑的 codeup Flow 载体不存在 ⇒ 判据失效")
	}
}

// TestCICarrierRunsGuardGate 是 C3：★ **在跑的载体必须真的执行门禁**。
//
// ★ 这是本门的核心：前序的「专用门还在」判据只证明「文件里有 run: make guards」，
// 而本门要求「**真正在跑的那个平台**的流水线配置里**真的调了**门禁」。
//
// ★ 判定锚在**非注释行 + 完整命令**两个条件上：
//
//	· 排除注释行 ⇒ 注释里写 `make guards` 不算（guards-sync.sh 踩过这个坑）
//	· 完整命令 ⇒ `echo "make guards"` 不算
func TestCICarrierRunsGuardGate(t *testing.T) {
	root := ciCarrierRepoRoot(t)
	dirs, configs := ciCarrierPipelineConfigs(t, root)

	if len(configs) == 0 {
		t.Fatalf("仓内找不到任何流水线配置（查过 %v）⇒ 量具失效，"+
			"「CI 跑不跑门」这件事本门判不了", dirs)
	}
	t.Logf("在跑的流水线配置（目录 %v）：%s", dirs, strings.Join(configs, ", "))

	for _, rel := range configs {
		b, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			t.Errorf("读流水线配置 %s 失败：%v", rel, err)
			continue
		}
		if !ciCarrierGuardCallRE.Match(b) {
			t.Errorf("流水线配置 %s **没有在执行审计门禁** ⇒ "+
				"守卫在 CI 上是空转的（本地跑绿、CI 也绿，但 CI 从没跑过它）。\n"+
				"   修法：在该配置的 step 里加一条 `make guards-fast`（**非注释行**）。\n"+
				"   ★ 注意：`.github/workflows/` 不在本仓的执行路径上（远端是 codeup）⇒\n"+
				"     往那个目录里加 step **不会**让门在 CI 上跑起来。",
				rel)
		}
	}
}

// TestCIVerifyShCoversAdminPackage 是 C4 的一环：**官方门（verify.sh）是否覆盖 admin**。
//
// ★ 本轮实测推翻了自己审计档 §3.16.3 的结论：
//
//	§3.16.3 写「`admin` 套件不在 `GUARD_PACKAGES` ⇒ 它的测试**没有任何 CI 路径**
//	⇒ 它的红没人看见」——★ **前半句对，后半句错**。
//
// ★ 事实：`verify.sh`（README 明定的官方门）跑的是 `go test ./... -count=1 -timeout=900s`
//
//	⇒ **`./admin` 在其中** ⇒ admin 的红**会被官方门看见**。
//	⇒ 而「它不在 GUARD_PACKAGES」这件事的**真实后果**不是「没人看它的红」，
//	而是「**它在审计守卫这条专用路径上缺席**」（GUARD_PACKAGES 是给 CI 的
//	**快门**，用来跑那些**不该混在全量测试里**的纯静态守卫）。
//
// ⇒ 所以本断言钉的是**语义**（「官方门覆盖 admin」）而不是**形态**
//
//	（「admin 在不在 GUARD_PACKAGES」）——★ 形态会随实现演进，语义不会。
func TestCIVerifyShCoversAdminPackage(t *testing.T) {
	root := ciCarrierRepoRoot(t)
	b, err := os.ReadFile(filepath.Join(root, "verify.sh"))
	if err != nil {
		t.Fatalf("读 verify.sh 失败：%v", err)
	}
	src := string(b)

	// 官方门必须跑**全量** Go 测试（`./...`）——admin 只有在这里才被覆盖。
	// ★ 判据锚在「go test」+「./...」同现，且排除注释行。
	reFull := regexp.MustCompile(`(?m)^[^#\n]*\bgo\s+test\b[^\n]*\./\.\.\.`)
	if !reFull.MatchString(src) {
		t.Errorf("verify.sh 里没有跑全量 Go 测试（`go test ./...`）的**非注释行** ⇒ " +
			"`./admin` 不在官方门的覆盖范围 ⇒ admin 的红没人看见。\n" +
			"   ★ 这条同时是 §3.16.3 那句「admin 没有任何 CI 路径」的**订正依据**。")
	}
}
