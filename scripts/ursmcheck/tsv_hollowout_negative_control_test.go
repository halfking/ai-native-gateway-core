package ursm_test

// tsv_hollowout_negative_control_test.go —— 2026-10-28 R86 轮（掏空法门的**负控臂**）
//
// # 这道门要钉住的事
//
// ★ 承接 §19.25.19.10 优先级 1 ①：**掏空法门自己的「量具有牙」够不够？**
//
//	★ R85 轮的自检用的是**合成读方**（永远绿 / 读到 0 行就红）
//	⇒ ⇒ 它只证明了「**分类器**能把红与绿分开」
//	⇒ ⇒ **真实读方的「有牙」只由行为判据间接证明**
//
// ★ ⇒ 本臂走的是**同一条**行为判据代码路径（`hollowVerifyEntry`），
// ★★ 且只差**一个因子**：「读方的红条件还在不在」
// ⇒ ⇒ 期望结论必须**翻转**：
//
//	★ 「未改瞎的读方」 ⇒ 掏空 ⇒ 红  ⇒ 分类 = red-caught（★ 有牙）
//	★ 「被改瞎的读方」 ⇒ 掏空 ⇒ 绿 ⇒ 分类 = still-green（★ 洞）
//
// ★★ 这条翻转才是「**门必须能发现『读方被改瞎了』**」的**行为化证据**：
// ★★ 合成读方证明不了它（★ 合成读方本来就是假的）。
//
// # 两个臂（★ 两种读方形态各一个）
//
//	① **gotest 臂**：★ 用 `go build -overlay <file>` 把**真实**测试文件的一份
//	   **改瞎副本**盖到编译输入里
//	   ⇒ ⇒ ★★ **仓内工作树一个字节都没动**
//	   ⇒ ⇒ ★★ 并行会话在同一个 checkout 里跑全套时，不会编译到一个「被改瞎的」包
//	   ⇒ ⇒ ★★ 进程被 SIGKILL 也不会在仓里留残骸（★ 残骸只在临时目录里）。
//	② **shell 臂**：★ 把**真实**脚本复制成临时副本并去掉红条件
//	   ⇒ ⇒ 同样**不动**工作树里的原件（★ 副本用完即删）。
//
// # 每个臂都跑【A/B 对照】
//
// ★ 同一份登记册、同一个掏空变换 ⇒ ⇒ **唯一的变量是「读方的红条件」**
// ⇒ ⇒ 结论的翻转因此**只能**由那个因子解释（★ 这就是「判据的输入集合必须确定」）。
//
// # 诚实边界
//
//	★ 本臂证明的是「**门能发现读方被改瞎**」，
//	★ 而**不是**「仓内所有读方都有牙」——★ 后者由行为判据（逐条掏空）负责。
//	★ 本臂也**不是**「负控臂本身没有 bug」的证明：★ 它的自证在下面
//	  （★ 变异「把改瞎的那一步变成空操作」⇒ 本臂必须红）。

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// ── 臂 ①：gotest 读方 ──────────────────────────────────────────────────

// blindGotestTarget 是「gotest 臂」的靶点。
//
// ★ 它是**一条真实的清单条目**（★ 在 tsv_hollowout_inventory.tsv 里登记为 expect=red）
// ⇒ ⇒ 本臂跑的是**真实读方**，不是合成读方。
//
// ★ 它的红条件在哪：★ `TestApiHandlerCoverageLedgerIsNotEmpty` 里那句
//
//	if len(ledger) == 0 {
//	    t.Fatal("★ 台账为空 ⇒ 分母为 0 ⇒ 拒绝出结论")
//	}
//
// ★ 改瞎的方式：★ 把 `t.Fatal` 换成 `t.Log`
// ⇒ ⇒ 「检查还在，但**不再让门红**」⇒ ⇒ 这就是「红条件被去掉」的最小改动。
const blindGotestTarget = "scripts/ursmcheck/api_handler_coverage_ledger.tsv"

