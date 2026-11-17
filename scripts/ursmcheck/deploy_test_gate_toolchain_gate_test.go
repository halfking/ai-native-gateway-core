//go:build !integration

package ursm_test

// 部署测试闸门的**工具链选择**门（2026-11-17 F3 残留地平线根修轮新增）。
//
// # 背景（一次真实的部署被卡死）
//
//	2026-11-17 17:4x 的 211 部署，闸门（scripts/deploy-test-gate.sh）判红：
//
//	  [test-gate] ★ 主模块：该箱没有产出可判读的结果 ⇒ native 跑法不可用
//	  [test-gate] ★ 原生 go 没有产出可判读的结果 ⇒ 落 docker 兜底
//	  --- FAIL: TestLiteRequestLogSink_PersistsFullJournal (0.00s)
//	      sqlite: 当前二进制以 CGO_ENABLED=0 构建，SQLite 驱动不可用
//	  FAIL github.com/kaixuan/llm-gateway-go/cmd/gateway  600.159s
//
//	★ 红的理由（SQLite 驱动不可用）与真因（工具链版本）**毫无关系**：
//	  闸门原来按「**第一个存在的 go**」挑工具链 ⇒ 本机命中 homebrew 的
//	  **go1.27.3**，而 go.mod 要求 **go >= 1.27.4** ⇒ 原生路径被 go.mod
//	  **直接拒绝** ⇒ 落 docker 兜底 ⇒ 而兜底**没设 CGO_ENABLED=1**
//	  （Dockerfile 里明确设了 + 装 gcc/musl-dev，正是为了 lite 模式的 SQLite）。
//
// # 本门钉住什么
//
//	① **按版本挑，而不是按存在性挑**（实跑验证，不是文本匹配）：
//	   用**假 go 二进制**（各自打印不同版本号）真跑
//	   scripts/lib/go-toolchain.sh 的挑选逻辑 ⇒
//	     · go.mod 要求 1.27.4、候选含 1.27.3/1.27.4/1.28.0 ⇒ **必须挑 1.27.4**
//	       （★ 挑**第一个满足**的，不是挑版本最高的 —— 否则会去挑 1.28.0，
//	        而 1.28.0 与 go.mod 的锁文件/工具链并不一定匹配）；
//	     · 全部候选都低于要求 ⇒ **必须挑不到**（交由 docker 兜底），
//	       而不是挑一个注定被 go.mod 拒绝的旧版 go。
//	② 版本比较与 go.mod 解析的边界（缺段当 0、垃圾输入回落 1.0）。
//	③ **防复发**：闸门脚本**不得**再出现「第一个存在的 go」那条选择循环，
//	   且 docker 兜底**必须**开 CGO_ENABLED=1（否则兜底路径天生产假红）。

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

const (
	goToolchainLib   = "../lib/go-toolchain.sh"
	deployTestGateSh = "../deploy-test-gate.sh"
)

// fakeGo 造一个**假 go 二进制**：它只回答 `go version`，打印指定版本号。
func fakeGo(t *testing.T, dir, name, version string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	script := "#!/bin/sh\n" +
		"if [ \"$1\" = \"version\" ]; then\n" +
		"  echo 'go version go" + version + " darwin/arm64'\n" +
		"  exit 0\n" +
		"fi\n" +
		"exit 0\n"
	if err := os.WriteFile(p, []byte(script), 0o755); err != nil {
		t.Fatalf("写假 go %s：%v", p, err)
	}
	return p
}

// runLib 执行库里的一个函数（真实 shell 实跑，不是文本匹配）。
func runLib(t *testing.T, script string) (string, string, error) {
	t.Helper()
	cmd := exec.Command("bash", "-c", ". "+goToolchainLib+"\n"+script)
	out, err := cmd.CombinedOutput()
	return string(out), "", err
}

// gateFuncBody 取 shell 脚本里某个函数的**函数体**（从 `name() {` 到行首的 `}`）。
//
// ★ 为什么需要它：shell 脚本里的判据若写成「全文 contains 某串」⇒ **注释里**
//
//	出现同名字符串就会把断言喂饱 ⇒ 变异（删掉真正的配置行）**照样绿**
//	⇒ 实测踩过（M2 假绿）⇒ 断言必须限定在**真正生效的那段代码**里。
func gateFuncBody(t *testing.T, text, name string) string {
	t.Helper()
	lines := strings.Split(text, "\n")
	start := -1
	for i, l := range lines {
		if strings.HasPrefix(strings.TrimSpace(l), name+"() {") {
			start = i
			break
		}
	}
	if start < 0 {
		t.Fatalf("脚本里找不到函数 %s", name)
	}
	for j := start + 1; j < len(lines); j++ {
		if strings.TrimSpace(lines[j]) == "}" {
			return strings.Join(lines[start:j+1], "\n")
		}
	}
	t.Fatalf("函数 %s 没有以行首 } 结束（判据形态变了？）", name)
	return ""
}

