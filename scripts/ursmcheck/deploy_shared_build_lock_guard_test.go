package ursm_test

// 12h 审计轮（2026-10-23）· 门：docker 部署路径必须与 systemd 路径共用同一把
// 【共享构建锁】；且 fail-closed 退出守卫必须真的 fail-closed。
//
// 被测对象：scripts/deploy-seamless-docker.sh
//
// ── 为什么这道门存在（2026-10-23 12h 审计轮查出的真实缺陷）────────────────
//
//	① 【并发锁控制 / 资源竞争】
//	   2026-10-22 的「211 docker 部署自动构建前端」把 `pnpm run build` 引入了
//	   **共享 checkout**（web/dist、web-mobile/dist、web/public/menu-config.json）。
//	   ⇒ 而它只拿了【每目标部署锁】（kx-llm-gateway-deploy-<target>.lock）。
//	   ⇒ 而 systemd 路径（deploy-seamless.sh，245/154）早就知道共享 checkout 的写操作
//	      必须单独串行 ⇒ 它额外拿了【共享构建锁】（kx-llm-gateway-build.lock），
//	      deploy-lib/lock.sh 的注释原文就是「A separate shared build lock protects
//	      checkout mutations」。
//	   ⇒ ⇒ 两把锁**互不排斥** ⇒ 一次 211 部署与一次 245 部署并发时，两个进程
//	      会在**同一个 web/dist 里同时写** ⇒ ⇒ 产出可能是**半截的 dist**
//	      ⇒ ⇒ 半截 dist 随 tar 上树 ⇒ ⇒ 线上是坏掉的 SPA。
//	   ⇒ 这正是那句被写在代码里的断言「放在拿锁之后：两个并发部署不能同时构建
//	      同一份 dist」**不成立**的原因：它只对【同目标的两个 --via】成立。
//
//	② 【异常错误的处理】
//	   「bash set -eu + 未绑定变量 ⇒ 退出码 0」这个被写进注释的「实测矩阵」
//	   **不成立**：没有 EXIT trap 时它退出 1；**装了 trap 之后**退出码才可能是 0，
//	   而且**取决于 bash 版本**（macOS 自带 bash 3.2.57 ⇒ 0；bash 5.2.0 ⇒ 1）。
//	   ⇒ ⇒ 真正让结果确定的是【完成标记守卫】（fail-closed），不是「保住 $?」。
//	   ⇒ ⇒ 本门用【真跑 bash】把这条性质钉住，而不是复述注释里的断言。
//
// ── 四道判据 ────────────────────────────────────────────────────────────
//
//	① 【顺序判据】（定位式，不是子串式）：
//	   `lock_acquire_build` 的**可执行调用点**必须早于 `dd_build_web_dist` 的调用点，
//	   后者必须早于 tar 同步点。⇒ 顺序倒过来 ⇒ 门红。
//	② 【释放判据】：EXIT trap 必须释放共享构建锁（否则一次失败就把锁永久留下）。
//	③ 【行为判据】（★ 真跑 bash 子进程）：把脚本里**真实的**dd_exit_guard_finalize
//	   抽出来跑四种死法，断言「未走到完成标记 ⇒ 退出码非 0」且「真实失败码不被洗掉」。
//	   ⇒ 这条判据锚在【行为】上 ⇒ 注释怎么改都不影响它 ⇒ 不会退化成「同源自证」。
//	④ 【版本戳判据】：dirty 判定必须排除三个版本产物（否则见审计文档问题 ③）。

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

const ddDeployScript = "scripts/deploy-seamless-docker.sh"

// ── 工具 ────────────────────────────────────────────────────────────────

func repoRootDeployGuard(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatalf("解析仓库根失败: %v", err)
	}
	return root
}

func readDeployScript(t *testing.T) string {
	t.Helper()
	root := repoRootDeployGuard(t)
	b, err := os.ReadFile(filepath.Join(root, ddDeployScript))
	if err != nil {
		t.Fatalf("读取 %s 失败: %v", ddDeployScript, err)
	}
	return string(b)
}