const blindGotestReaderFile = "scripts/ursmcheck/api_handler_coverage_gate_test.go"

// blindGotestMutations 是「gotest 臂」要去掉的**全部**红路径。
//
// ★★★ 为什么是**全部**而不是一条（★ R86 轮实测踩到的坑，★ 值得单列）：
//
//	★ 第一版只改了 `TestApiHandlerCoverageLedgerIsNotEmpty` 里那句 `t.Fatal` ⇒
//	★ ⇒ 负控臂**当场失败**（分类仍是 red-caught）⇒ ★ 门「发现不了被改瞎的读方」。
//	★ ⇒ 真因：**这个读方有【两条】红路径** ——
//	  ① 测试体里的 `if len(ledger) == 0 { t.Fatal(...) }`；
//	  ② 解析辅助 `ahcReadLedger` 末尾的 `if len(out) == 0 { t.Fatalf(...) }`。
//	★ ⇒ 只去掉①，② 仍然会让读方红 ⇒ ⇒ 读方**并没有被改瞎** ⇒ ⇒ 负控臂的期望当然不成立。
//
// ★★★ ⇒ ⇒ **通用式（★ 本轮新增）**：
// ★★ 「把读方的红条件去掉」不是「改一行」⇒ ⇒ 是「改掉**读方在读到 0 行时仍会红**的**每一条**路径」
// ★★ ⇒ ⇒ **只去掉一部分 ⇒ 读方并没有被改瞎** ⇒ ⇒ 负控臂会**误报「门发现不了」**
// ★★ ⇒ ⇒ 而这条误报本身**不是**门的缺陷（★ 是探针没打到位）
// ★★ ⇒ ⇒ 承本线纪律：★「变异没让门红」的第一嫌疑是【探针无效】★ ★★ 本轮又撞了一次。
var blindGotestMutations = [][2]string{
	{"\t\tt.Fatal(\"★ 台账为空 ⇒ 分母为 0 ⇒ 拒绝出结论\")",
		"\t\tt.Log(\"★ 台账为空 ⇒ 分母为 0 ⇒ 拒绝出结论\")"},
	{"\t\tt.Fatalf(\"★ 台账为空 ⇒ 分母为 0 ⇒ 拒绝出结论\")",
		"\t\tt.Logf(\"★ 台账为空 ⇒ 分母为 0 ⇒ 拒绝出结论\")"},
}

// ── 臂 ②：shell 读方 ───────────────────────────────────────────────────

// blindShellTarget 是「shell 臂」的靶点（★ 同样是清单里的一条真实条目）。
//
// ★ 它的读方是 `measure-permanent-skip-axis.sh --dry-run-readonly`
// ⇒ ⇒ R86 轮刚给那个脚本加的**不碰库**模式（★ 见脚本里的同名段落）。
// ★ 它的红条件是「台账数据行 < 1 ⇒ exit 1」。
const blindShellTarget = "scripts/audit/permanent-skip-ledger.tsv"

const blindShellReaderFile = "scripts/audit/measure-permanent-skip-axis.sh"

const blindShellAnchor = "if [ \"${rows:-0}\" -lt 1 ]; then"

const blindShellReplacement = "if [ \"${rows:-0}\" -lt 0 ]; then"

// blindShellCopyDir 是「shell 臂」的副本落点目录。
//
// ★★ 为什么副本必须放在**仓内**而不是 /tmp：
// ★ 这些脚本用 `dirname "$0"/../..` 推算仓根 ⇒ ⇒ 副本必须**深度相同**才能推对。
// ★ ⇒ 落点 = `scripts/audit/`（★ 与原件同目录 ⇒ 深度相同 ⇒ 仓根推算正确）。
// ★ ⇒ 副本名带 `.` 前缀 ＋ `.gitignore` 已登记 ⇒ ⇒ 即使被 SIGKILL 留下，
// ★   `git status` 也**不会**变脏（★ 不污染并行会话的工作树视图）。
const blindShellCopyDir = "scripts/audit"

