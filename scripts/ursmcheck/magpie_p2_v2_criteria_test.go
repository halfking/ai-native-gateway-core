package ursm_test

// magpie_p2_v2_criteria_test.go —— P2-1 的门：**v2 切换判据 + 行为差异清单**。
//
// # 这道门守住哪块缺口
//
// 计划书 §6-P2-1：「把『默认关』当**未完成**而不是『关闭』：给出切换判据
// （哪些能力必须在 v2 里、哪些可留 v1），并明确『两套路径并存』期间的行为差异清单」
// ⇒ 验收② 是「**每条差异有测试覆盖**」。
//
// ★ 若判据与差异写在 Markdown 里，这句话就只是一句**承诺**。
// ⇒ 本门把两者都当**代码**来校验：
//
//	① 判据清单非空、字段齐全、归属要求在词表内、ID 唯一；
//	② ★ **每条判据的 `Evidence` 必须逐字出现在生产文件里**
//	   （⇒ 判据不能凭空写，判据与实现不会悄悄分家）；
//	③ 差异清单非空、字段齐全、ID 唯一；
//	④ ★ **每条差异的 `TestName` 必须在仓内真实存在**
//	   （⇒ 「每条差异有测试覆盖」成为机器可校验的事实）；
//	⑤ ★ 两条**代码事实**仍然成立：4 个端点都被包装；阶段错误不阻断；默认关。
//
// # ★ 被测集合与期望都不来自被测对象
//
// 期望值是本文件里的**字面量**（端点路径、字符串片段）⇒ 改实现改不掉断言。

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/internal/pipelinev2"
)

// magpieP2RepoRoot 返回仓根目录。
func magpieP2RepoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller 失败 ⇒ 无法定位仓根")
	}
	root := filepath.Dir(filepath.Dir(filepath.Dir(file)))
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatalf("仓根定位可疑：%s 下没有 go.mod", root)
	}
	return root
}

// magpieP2ReadFile 读仓内相对路径的文件。
func magpieP2ReadFile(t *testing.T, root, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root, rel))
	if err != nil {
		t.Fatalf("读 %s 失败：%v", rel, err)
	}
	return string(b)
}

// TestMagpieP2V2CriteriaAreGroundedInProduction G1 + G2：
// 判据清单非空、字段齐全、且**每条判据的依据都逐字出现在生产文件里**。
func TestMagpieP2V2CriteriaAreGroundedInProduction(t *testing.T) {
	root := magpieP2RepoRoot(t)
	list := pipelinev2.Criteria()
	if len(list) == 0 {
		t.Fatal("判据清单是空的 ⇒ 门恒绿（形同虚设）")
	}
	seen := map[string]bool{}
	for _, c := range list {
		if c.ID == "" || c.Capability == "" || c.Reason == "" || c.Evidence == "" {
			t.Errorf("判据 %+v 字段不完整", c)
			continue
		}
		if seen[c.ID] {
			t.Errorf("判据 ID 重复：%s", c.ID)
		}
		seen[c.ID] = true
		switch c.Requirement {
		case pipelinev2.MustStayInV1, pipelinev2.MustBeInV2, pipelinev2.MayStayInV1:
		default:
			t.Errorf("判据 %s 的归属要求 %q 不在词表内", c.ID, c.Requirement)
		}
		file := c.EvidenceFile
		if file == "" {
			file = pipelinev2.EvidenceFile
		}
		src := magpieP2ReadFile(t, root, file)
		if !strings.Contains(src, c.Evidence) {
			t.Errorf("判据 %s（%s）的依据 %q 在 %s 里**找不到** ⇒ "+
				"判据与实现已经分家 ⇒ 要么更新判据，要么修实现",
				c.ID, c.Capability, c.Evidence, file)
		}
	}
}