// stripQuoted 去掉行内单引号与双引号包裹的内容。
// ★ 用途：让「只在字符串字面量里被提到」的名字**不算**调用点。
//
//	（例如 warn "...lock_acquire_build..." 这类说明性文本。）
func stripQuoted(s string) string {
	var b strings.Builder
	var quote byte
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case quote == 0 && (c == '\'' || c == '"'):
			quote = c
		case quote != 0 && c == quote:
			quote = 0
		case quote == 0:
			b.WriteByte(c)
		}
	}
	return b.String()
}

// executableCallOffset 返回「第一条可执行地调用了 name 的行」的偏移（1 基行号）。
//
// ★ 刻意排除三类行：
//
//	① 注释行（`#` 开头）；
//	② 函数定义行（`name() {`）；
//	③ 仅在字符串字面量里提到 name 的行。
//
// ⇒ 判据钉的是「调用发生在哪里」，而不是「这个词出现在哪里」。
func executableCallOffset(src, name string) (int, string, bool) {
	lines := strings.Split(src, "\n")
	re := regexp.MustCompile(`\b` + regexp.QuoteMeta(name) + `\b`)
	for i, ln := range lines {
		trimmed := strings.TrimSpace(ln)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if strings.HasPrefix(trimmed, name+"()") { // 函数定义
			continue
		}
		if !re.MatchString(ln) {
			continue
		}
		if !re.MatchString(stripQuoted(ln)) { // 只在字符串里被提到
			continue
		}
		return i + 1, ln, true
	}
	return 0, "", false
}

// tarSyncOffset 返回 tar 同步点（`_tar_create` 数组定义）的 1 基行号。
func tarSyncOffset(src string) (int, bool) {
	for i, ln := range strings.Split(src, "\n") {
		if strings.Contains(ln, "_tar_create=(") {
			return i + 1, true
		}
	}
	return 0, false
}

// extractBashFunction 从脚本源码里抽出 `name() { ... }` 的完整函数体。
// ★ 用括号配平来定位函数结尾，而不是「找到下一个 }」—— 注释/字符串里的 } 不算数。
func extractBashFunction(t *testing.T, src, name string) string {
	t.Helper()
	start := strings.Index(src, name+"() {")
	if start < 0 {
		t.Fatalf("脚本里找不到函数定义 %s() {", name)
	}
	depth := 0
	seen := false
	for i := start; i < len(src); i++ {
		switch src[i] {
		case '{':
			depth++
			seen = true
		case '}':
			depth--
			if seen && depth == 0 {
				return src[start : i+1]
			}
		}
	}
	t.Fatalf("函数 %s 的花括号未配平", name)
	return ""
}

// ── 判据①：共享构建锁必须在【构建之前】拿，且【构建之后、tar 之前】上树 ──

func TestDeployDockerPathAcquiresSharedBuildLockBeforeBuilding(t *testing.T) {
	src := readDeployScript(t)

	acquireLine, acquireText, ok := executableCallOffset(src, "lock_acquire_build")
	if !ok {
		t.Fatalf("★ %s 里没有可执行地调用 lock_acquire_build ⇒ "+
			"docker 路径仍在【不拿共享构建锁】的状态下写共享 checkout（web/dist）⇒ "+
			"⇒ 与 systemd 路径（deploy-seamless.sh，245/154）并发时两个进程会同时写"+
			"同一份 dist ⇒ ⇒ 产出可能是半截的 ⇒ ⇒ 半截 dist 会被 tar 上树",
			ddDeployScript)
	}
	t.Logf("lock_acquire_build 调用点：第 %d 行 —— %s", acquireLine, strings.TrimSpace(acquireText))

	buildLine, buildText, ok := executableCallOffset(src, "dd_build_web_dist")
	if !ok {
		t.Fatalf("★ %s 里没有可执行地调用 dd_build_web_dist（前端构建）—— 判据前提不成立", ddDeployScript)
	}
	t.Logf("dd_build_web_dist 调用点：第 %d 行 —— %s", buildLine, strings.TrimSpace(buildText))

	tarLine, ok := tarSyncOffset(src)
	if !ok {
		t.Fatalf("★ %s 里找不到 tar 同步点（_tar_create=( ）—— 判据前提不成立", ddDeployScript)
	}
	t.Logf("tar 同步点：第 %d 行", tarLine)

	if !(acquireLine < buildLine) {
		t.Errorf("★ 共享构建锁取得点（第 %d 行）晚于前端构建调用点（第 %d 行）⇒ "+
			"⇒ 构建发生在【没锁】的状态下 ⇒ ⇒ 与并发部署同时写同一份 dist",
			acquireLine, buildLine)
	}
	if !(buildLine < tarLine) {
		t.Errorf("★ 前端构建调用点（第 %d 行）晚于 tar 同步点（第 %d 行）⇒ "+
			"⇒ 打进 tar 的不是刚构建出来的那份产物", buildLine, tarLine)
	}
}