// hollowBlindEntry 在清单里找一条条目（★ 失败即 t.Fatal）。
func hollowBlindEntry(t *testing.T, root, path string) hollowEntry {
	t.Helper()
	for _, e := range readHollowInventory(t, root) {
		if e.Path == path {
			return e
		}
	}
	t.Fatalf("清单里没有条目 %s ⇒ 负控臂的靶点没了", path)
	return hollowEntry{}
}

// hollowBlindCopy 把一个文件复制到 dst，并返回内容。
func hollowBlindCopy(t *testing.T, src, dst string) []byte {
	t.Helper()
	data, err := os.ReadFile(src)
	if err != nil {
		t.Fatalf("读 %s 失败：%v", src, err)
	}
	if err := os.WriteFile(dst, data, 0o644); err != nil {
		t.Fatalf("写副本 %s 失败：%v", dst, err)
	}
	return data
}

// hollowBlindMutate 对内容做**恰好一次**的定点替换。
//
// ★ 「恰好一次」是硬要求（★ 承本线纪律：★ 变异落地 ≠ 变异有预期效果
// ★★ 而「命中 0 次」会让负控臂变成「假通过」⇒ ⇒ 所以这里直接判失败）。
func hollowBlindMutate(t *testing.T, data []byte, anchor, replacement, what string) []byte {
	t.Helper()
	s := string(data)
	n := strings.Count(s, anchor)
	if n != 1 {
		t.Fatalf("变异锚点命中 %d 次（★ 必须恰好 1 次）⇒ %s", n, what)
	}
	return []byte(strings.Replace(s, anchor, replacement, 1))
}

// hollowBlindMutateAll 依次应用**全部**变异锚点（★ 每个都必须恰好命中一次）。
//
// ★★ 为什么是「全部」（★ R86 轮实测踩到的坑）：
// ★★ 「把读方的红条件去掉」= 去掉**读方在读到 0 行时仍会红的每一条路径**
// ★★ ⇒ 只去掉其中一部分 ⇒ ★ 读方并没有被改瞎 ⇒ ⇒ 负控臂会**误报「门发现不了」**。
func hollowBlindMutateAll(t *testing.T, data []byte, muts [][2]string, what string) []byte {
	t.Helper()
	out := data
	for i, m := range muts {
		out = hollowBlindMutate(t, out, m[0], m[1],
			fmt.Sprintf("%s 的第 %d 条红路径（%q）", what, i+1, m[0]))
	}
	return out
}