func TestGoToolchainPicksByVersionNotByExistence(t *testing.T) {
	dir := t.TempDir()
	old := fakeGo(t, dir, "go-1.27.3", "1.27.3")
	ok := fakeGo(t, dir, "go-1.27.4", "1.27.4")
	newer := fakeGo(t, dir, "go-1.28.0", "1.28.0")

	gomod := filepath.Join(dir, "go.mod")
	if err := os.WriteFile(gomod, []byte("module x\n\ngo 1.27.4\n"), 0o644); err != nil {
		t.Fatalf("写 go.mod：%v", err)
	}

	// ① 第一个**满足要求**的候选当��（不是版本最高的那个）。
	out, _, err := runLib(t, "llmgw_go_pick '"+gomod+"' '"+old+"' '"+ok+"' '"+newer+"'")
	if err != nil {
		t.Fatalf("跑 llmgw_go_pick：%v", err)
	}
	if got := strings.TrimSpace(out); got != ok {
		t.Errorf("挑中的 go 应该是**第一个满足 go.mod 要求**的 %s，实际=%q\n"+
			"★ 挑版本最高的（%s）会把工具链与 go.mod 的要求脱钩 ⇒ 下次 go.mod 抬版本时行为漂移。",
			ok, got, newer)
	}

	// ② 候选顺序不同 ⇒ 仍挑第一个满足的。
	out, _, _ = runLib(t, "llmgw_go_pick '"+gomod+"' '"+old+"' '"+newer+"' '"+ok+"'")
	if got := strings.TrimSpace(out); got != newer {
		t.Errorf("候选顺序变了就挑了别的：期望 %s（第一个满足要求的），实际=%q", newer, got)
	}

	// ③ 全部候选都低于要求 ⇒ **必须挑不到**（交 docker 兜底），
	//    绝不能挑一个注定被 go.mod 拒绝的旧版 go。
	out, _, _ = runLib(t, "llmgw_go_pick '"+gomod+"' '"+old+"'")
	if got := strings.TrimSpace(out); got != "" {
		t.Errorf("所有候选都低于 go.mod 要求时**必须挑不到**（落 docker 兜底），实际=%q\n"+
			"★ 挑一个旧版 go ⇒ 原生路径必然被 go.mod 拒绝 ⇒ 门的红理由与真因脱钩。", got)
	}
}

func TestGoToolchainVersionHelpers(t *testing.T) {
	dir := t.TempDir()

	// go.mod 解析：正常值 / 缺文件 / 垃圾行 ⇒ 回落 1.0。
	gomod := filepath.Join(dir, "go.mod")
	os.WriteFile(gomod, []byte("module x\n\ngo 1.27.4\n"), 0o644)
	out, _, _ := runLib(t, "llmgw_go_required_version '"+gomod+"'")
	if got := strings.TrimSpace(out); got != "1.27.4" {
		t.Errorf("go.mod 的 go 指令解析错：期望 1.27.4，实际=%q", got)
	}
	out, _, _ = runLib(t, "llmgw_go_required_version '"+filepath.Join(dir, "missing.mod")+"'")
	if got := strings.TrimSpace(out); got != "1.0" {
		t.Errorf("go.mod 缺失时必须回落 1.0，实际=%q", got)
	}
	junk := filepath.Join(dir, "junk.mod")
	os.WriteFile(junk, []byte("module x\n\ngo abc\n"), 0o644)
	out, _, _ = runLib(t, "llmgw_go_required_version '"+junk+"'")
	if got := strings.TrimSpace(out); got != "1.0" {
		t.Errorf("go.mod 的 go 指令不可解析时必须回落 1.0，实际=%q", got)
	}

	// 版本比较：逐段数值；缺段当 0。
	cases := []struct {
		a, b string
		want bool
	}{
		{"1.27.4", "1.27.4", true},
		{"1.27.4", "1.27.3", true},
		{"1.27.3", "1.27.4", false},
		{"1.28", "1.27.9", true},
		{"1.27", "1.27.0", true},
		{"2.0", "1.99.99", true},
		{"1.27.4", "1.27", true},
	}
	for _, c := range cases {
		out, _, _ := runLib(t, "if llmgw_go_version_ge "+c.a+" "+c.b+"; then echo GE; else echo LT; fi")
		got := strings.TrimSpace(out)
		want := "LT"
		if c.want {
			want = "GE"
		}
		if got != want {
			t.Errorf("llmgw_go_version_ge %s %s：期望 %s，实际=%s", c.a, c.b, want, got)
		}
	}

	// 工具链版本提取：假 go 打印 go1.27.4 ⇒ 1.27.4。
	p := fakeGo(t, dir, "go-fake", "1.27.4")
	out, _, _ = runLib(t, "llmgw_go_toolchain_version '"+p+"'")
	if got := strings.TrimSpace(out); got != "1.27.4" {
		t.Errorf("工具链版本提取错：期望 1.27.4，实际=%q", got)
	}
}

