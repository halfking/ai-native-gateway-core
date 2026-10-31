package ursm_test

// structured_registry_negative_control_test.go —— 2026-10-28 R87 轮
// （非 `.tsv` 登记册掏空门的**负控臂** + ★★ **负控臂的负控臂**）
//
// # 这道门要钉住的事
//
// ★ 承接 §19.25.19.10 优先级 1 ①：★ **负控臂本身够不够？**
//
// ★ R86 轮的负控臂有 2 个臂（★ gotest / shell）＋ 1 个自证臂。
// ★★ R87 轮补上三样：
//
//	① **node 臂**（★ R87 轮新增了 `node` / `vitest` 两种读方形态 ⇒ ⇒ 负控臂
//	   **只覆盖了旧的两种** ⇒ ⇒ 新形态的「有牙」从未被行为化验证过）。
//	② ★★ **负控臂的负控臂**（★ 本轮最值钱的一处）：
//	   ★ 负控臂的结论是「被改瞎的读方 ⇒ 分类 = `still-green`」。
//	   ★★ **风险**：★ 如果读方**根本没跑起来**（★ 路径写错 / 二进制不存在 / cwd 不对），
//	   ★★ 退出码也会是 0 ⇒ ⇒ 分类同样判成 `still-green`
//	   ⇒ ⇒ ⇒ 负控臂会报「门发现不了被改瞎的读方」⇒ ⇒ **假警报**。
//	   ★ ⇒ ⇒ 本门证明：★ 每一种读方形态在「**跑不起来**」时都返回**错误**
//	   ⇒ ⇒ 而不是「退出码 0」⇒ ⇒ ⇒ 「still-green」不可能是「探针没跑」的代名词。
//	③ **vitest 形态的「有牙」双向自证**（★ 绿 ⇒ 退出码 0；红 ⇒ 退出码非 0）。
//
// # 沿用本线的纪律
//
// ★★★ 「把读方的红条件去掉」＝ 去掉【读方在读到 0 行时仍会红的**每一条**路径】
// ★★★ ⇒ ⇒ 只改一处 ⇒ ⇒ 负控臂会误报「门发现不了」。
// ★★★★ 凡要在真实代码上做「减法」的负控臂，首选【编译期 overlay】⇒ ⇒
// ★★★★ **绝不改共享工作树里的源文件**（★ 本文件只写**临时副本**⇒ ⇒ 用完即删）。

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// ── 臂 ③：node 读方 ─────────────────────────────────────────────────────

// blindNodeTarget 是「node 臂」的靶点（★ 一份**真实的**结构化登记册）。
//
// ★ 它是 `scripts/ursmcheck/structured-registry-census.json` 里登记为
// ★ `kind=registry` 且 `behaviorally_verified=true` 的一条 ⇒ ⇒ 本臂跑的是**真实读方**。
//
// ★ 它的红条件在哪：★ `web/scripts/i18n-empty-state-guard.mjs` 的 `checkEmptyStateFacts`
// ★ 开头那两条（R87 轮补上的）：
//
//	if (!Array.isArray(registry.keys) || registry.keys.length === 0) {
//	  add('empty-registry', '登记册一条键都没有 ⇒ …')
//	}
//	if (!Array.isArray(registry.kindVocabulary) || registry.kindVocabulary.length === 0) {
//	  add('empty-kind-vocabulary', '状态种类词表为空 ⇒ …')
//	}
//
// ★ 改瞎的方式：★ 把两个条件都换成 `false` ⇒ ⇒ 那两条红路径**都不再触发**
// ⇒ ⇒ 登记册被掏空时门就**绿**⇒ ⇒ 这就是「读方被改瞎了」。
//
// ★★★ 为什么**两条都要改**（★ 沿用 R86 轮的教训）：
// ★★ 只去掉 `empty-registry` 那一条 ⇒ `empty-kind-vocabulary` 那条**仍会红**
// ★★ ⇒ ⇒ 读方**并没有被改瞎**⇒ ⇒ 负控臂会**误报「门发现不了」**。
const blindNodeTarget = "web/scripts/i18n-empty-state-registry.json"

// blindNodeReaderFile 是「node 臂」的读方文件。
const blindNodeReaderFile = "web/scripts/i18n-empty-state-guard.mjs"

