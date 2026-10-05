package main

// 判据：backfill-modality 不得与已盖章的标注争 modality 这一列。
//
// # 缺口（2026-10-05，与 catalog.EffectiveModality 同一族）
//
// 「按名字推断模态」与「语义核实模态」对 models_canonical.modality 提出
// **互斥**的要求：核实判负要把模型降级成 text，按名推断看到 stored='text'
// 且名字像多模态就要把它推回去。迁移 825 在**写侧**（discovery 的 upsert、
// canonical_match.go）建了闸门：`modality_source NOT IN ('semantic','manual')`。
//
// 本工具是**第三处**，原先两道闸门都没有：
//   - 候选 SELECT 只筛 `modality='text'`，不看出处；
//   - UPDATE 只带 `AND modality='text'`，同样不看出处；
//   - UPDATE **不写 modality_source**。
//
// 最后一条是里面最险的一条：本工具产出的是**按名推断**，让它留着一个
// semantic/manual 的旧章，等于让出处对值说谎 —— 而 2026-10-05 补的读路径
// （catalog.EffectiveModality + GET /api/models/:id 的三个出处字段）正是
// 按章决定信不信值的那几处。出处一说谎，读路径就信了假话。
//
// # 为什么是自校验的源码形状判据
//
// 这是一个一次性的运维工具，没有可注入的 DB 接缝，仓库里也没有它的测试。
// 所以判据走源码形状。但**只查真实源码是不够的**：形状判据最容易变成恒真
// 判据（锚点写错、切片取空、检查的字符串和被检查的不是同一处），于是下面
// 每个断言都在**变异体**上再跑一遍，证明它真的会红。恒绿的判据等于没有判据。

import (
	"os"
	"strings"
	"testing"
)

const (
	backfillGuard     = "NOT IN ('semantic', 'manual')"
	backfillStamp     = "modality_source = 'inferred'"
	backfillSelectKey = "SELECT id, canonical_name, modality"
	backfillUpdateKey = "UPDATE models_canonical"
	backfillOrderKey  = "ORDER BY id"
)

// backfillProblems 返回源码里每一处「本工具会覆盖已盖章标注」或
// 「写值不盖章」的问题。空切片 = 通过。
//
// 判据自身的量具坑（先记下来，别让下一个人重踩）：
//   - 两段 SQL 都用同一个闸门字面量，所以**只查一处会漏**：SELECT 有闸门
//     而 UPDATE 没有的话，候选筛掉了但竞态窗口照旧存在（见 UPDATE 里那句
//     「闸门在 UPDATE 里再写一遍」的注释）。两条分别断言。
//   - 只 grep 字面量 `semantic` 会把半截闸门（`NOT IN ('semantic')`）判成通过，
//     所以匹配的是完整的 `NOT IN ('semantic', 'manual')`。
func backfillProblems(src string) []string {
	var problems []string

	selStart := strings.Index(src, backfillSelectKey)
	if selStart < 0 {
		return []string{"找不到候选 SELECT 的锚点（判据自身可能已失效）"}
	}
	selEnd := strings.Index(src[selStart:], backfillOrderKey)
	if selEnd < 0 {
		return []string{"找不到候选 SELECT 的终点锚点 ORDER BY id"}
	}
	selectSQL := src[selStart : selStart+selEnd]
	if !strings.Contains(selectSQL, backfillGuard) {
		problems = append(problems,
			"候选 SELECT 没有 modality_source 闸门：会把 semantic/manual 的行选成升级候选")
	}

	updStart := strings.Index(src, backfillUpdateKey)
	if updStart < 0 {
		return append(problems, "找不到 UPDATE 的锚点（判据自身可能已失效）")
	}
	updEnd := strings.Index(src[updStart:], "`")
	if updEnd < 0 {
		return append(problems, "找不到 UPDATE 语句的终点")
	}
	updateSQL := src[updStart : updStart+updEnd]
	if !strings.Contains(updateSQL, backfillGuard) {
		problems = append(problems,
			"UPDATE 没有 modality_source 闸门：候选集算完之后到写下去之间，"+
				"核实 worker 可能已把同一行判负并盖上 semantic")
	}
	if !strings.Contains(updateSQL, backfillStamp) {
		problems = append(problems,
			"UPDATE 不写 modality_source：按名推断的值会留在 semantic/manual 的旧章下面，"+
				"出处从此说谎，而读路径按章决定信不信值")
	}
	return problems
}

func TestBackfillModality_annotatedRowsAreProtected(t *testing.T) {
	src, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatalf("read main.go: %v", err)
	}
	if problems := backfillProblems(string(src)); len(problems) > 0 {
		for _, p := range problems {
			t.Errorf("backfill-modality 会与已盖章标注争同一列: %s", p)
		}
	}
}

// TestBackfillProblems_变异体上必须红 是这条判据自己的牙齿。
//
// 每个变异对应一个真实缺陷形态；判据对每个都必须给出非空结论。
// 全绿说明判据恒真 —— 那比没有判据更坏。
func TestBackfillProblems_变异体上必须红(t *testing.T) {
	real, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatalf("read main.go: %v", err)
	}
	base := string(real)

	// 先自证基线：判据对真实源码必须是绿的，否则下面全是噪声。
	if problems := backfillProblems(base); len(problems) > 0 {
		t.Fatalf("基线就红，变异实验无意义: %v", problems)
	}

	mutants := []struct {
		name   string
		mutate func(string) string
		// wantSubstring 让失败信息指出「缺的是哪一条」，而不是只说「有问题」
		wantSubstring string
	}{
		{
			name: "SELECT 闸门被删",
			mutate: func(s string) string {
				// 只删第一处（SELECT 那处），UPDATE 的保留。
				return strings.Replace(s, backfillGuard, "", 1)
			},
			wantSubstring: "候选 SELECT 没有 modality_source 闸门",
		},
		{
			name: "半截闸门（只挡 semantic，漏 manual）",
			mutate: func(s string) string {
				return strings.ReplaceAll(s, backfillGuard, "NOT IN ('semantic')")
			},
			wantSubstring: "闸门",
		},
		{
			name: "UPDATE 闸门被删（候选筛了但竞态窗口照旧）",
			mutate: func(s string) string {
				// 删最后一处 = UPDATE 那处，SELECT 的保留。
				i := strings.LastIndex(s, backfillGuard)
				if i < 0 {
					t.Fatalf("变异前提不成立：找不到闸门字面量")
				}
				return s[:i] + strings.Replace(s[i:], backfillGuard, "", 1)
			},
			wantSubstring: "UPDATE 没有 modality_source 闸门",
		},
		{
			name: "UPDATE 不盖章（值换了、章留着）",
			mutate: func(s string) string {
				return strings.Replace(s, backfillStamp, "", 1)
			},
			wantSubstring: "UPDATE 不写 modality_source",
		},
	}
	for _, m := range mutants {
		mutated := m.mutate(base)
		if mutated == base {
			t.Errorf("%s: 变异没生效（变异器本身坏了），这条实验不算数", m.name)
			continue
		}
		problems := backfillProblems(mutated)
		if len(problems) == 0 {
			t.Errorf("%s: 判据对缺陷变体是绿的 ⇒ 判据恒真", m.name)
			continue
		}
		joined := strings.Join(problems, " | ")
		if !strings.Contains(joined, m.wantSubstring) {
			t.Errorf("%s: 判据红了但指的是别的问题（%q 里没有 %q）",
				m.name, joined, m.wantSubstring)
		}
	}
}