// ── 判据②：EXIT trap 必须释放共享构建锁 ────────────────────────────────

func TestDeployDockerPathReleasesSharedBuildLockOnExit(t *testing.T) {
	src := readDeployScript(t)

	var traps []string
	for _, ln := range strings.Split(src, "\n") {
		tr := strings.TrimSpace(ln)
		if strings.HasPrefix(tr, "trap ") && strings.Contains(tr, "EXIT") {
			traps = append(traps, tr)
		}
	}
	if len(traps) == 0 {
		t.Fatalf("★ %s 里没有任何 EXIT trap ⇒ 退出码与锁清理都没有兜底", ddDeployScript)
	}

	// 至少有一道 trap 必须同时做三件事：保住退出码 / 释放每目标锁 / 释放共享构建锁。
	// ⚠ 释放共享构建锁的入口有两个名字：本脚本自己定义的 `dd_release_build_lock`
	//   与 deploy-lib 里的 `lock_release_build` ⇒ 两个都认。
	found := false
	for _, tr := range traps {
		hasRC := strings.Contains(tr, "rc=$?")
		hasLocal := strings.Contains(tr, "lock_release_local")
		hasBuild := strings.Contains(tr, "dd_release_build_lock") || strings.Contains(tr, "lock_release_build")
		if hasRC && hasLocal && hasBuild {
			found = true
			t.Logf("合格 trap: %s", tr)
		}
	}
	if !found {
		t.Errorf("★ 没有一道 EXIT trap 同时做「保住退出码 + 释放每目标锁 + 释放共享构建锁」⇒ "+
			"⇒ 共享构建锁会在一次失败部署后被【永久留下】⇒ ⇒ 后续所有部署都卡死在"+
			"「共享构建锁被占用」⇒ ⇒ 现场表现是「部署莫名失败」，而现场没有任何"+
			"痕迹指向真正的原因。现有 trap: %v", traps)
	}
}

// ── 判据③：fail-closed 守卫必须真的 fail-closed（★ 真跑 bash）─────────

type guardCase struct {
	name      string
	completed string // 部署完成标记
	death     string // 触发死亡的那一行
	wantZero  bool   // 期望退出码为 0？
}