// blindNodeMutations 是「node 臂」要去掉的**全部**红路径。
var blindNodeMutations = [][2]string{
	{"!Array.isArray(registry.keys) || registry.keys.length === 0", "false"},
	{"!Array.isArray(registry.kindVocabulary) || registry.kindVocabulary.length === 0", "false"},
}

// blindNodeCopyDir 是「node 臂」的副本落点目录。
//
// ★★ 为什么副本必须放在**同目录**（★ 而不在 /tmp）：★ 这些脚本用
// ★ `resolve(fileURLToPath(import.meta.url), '../..')` 推算仓根
// ⇒ ⇒ 副本必须**深度相同**才能推对（★ 否则它会去读**别的**仓根下的登记册）。
// ★ ⇒ ⇒ 落点 = `web/scripts/`（★ 与原件同目录 ⇒ 深度相同 ⇒ 仓根推算正确）。
const blindNodeCopyDir = "web/scripts"

// blindNodeCopyName 是副本文件名模板（★ 以 `.` 开头 ⇒ 已被 .gitignore 登记 ⇒ 不脏工作树）。
const blindNodeCopyName = ".hollowout-blind-%d.mjs"

// TestStructuredRegistryNegativeControlDetectsBlindedNodeReader 是【负控臂·node 臂】。
//
// ★ 断言（★ A/B 对照）：★ 同一份登记册、同一个掏空变换
// ⇒ ⇒ **唯一的变量是「读方的红条件」** ⇒ ⇒ 结论的翻转只能由那个因子解释。
func TestStructuredRegistryNegativeControlDetectsBlindedNodeReader(t *testing.T) {
	root := hollowRepoRoot(t)

	e := structuredCensusRegistryEntry(t, root, blindNodeTarget)
	if e.ReaderKind != "node" {
		t.Fatalf("靶点 %s 的读方形态是 %q，负控臂的 node 臂要求 node", blindNodeTarget, e.ReaderKind)
	}
	if e.HollowMode != string(structuredHollowEntries) {
		t.Fatalf("靶点 %s 的掏空模式是 %q，node 臂要求 %q", blindNodeTarget, e.HollowMode, structuredHollowEntries)
	}

	abs := filepath.Join(root, blindNodeReaderFile)
	orig, err := os.ReadFile(abs)
	if err != nil {
		t.Fatalf("读 %s 失败：%v", blindNodeReaderFile, err)
	}
	origMD5 := md5Of(orig)

	// ★ 造「改瞎副本」⇒ ★★ 仓内那个文件不动（★ 只写进 web/scripts/ 下的临时副本）。
	blindPath := filepath.Join(root, blindNodeCopyDir, fmt.Sprintf(blindNodeCopyName, os.Getpid()))
	blinded := hollowBlindMutateAll(t, orig, blindNodeMutations[:], "node 臂的红条件（登记册为空 / 词表为空 ⇒ 恒假）")
	if err := os.WriteFile(blindPath, blinded, 0o644); err != nil {
		t.Fatalf("写改瞎副本失败：%v", err)
	}
	defer func() {
		_ = os.Remove(blindPath)
		// ★ 还原校验：★ 原件必须逐字未变。
		back, err := os.ReadFile(abs)
		if err != nil {
			t.Errorf("还原校验：读 %s 失败：%v", blindNodeReaderFile, err)
			return
		}
		if got := md5Of(back); got != origMD5 {
			t.Errorf("还原校验：%s 的 md5 变了（期望 %s，实际 %s）", blindNodeReaderFile, origMD5, got)
		}
	}()

	blindSpec := filepath.Join(blindNodeCopyDir, filepath.Base(blindPath))

	// ★ A：未改瞎 ⇒ 必须 red-caught（★ 对照组 ⇒ 证明靶点本身是有牙的）
	outcome, detail := structuredVerifyEntry(t, root, e)
	if outcome != structuredRedCaught {
		t.Fatalf("★ 对照组失效：未改瞎的读方在 %s 上不是 red-caught（是 %s）⇒ ⇒ 靶点选错了 ⇒ "+
			"★ 「变异没让门红」的第一嫌疑是【探针无效】。\n%s", blindNodeTarget, outcome, detail)
	}

	// ★ B：改瞎 ⇒ 必须 still-green（★ 这才是本臂要证明的那件事）
	outcome, detail = structuredVerifyEntryWithReader(t, root, e, "node", blindSpec)
	if outcome != structuredStillGreen {
		t.Fatalf("★ 负控臂失败：读方被改瞎（红条件去掉）后，分类是 %s（期望 still-green）⇒ ⇒ "+
			"★ 门**发现不了**『读方被改瞎』⇒ ⇒ 门自己的量具不够。\n%s", outcome, detail)
	}
}