// TestTsvHollowoutGateDetectsBlindedRealReader 是【负控臂】。
//
// ★ 断言（★ 两个臂各一组 A/B 对照）：
//
//	★ 未改瞎的**真实**读方 ⇒ 掏空 ⇒ 红 ⇒ 分类 = red-caught
//	★ 被改瞎的**真实**读方 ⇒ 掏空 ⇒ 绿 ⇒ 分类 = still-green
//
// ★★ 结论的翻转**只**由「读方的红条件」这一个因子解释（★ A/B 对照的价值）。
func TestTsvHollowoutGateDetectsBlindedRealReader(t *testing.T) {
	root := hollowRepoRoot(t)

	t.Run("gotest-臂", func(t *testing.T) {
		e := hollowBlindEntry(t, root, blindGotestTarget)
		if e.ReaderKind != "gotest" {
			t.Fatalf("靶点 %s 的读方形态是 %q，负控臂的 gotest 臂要求 gotest", blindGotestTarget, e.ReaderKind)
		}
		abs := filepath.Join(root, blindGotestReaderFile)
		orig, err := os.ReadFile(abs)
		if err != nil {
			t.Fatalf("读 %s 失败：%v", blindGotestReaderFile, err)
		}
		origMD5 := md5Of(orig)

		// ★ 造「改瞎副本」⇒ ★★ **仓内那个文件不动**（★ 只写进临时目录）。
		tmpDir := t.TempDir()
		blindedSrc := filepath.Join(tmpDir, "blinded_"+filepath.Base(blindGotestReaderFile))
		blinded := hollowBlindMutateAll(t, orig, blindGotestMutations[:],
			"gotest 臂的红条件（t.Fatal/t.Fatalf → t.Log/t.Logf）")
		if err := os.WriteFile(blindedSrc, blinded, 0o644); err != nil {
			t.Fatalf("写改瞎副本失败：%v", err)
		}
		overlayPath := filepath.Join(tmpDir, "overlay.json")
		overlayJSON := fmt.Sprintf("{\"Replace\":{%q:%q}}", abs, blindedSrc)
		if err := os.WriteFile(overlayPath, []byte(overlayJSON), 0o644); err != nil {
			t.Fatalf("写 overlay 失败：%v", err)
		}

		// ★ A：未改瞎 ⇒ 必须 red-caught（★ 这是对照组 ⇒ 证明靶点本身是有牙的）
		outcome, detail := hollowVerifyEntry(t, root, e, e.ReaderKind, e.ReaderSpec)
		if outcome != outcomeRedCaught {
			t.Fatalf("★ 对照组失效：未改瞎的读方在 %s 上不是 red-caught（是 %s）⇒ ⇒ 靶点选错了 ⇒ "+
				"★ 「变异没让门红」的第一嫌疑是【探针无效】。\n%s", blindGotestTarget, outcome, detail)
		}

		// ★ B：改瞎 ⇒ 必须 still-green（★ 这才是本臂要证明的那件事）
		outcome, detail = hollowVerifyEntryOverlay(t, root, e, e.ReaderKind, e.ReaderSpec, overlayPath)
		if outcome != outcomeStillGreen {
			t.Fatalf("★ 负控臂失败：读方被改瞎（红条件去掉）后，分类是 %s（期望 still-green）⇒ ⇒ "+
				"★ 门**发现不了**『读方被改瞎』⇒ ⇒ 门自己的量具不够。\n%s", outcome, detail)
		}

		// ★★★ 还原校验：★ 仓内那个文件**必须逐字未变**（★ 本臂的工作树零污染）。
		back, err := os.ReadFile(abs)
		if err != nil {
			t.Fatalf("还原校验：读 %s 失败：%v", blindGotestReaderFile, err)
		}
		if got := md5Of(back); got != origMD5 {
			t.Fatalf("还原校验：%s 的 md5 变了（期望 %s，实际 %s）⇒ ⇒ 负控臂污染了工作树",
				blindGotestReaderFile, origMD5, got)
		}
	})

	t.Run("shell-臂", func(t *testing.T) {
		e := hollowBlindEntry(t, root, blindShellTarget)
		if e.ReaderKind != "shell" {
			t.Fatalf("靶点 %s 的读方形态是 %q，负控臂的 shell 臂要求 shell", blindShellTarget, e.ReaderKind)
		}
		abs := filepath.Join(root, blindShellReaderFile)
		orig, err := os.ReadFile(abs)
		if err != nil {
			t.Fatalf("读 %s 失败：%v", blindShellReaderFile, err)
		}
		origMD5 := md5Of(orig)

		// ★ 造「改瞎副本」⇒ ★★ 原件不动；★ 副本落在**同深度**目录（★ 仓根推算才对）。
		copyPath := filepath.Join(root, blindShellCopyDir,
			fmt.Sprintf(".hollowout-blind-%d.sh", os.Getpid()))
		blinded := hollowBlindMutateAll(t, orig, [][2]string{{blindShellAnchor, blindShellReplacement}},
			"shell 臂的红条件（rows < 1 ⇒ rows < 0）")
		if err := os.WriteFile(copyPath, blinded, 0o755); err != nil {
			t.Fatalf("写改瞎副本失败：%v", err)
		}
		defer func() {
			_ = os.Remove(copyPath)
			// ★ 还原校验：★ 原件必须逐字未变。
			back, err := os.ReadFile(abs)
			if err != nil {
				t.Errorf("还原校验：读 %s 失败：%v", blindShellReaderFile, err)
				return
			}
			if got := md5Of(back); got != origMD5 {
				t.Errorf("还原校验：%s 的 md5 变了（期望 %s，实际 %s）", blindShellReaderFile, origMD5, got)
			}
		}()

		blindSpec := fmt.Sprintf("bash %s --dry-run-readonly",
			filepath.Join(blindShellCopyDir, filepath.Base(copyPath)))

		// ★ A：未改瞎 ⇒ 必须 red-caught（★ 对照组）
		outcome, detail := hollowVerifyEntry(t, root, e, e.ReaderKind, e.ReaderSpec)
		if outcome != outcomeRedCaught {
			t.Fatalf("★ 对照组失效：未改瞎的读方在 %s 上不是 red-caught（是 %s）⇒ ⇒ 靶点选错了。\n%s",
				blindShellTarget, outcome, detail)
		}

		// ★ B：改瞎 ⇒ 必须 still-green
		outcome, detail = hollowVerifyEntry(t, root, e, "shell", blindSpec)
		if outcome != outcomeStillGreen {
			t.Fatalf("★ 负控臂失败：读方被改瞎（红条件去掉）后，分类是 %s（期望 still-green）⇒ ⇒ "+
				"★ 门**发现不了**『读方被改瞎』。\n%s", outcome, detail)
		}
	})
}