func TestDeployTestGateNoLongerPicksFirstExistingGo(t *testing.T) {
	raw, err := os.ReadFile(deployTestGateSh)
	if err != nil {
		t.Fatalf("读 %s：%v", deployTestGateSh, err)
	}
	text := string(raw)

	// ① 旧的那条「第一个存在的 go」选择循环不得复活。
	oldPick := regexp.MustCompile(`for c in "\$\(command -v go[^)]*\)"[^\n]*\n[^\n]*\n?[^\n]*if \[\[ -n "\$c"`)
	if oldPick.MatchString(text) {
		t.Errorf("%s 里又出现了「第一个存在的 go」那条选择循环 ⇒ 版本不够的 go 会被选中 ⇒ "+
			"原生路径被 go.mod 拒绝 ⇒ 落 docker 兜底 ⇒ 兜底若没 CGO 就会产假红（2026-11-17 实测）。\n"+
			"★ 判据必须走 scripts/lib/go-toolchain.sh（按 go.mod 的版本要求挑）。", deployTestGateSh)
	}

	// ② 必须引用新库。
	if !strings.Contains(text, "scripts/lib/go-toolchain.sh") {
		t.Errorf("%s 没有引用 scripts/lib/go-toolchain.sh ⇒ 工具链选择没有按版本判据走", deployTestGateSh)
	}
	if !strings.Contains(text, "llmgw_go_pick_detail") {
		t.Errorf("%s 没有用 llmgw_go_pick_detail 挑候选 ⇒ 版本判据没接上", deployTestGateSh)
	}

	// ③ docker 兜底必须开 CGO（与 Dockerfile 同款），否则 SQLite 测试必然假红。
	//
	// ★★ **判据必须限定在 run_docker 的函数体里**（2026-11-17 实测的坑）：
	//   写成「全文 contains CGO_ENABLED=1」是**弱判据** ⇒ 因为**注释里**也写着
	//   这几个字 ⇒ 把真正的 `-e CGO_ENABLED=1` 删掉，断言**照样绿**
	//   ⇒ 实测：变异 M2（删掉那行 env）⇒ 门没红 ⇒ **假绿**。
	//   ⇒ 这里改成「取 run_docker 的函数体 ⇒ 体内必须有 `-e CGO_ENABLED=1`」。
	body := gateFuncBody(t, text, "run_docker")
	if !strings.Contains(body, "-e CGO_ENABLED=1") {
		t.Errorf("%s 的 run_docker 函数体里没有 `-e CGO_ENABLED=1` ⇒ 兜底路径天生产假红\n"+
			"（lite 模式的 SQLite 测试会报「当前二进制以 CGO_ENABLED=0 构建」）——\n"+
			"★ Dockerfile 里明确设了 CGO_ENABLED=1 + 装 gcc/musl-dev，正是为了这个。\n"+
			"★ 判据只看函数体：全文 contains 会被注释里的同名字符串骗过（实测假绿）。",
			deployTestGateSh)
	}
	if !strings.Contains(body, "musl-dev") {
		t.Errorf("%s 的 run_docker 函数体里没有装 musl-dev ⇒ CGO_ENABLED=1 也编不出 sqlite（缺 musl 头文件）", deployTestGateSh)
	}

	// ④ 库文件必须在位。
	if _, err := os.Stat(goToolchainLib); err != nil {
		t.Errorf("%s 不存在：%v", goToolchainLib, err)
	}
}