// structuredCensusRegistryEntry 在普查表里找一条 registry 条目（★ 失败即 t.Fatal）。
func structuredCensusRegistryEntry(t *testing.T, root, path string) structuredCensusEntry {
	t.Helper()
	for _, e := range loadStructuredCensus(t, root).Entries {
		if e.Path == path {
			return e
		}
	}
	t.Fatalf("普查表里没有 registry 条目 %s ⇒ 负控臂的靶点没了", path)
	return structuredCensusEntry{}
}

// ── ★★ 臂 ④：负控臂的负控臂（★ 读方没跑起来 vs 读方是绿的） ─────────────

// TestStructuredRegistryHarnessDistinguishesNotRunFromGreen 是
// ★★ **负控臂的负控臂**（★ R87 轮新增，★ 承接 §19.25.19.10 优先级 1 ①）。
//
// # 要钉住的事
//
// ★★ 负控臂的结论形态是：「被改瞎的读方 ⇒ 分类 = `still-green`」。
// ★★★ **风险**：★ 如果读方**根本没跑起来**（★ 脚本路径写错 / vitest 二进制不在 /
// ★★★ 包目录不存在 / cwd 推错）⇒ ⇒ 退出码也会是 0（★ 或被 harness 当成 0）
// ⇒ ⇒ ⇒ 分类同样判成 `still-green`
// ⇒ ⇒ ⇒ 负控臂会报「门发现不了『读方被改瞎』」⇒ ⇒ **那是假警报**（★ 探针压根没跑）。
//
// ★ ⇒ ⇒ 本门证明一件机械的事：★ **每一种读方形态在「跑不起来」时都返回错误**
// ⇒ ⇒ 而不是「退出码 0」⇒ ⇒ ⇒ 于是 `still-green` **只可能**意味着
// ★ 「读方真的跑了，而且它确实是绿的」。
//
// ★★ 而这条恰恰是本线纪律「★ 『掏空后仍绿』的第一嫌疑是【探针无效】」的**镜像**：
// ★★ 阴性结论要当缺陷一样复核 ⇒ ⇒ **阳性结论（『仍绿』⇒ 『读方被改瞎了』）也一样要复核**。
func TestStructuredRegistryHarnessDistinguishesNotRunFromGreen(t *testing.T) {
	root := hollowRepoRoot(t)

	cases := []struct {
		kind string
		spec string
		what string
	}{
		{"node", "web/scripts/__hollowout_no_such_script__.mjs", "node 臂（★ 脚本不存在）"},
		{"vitest", "web/src/i18n/__hollowout_no_such_test__.test.ts", "vitest 臂（★ 测试文件不存在）"},
		{"gotest", "./__hollowout_no_such_pkg__/|TestNothing", "gotest 臂（★ 包目录不存在）"},
	}
	for _, c := range cases {
		t.Run(c.what, func(t *testing.T) {
			code, out, err := runHollowReader(c.kind, c.spec, root, 60*time.Second)
			if err == nil {
				t.Fatalf("★ %s：读方**跑不起来**时必须返回**错误**（★ err != nil）⇒ ⇒ "+
					"★ 现在却拿到了「退出码 %d」⇒ ⇒ 「still-green」就可能是**假警报**（★ 探针没跑）。\n%s",
					c.what, code, tailLines(out, 20))
			}
			if code == 0 {
				t.Fatalf("★ %s：读方跑不起来时退出码也**不许**是 0（★ 0 = 绿）⇒ ⇒ 同样的假警报风险。\n%s",
					c.what, tailLines(out, 20))
			}
			t.Logf("✔ %s：读方跑不起来 ⇒ 返回错误（%v）⇒ ⇒ 「still-green」只可能意味着「跑了且绿」", c.what, err)
		})
	}
}

// ── 臂 ⑤：vitest 形态的「有牙」双向自证 ─────────────────────────────────