// TestMagpieP2V2DifferencesHaveRealTests G3 + G4：
// 差异清单非空、字段齐全，且**每条差异挂的测试真实存在**。
//
// ★ 这是 P2-1 验收②「每条差异有测试覆盖」的机器化：
// 差异清单里的 `TestName` 必须在仓内某条 `*_test.go` 里**真的有一个同名测试函数**。
func TestMagpieP2V2DifferencesHaveRealTests(t *testing.T) {
	root := magpieP2RepoRoot(t)
	list := pipelinev2.Differences()
	if len(list) == 0 {
		t.Fatal("差异清单是空的 ⇒ 门恒绿（形同虚设）")
	}

	// 扫出仓内所有测试函数名（一次性）。
	defined := map[string]string{} // 测试名 → 所在文件
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			name := d.Name()
			if name == "vendor" || name == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, "_test.go") {
			return nil
		}
		b, rerr := os.ReadFile(path)
		if rerr != nil {
			return nil
		}
		for _, m := range regexp.MustCompile(`(?m)^func (Test[A-Za-z0-9_]*)\(`).FindAllStringSubmatch(string(b), -1) {
			if _, dup := defined[m[1]]; !dup {
				rel, _ := filepath.Rel(root, path)
				defined[m[1]] = filepath.ToSlash(rel)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("扫测试函数失败：%v", err)
	}
	if len(defined) == 0 {
		t.Fatal("一个测试函数都没扫到 ⇒ 判据失效")
	}

	seen := map[string]bool{}
	for _, d := range list {
		if d.ID == "" || d.Dimension == "" || d.V1 == "" || d.V2 == "" || d.Impact == "" {
			t.Errorf("差异 %+v 字段不完整", d)
			continue
		}
		if seen[d.ID] {
			t.Errorf("差异 ID 重复：%s", d.ID)
		}
		seen[d.ID] = true

		// ★ 恰有一个非空：要么有测试，要么**显式**承认没有。
		switch {
		case d.TestName != "" && d.Uncovered != "":
			t.Errorf("差异 %s 同时给了 TestName 与 Uncovered ⇒ 二者恰有一个非空", d.ID)
			continue
		case d.TestName == "" && d.Uncovered == "":
			t.Errorf("差异 %s 既没有测试也没有登记缺口 ⇒ 它要么有测试、要么显式承认没有", d.ID)
			continue
		case d.Uncovered != "":
			// 显式登记的缺口：理由必须是**实质**的（不是占位符）。
			if len(d.Uncovered) < 20 {
				t.Errorf("差异 %s 的 Uncovered 理由太短（%d 字）：%q", d.ID, len(d.Uncovered), d.Uncovered)
			}
			for _, ph := range []string{"todo", "TBD", "待补", "无", "?"} {
				if d.Uncovered == ph {
					t.Errorf("差异 %s 的 Uncovered 理由是占位符 %q", d.ID, ph)
				}
			}
			t.Logf("差异 %s（%s）⇒ ★ 显式登记为**无测试覆盖**：%s", d.ID, d.Dimension, d.Uncovered)
			continue
		}

		where, ok := defined[d.TestName]
		if !ok {
			t.Errorf("差异 %s（%s）挂的测试 %s 在仓内**不存在** ⇒ "+
				"「每条差异有测试覆盖」不成立",
				d.ID, d.Dimension, d.TestName)
			continue
		}
		// ★ 名实相符：主题词必须出现在测试名里。
		// ★ 这一条抓的是「把 A 差异挂到 B 差异的测试上」——
		//   那种情况下测试**存在**，但它根本不测 A ⇒ 单看「存在」会放行。
		if d.Topic == "" {
			t.Errorf("差异 %s 没有 Topic ⇒ 无法校验「名实相符」", d.ID)
			continue
		}
		if !strings.Contains(d.TestName, d.Topic) {
			t.Errorf("差异 %s（%s）的主题词 %q **不出现在**测试名 %s 里 ⇒ "+
				"「测试存在」但**名不符实**（它测的是别的东西）",
				d.ID, d.Dimension, d.Topic, d.TestName)
			continue
		}
		t.Logf("差异 %s（%s）⇒ 覆盖测试 %s（%s）", d.ID, d.Dimension, d.TestName, where)
	}
}

// TestMagpieP2V2ProductionFactsStillHold G5：
// 三条**代码事实**仍然成立（判据表不能取代代码事实）。
func TestMagpieP2V2ProductionFactsStillHold(t *testing.T) {
	root := magpieP2RepoRoot(t)
	src := magpieP2ReadFile(t, root, "cmd/gateway/main_pipeline.go")

	// ① 4 个端点都被包装（注册点形态：`mux.Handle("<path>", v2DispatchHandler(...))`）。
	for _, ep := range pipelinev2.WrappedEndpoints() {
		needle := "mux.Handle(\"" + ep + "\", v2DispatchHandler("
		if !strings.Contains(src, needle) {
			t.Errorf("端点 %s 没有被 v2DispatchHandler 包装（找不到 %q）", ep, needle)
		}
	}
	if len(pipelinev2.WrappedEndpoints()) != 4 {
		t.Errorf("被包装的端点数 = %d，want 4", len(pipelinev2.WrappedEndpoints()))
	}

	// ② 「阶段错误不阻断」这条**安全契约**仍在。
	if !strings.Contains(src, "does NOT block the request") {
		t.Error("找不到「阶段错误不阻断请求」的安全契约措辞 ⇒ " +
			"★ 若它被改成阻断，v2 的「misbehaving Hook 不能丢数据」前提就没了")
	}

	// ③ 默认关：`v2UsePipeline` 在没有任何环境变量为真时返回 false。
	start := strings.Index(src, "func v2UsePipeline() bool {")
	if start < 0 {
		t.Fatal("找不到 v2UsePipeline ⇒ 判据失效")
	}
	end := strings.Index(src[start:], "\n}")
	if end < 0 {
		t.Fatal("v2UsePipeline 的函数体边界读不出来 ⇒ 判据失效")
	}
	body := src[start : start+end]
	if !strings.Contains(body, "return false") {
		t.Error("v2UsePipeline 的默认返回值不是 false ⇒ 「默认关」这条判据已被破坏")
	}
}