func TestDeployExitGuardFailsClosed(t *testing.T) {
	src := readDeployScript(t)
	fn := extractBashFunction(t, src, "dd_exit_guard_finalize")

	bashBin, err := exec.LookPath("bash")
	if err != nil {
		t.Skipf("本机找不到 bash，跳过行为判据: %v", err)
	}

	cases := []guardCase{
		{
			name:      "正常成功且走到完成标记",
			completed: "1",
			death:     "true",
			wantZero:  true,
		},
		{
			name:      "未走到完成标记就退出（fail-closed 的核心）",
			completed: "0",
			death:     "true",
			wantZero:  false,
		},
		{
			name:      "未绑定变量（2026-10-22 事故的真实死法）",
			completed: "0",
			death:     `echo "$__nonexistent_var_for_guard_test__"`,
			wantZero:  false,
		},
		{
			name:      "命令失败",
			completed: "0",
			death:     "false",
			wantZero:  false,
		},
	}

	// ★ 跑【多个 bash 解释器】：这个性质的取值与 bash 版本相关
	//   （macOS 自带 bash 3.2 与 bash 5.x 在「未绑定变量」这一种死法上
	//   于 EXIT trap 入口处的 $? 取值不同）⇒ 只测一个解释器等于没测。
	interpreters := []string{bashBin}
	for _, extra := range []string{"/bin/bash", "/usr/local/bin/bash", "/opt/homebrew/bin/bash"} {
		if extra == bashBin {
			continue
		}
		if _, err := os.Stat(extra); err == nil {
			interpreters = append(interpreters, extra)
		}
	}

	for _, interp := range interpreters {
		for _, c := range cases {
			interp, c := interp, c
			t.Run(interp+"_"+c.name, func(t *testing.T) {
				script := strings.Join([]string{
					"set -eu",
					"DD_TARGET=guard-test",
					"DD_DEPLOY_COMPLETED=" + c.completed,
					fn,
					`trap 'rc=$?; dd_exit_guard_finalize "$rc"' EXIT`,
					c.death,
					"",
				}, "\n")

				cmd := exec.Command(interp, "-c", script)
				cmd.Stderr = os.Stderr
				err := cmd.Run()
				rc := 0
				if err != nil {
					if ee, ok := err.(*exec.ExitError); ok {
						rc = ee.ExitCode()
					} else {
						t.Fatalf("跑 %s 失败: %v", interp, err)
					}
				}
				t.Logf("interpreter=%s case=%q ⇒ exit code = %d", interp, c.name, rc)

				if c.wantZero && rc != 0 {
					t.Errorf("★ %s / %q：期望退出码 0，实得 %d ⇒ 守卫把正常成功误判成失败",
						interp, c.name, rc)
				}
				if !c.wantZero && rc == 0 {
					t.Errorf("★ %s / %q：期望退出码非 0（fail-closed），实得 0 ⇒ "+
						"⇒ 这正是 2026-10-22 事故的形态（部署其实啥都没干，却报成功）",
						interp, c.name)
				}
			})
		}
	}
}

// ── 判据④：版本戳的 dirty 判定不得把「版本文件」当成源码漂移 ────────────
//
//	2026-10-22 修过一次「版本戳恒带 -dirty」：原因是前端构建会重写
//	web/public/menu-config.json ⇒ 工作树必脏 ⇒ 每次部署都带 -dirty。
//	⇒ 但 bump-version.sh（systemd 路径的 1/9 步）会改 VERSION / version.json /
//	   web/public/version.json **且从不提交** ⇒ ⇒ 那三个文件长期留在工作树里
//	⇒ ⇒ 若 dirty 判定把它们算进去 ⇒ ⇒ 修好的告警会在下一次 245/154 部署后
//	⇒ ⇒ 重新变成「恒带 -dirty」。
func TestDeployDirtyStampExcludesVersionArtifacts(t *testing.T) {
	src := readDeployScript(t)

	var dirtyLine string
	for _, ln := range strings.Split(src, "\n") {
		if strings.Contains(ln, "status --porcelain") {
			dirtyLine = ln
			break
		}
	}
	if dirtyLine == "" {
		t.Fatalf("★ %s 里找不到 dirty 判定（git status --porcelain）—— 判据前提不成立", ddDeployScript)
	}
	t.Logf("dirty 判定行: %s", strings.TrimSpace(dirtyLine))

	// ★ 2026-10-30（24h 审计第 8 轮）：menu-config.json 也在豁免表里 ——
	//   它是本脚本自己后续步骤重新生成的构建再生产物（exported_at 时间戳），
	//   与 VERSION 家族同性质。
	for _, f := range []string{"VERSION", "version.json", "web/public/version.json", "web/public/menu-config.json"} {
		if !strings.Contains(dirtyLine, "(exclude)"+f) {
			t.Errorf("★ dirty 判定没有排除 %s ⇒ ⇒ 构建产物留下的未提交改动"+
				"⇒ ⇒ 下一次部署又会带上 -dirty ⇒ ⇒ 「版本戳不再恒带 -dirty」的修复"+
				"⇒ ⇒ 从另一扇门被原样顶回来。dirty 判定行: %s", f, strings.TrimSpace(dirtyLine))
		}
	}
}