// TestStructuredRegistryVitestReaderKindHasTeeth 是【vitest 读方形态的量具自证】。
//
// ★ 它证明 vitest 臂**两向**都成立：
//
//	① **跑一个会红的测试** ⇒ 退出码**非 0**（★ 否则「红」这一侧无从谈起）。
//	② **跑一个会绿的真实门** ⇒ 退出码**0**（★ 否则「绿」这一侧无从谈起 ⇒ ⇒
//	   ⇒ 「一切皆红」的门同样没有牙）。
//
// ★★ 为什么要有：★ R87 轮新增了 vitest 形态 ⇒ ⇒ 若它**恒红**（★ 例如路径参数错
// ★★ ⇒ vitest 报「No test files found」⇒ ⇒ 退出码非 0）⇒ ⇒ 那 3 条 vitest 登记册
// ★★ 的「掏空 ⇒ 红」结论就全是**假红**（★ 红在「没找到测试」而不是红在「基线不可用」）。
// ⇒ ⇒ ① 必须由一个**故意会红**的测试来证。
func TestStructuredRegistryVitestReaderKindHasTeeth(t *testing.T) {
	root := hollowRepoRoot(t)

	// ★ ① 故意会红的测试（★ 放在 web/src/i18n/ 下 ⇒ vitest 的 include 规则能收它）。
	// ★★ 名字必须**不像** `*.test.ts` 会被仓库其它机制扫到 ⇒ ⇒ 用 `.hollowout-` 前缀
	// ★★ 且已被 .gitignore 登记 ⇒ ⇒ 不脏工作树。
	redFile := filepath.Join(root, "web", "src", "i18n", ".hollowout-red.test.ts")
	redContent := "import { describe, expect, it } from 'vitest'\n" +
		"describe('hollowout 量具自证', () => { it('故意红', () => { expect(1).toBe(2) }) })\n"
	if err := os.WriteFile(redFile, []byte(redContent), 0o644); err != nil {
		t.Fatalf("写故意会红的测试失败：%v", err)
	}
	defer func() { _ = os.Remove(redFile) }()

	code, out, err := runHollowReader("vitest", "web/src/i18n/.hollowout-red.test.ts", root, 300*time.Second)
	if err != nil {
		t.Fatalf("★ vitest 臂跑故意会红的测试时**报错**了 ⇒ ⇒ 形态本身有问题：%v", err)
	}
	if code == 0 {
		t.Fatalf("★ vitest 臂跑一个**故意会红**的测试却退出码 0 ⇒ ⇒ 「红」这一侧没有牙 ⇒ ⇒ "+
			"★ 那 3 条 vitest 登记册的「掏空 ⇒ 红」结论都不可信。\n%s", tailLines(out, 20))
	}

	// ★ ② 跑一个**真实**的门（★ 此刻基线是好的 ⇒ ⇒ 必须绿）。
	e := structuredCensusRegistryEntry(t, root, "web/src/i18n/localeValueBaseline.json")
	code, out, err = runHollowReader(e.ReaderKind, e.ReaderSpec, root, 300*time.Second)
	if err != nil {
		t.Fatalf("★ vitest 臂跑真实门时**报错**了 ⇒ ⇒ 形态本身有问题：%v", err)
	}
	if code != 0 {
		t.Fatalf("★ vitest 臂跑真实门（基线完好）却退出码 %d ⇒ ⇒ 「绿」这一侧没有牙 ⇒ ⇒ "+
			"★ 「一切皆红」的门同样没有牙。\n%s", code, tailLines(out, 20))
	}
}

// ── 通用：检查一个目录里有没有留下未登记的临时文件 ───────────────────────

// TestStructuredRegistryNegativeControlLeavesNoResidue 是【工作树零污染】判据。
//
// ★ 负控臂要写**临时副本**（★ 同深度才能推对仓根）⇒ ⇒ 用完必须删干净
// ⇒ ⇒ 否则并行会话会看到**不属于它的**文件（★ 本仓 §143.87 登记过的那个病）。
func TestStructuredRegistryNegativeControlLeavesNoResidue(t *testing.T) {
	root := hollowRepoRoot(t)
	entries, err := os.ReadDir(filepath.Join(root, blindNodeCopyDir))
	if err != nil {
		t.Fatalf("读 %s 失败：%v", blindNodeCopyDir, err)
	}
	var leftovers []string
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".hollowout-") {
			leftovers = append(leftovers, e.Name())
		}
	}
	if len(leftovers) > 0 {
		t.Errorf("%s 里留下了 %d 个负控臂的临时副本：%v\n★ ⇒ ⇒ 负控臂污染了工作树 ⇒ ⇒ 必须删干净",
			blindNodeCopyDir, len(leftovers), leftovers)
	}
}