// TestTsvHollowoutNegativeControlHasTeeth 是【负控臂自己的自证】。
//
// ★ 它证明的是：「负控臂不是恒绿的」——★ 把「改瞎的那一步」变成**空操作**
// （★ 即：改瞎副本其实与原件一模一样 ⇒ 读方并没有被改瞎）
// ⇒ ⇒ 负控臂必须**红**（★ 因为那时分类会回到 red-caught ⇒ 与期望不符）。
//
// ★★ 承本线纪律：★「断言恒真比恒假更隐蔽」⇒ ⇒ 必须给「断言会红」这件事本身加牙。
func TestTsvHollowoutNegativeControlHasTeeth(t *testing.T) {
	root := hollowRepoRoot(t)
	e := hollowBlindEntry(t, root, blindShellTarget)
	abs := filepath.Join(root, blindShellReaderFile)
	orig, err := os.ReadFile(abs)
	if err != nil {
		t.Fatalf("读 %s 失败：%v", blindShellReaderFile, err)
	}

	// ★ 「空操作」版：★ 副本内容 = 原件内容（★ 没有任何红条件被去掉）。
	copyPath := filepath.Join(root, blindShellCopyDir,
		fmt.Sprintf(".hollowout-blind-noop-%d.sh", os.Getpid()))
	if err := os.WriteFile(copyPath, orig, 0o755); err != nil {
		t.Fatalf("写副本失败：%v", err)
	}
	defer func() { _ = os.Remove(copyPath) }()

	noopSpec := fmt.Sprintf("bash %s --dry-run-readonly",
		filepath.Join(blindShellCopyDir, filepath.Base(copyPath)))
	outcome, detail := hollowVerifyEntry(t, root, e, "shell", noopSpec)
	if outcome != outcomeRedCaught {
		t.Fatalf("★ 负控臂的自证失效：★「没改瞎」的副本竟然被判成 %s（期望 red-caught）⇒ ⇒ "+
			"★ 说明负控臂的判定压根没看「红条件在不在」⇒ ⇒ 负控臂是恒真的。\n%s", outcome, detail)
	}
}

// hollowNegativeControlCompileTimeout 是负控臂编译测试二进制的超时。
const hollowNegativeControlCompileTimeout = 600 * time.Second
