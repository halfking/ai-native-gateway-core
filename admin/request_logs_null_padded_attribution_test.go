package admin

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// scratchStringLiterals 返回文件里所有字符串字面量的文本。
//
// 解析用的是**原始源码**：剥注释会吃掉字面量里的 "//"（URL、SQL `--` 注释），
// 把 Go 源码弄成无法解析。字面量里本来就不含 Go 注释，不需要先剥。
func scratchStringLiterals(t *testing.T, src string) []string {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), "src.go", src, 0)
	if err != nil {
		t.Fatalf("解析失败 %v", err)
	}
	var out []string
	ast.Inspect(f, func(n ast.Node) bool {
		bl, ok := n.(*ast.BasicLit)
		if !ok || bl.Kind != token.STRING {
			return true
		}
		if s, err := strconv.Unquote(bl.Value); err == nil {
			out = append(out, s)
		}
		return true
	})
	return out
}

// request_logs_stop_write_classification_test.go 的 nullPaddedPredicateHit
// 配套门。§9.39。
//
// # 为什么这两道门必须存在
//
// nullPaddedPredicateHit 把「谓词级空」的判据从**整文件出现补位列名**改成
// **函数作用域归属**（同一函数既读会话臂视图、又提到该列）。这带来两个新面：
//
//  1. 登记表会因此**过期**。nullPaddedUnaffectedJustification 里那些「本文件
//     不在补位列上做谓词」的论证，一旦分类器自己能判出来，条目就成了失效豁免——
//     而失效的豁免比没有更坏（§9.37）：它让读者以为这条还在被守着。
//  2. 判据本身可能**被改回错的方向**。逐字面量归属看起来更精确，但它会把
//     拼接出来的 SQL 判成误触发，代价是放走真谓词。这条错误很隐蔽：它让门
//     保持绿，同时让一份真触发离开本族。
//
// 第 2 条只能靠「在已知答案上钉住判据」防住——先证明它能找到，
// 再谈它能挡住什么。

// TestNullPaddedJustificationIsNotStale 拦住失效豁免。
//
// 方向是单向的：**登记表里的每一条，都必须仍然处在 null_padded 族里**。
// 反方向（族里每个 unaffected 都得有论证）由
// TestStopWriteEffectAgreesWithSourceFamily 负责。
func TestNullPaddedJustificationIsNotStale(t *testing.T) {
	root := repoRootFromCaller(t)
	var stale []string
	for file := range nullPaddedUnaffectedJustification {
		raw, err := os.ReadFile(filepath.Join(root, file))
		if err != nil {
			t.Errorf("%s: 读取失败 %v", file, err)
			continue
		}
		if fam := sourceFamilyOf(string(raw), isSwitchConsumerFile(t, root, file)); fam != familyViewNullPadded {
			stale = append(stale, file+"（现族="+fam+"）")
		}
	}
	if len(stale) > 0 {
		sort.Strings(stale)
		t.Errorf("nullPaddedUnaffectedJustification 里有 %d 条已失效（该文件已不在 %s 族）：\n  %v\n"+
			"失效的豁免比没有更坏——它让读者以为这条还在被守着，而实际上分类器\n"+
			"已经能自己判出来了。请删掉条目；若你认为分类器判错了，那要修的是分类器。",
			len(stale), familyViewNullPadded, stale)
	}
}

// TestNullPaddedAttributionPinsKnownCases 把归属判据钉在**已知答案**上。
//
// 每条都注明它防的是哪个具体错误——没有说明的钉子过两年就没人敢动了。
func TestNullPaddedAttributionPinsKnownCases(t *testing.T) {
	root := repoRootFromCaller(t)
	cases := []struct {
		file    string
		inFam   bool
		via     string // 非空时同时钉住归属列
		because string
	}{
		{
			file: "bg/shared_pick.go", inFam: true, via: "id",
			// 防「逐字面量归属」这个看起来更精确、实际更危险的改法。
			// 该读点的 SQL 由多个字面量拼成（含 <ProbeTrafficExclusionPredicateView>
			// 占位）：含 client_model 的字面量不出现视图名，含视图名的字面量
			// 不含 client_model。逐字面量归属会把这个**真谓词**
			// （唯一产出是按 client_model 分组，而它是补位列 ⇒ 恒 0 行）
			// 判成误触发，让它离开本族。
			because: "拼接 SQL 的真谓词：函数作用域口径必须仍然命中它",
		},
		{
			file: "bg/candidate_failure_monitor.go", inFam: false,
			// 防整文件口径。它的 `WHERE id = $2`(:397) 落在
			// candidate_failure_logs 腿上，是另一张表；两个 request_logs 读点
			// （:206 staleness、:333 auto-cool）所在的函数里没有任何补位列。
			because: "`id` 属于 candidate_failure_logs，不是 710 视图的列",
		},
		{
			file: "admin/session_turns_tree.go", inFam: false,
			// 它的命中列只在投影里，且已被同表达式的非补位列 COALESCE 兜住。
			because: "命中列只在投影，且有非补位列 COALESCE 兜底",
		},
		{
			file: "domains/routeincident/store.go", inFam: false,
			// 它的 id 命中全在 incidents 表自身（SELECT/RETURNING/WHERE id = $1），
			// 读视图的函数在 :704，与那些命中不同函数。
			because: "`id` 属于 route_incidents 表自身",
		},
		{
			file: "admin/auto_title_generator.go", inFam: false,
			// `ak.id` on api_keys（:1174-1182）+ 一条 Go 注释。
			because: "`id` 属于 api_keys 表",
		},
		{
			file: "bg/stats_minute_rollup.go", inFam: false,
			// `WHERE id = 1` on request_stats_rollup_cursor（:94/:115/:131）。
			because: "`id` 属于 rollup cursor 表",
		},
	}
	for _, tc := range cases {
		t.Run(tc.file, func(t *testing.T) {
			raw, err := os.ReadFile(filepath.Join(root, tc.file))
			if err != nil {
				t.Fatalf("读取失败: %v", err)
			}
			fam := sourceFamilyOf(string(raw), isSwitchConsumerFile(t, root, tc.file))
			hit, via := nullPaddedPredicateHit(string(raw))
			inFam := fam == familyViewNullPadded
			if inFam != tc.inFam {
				t.Errorf("%s：族=%s，归属命中=%v（via %q），期望族内=%v（%s）\n"+
					"  当前判据：补位列必须出现在一个既读会话臂视图、又提到该列的函数体里。",
					tc.file, fam, hit, via, tc.inFam, tc.because)
			}
			if tc.via != "" && via != tc.via {
				t.Errorf("%s：归属列=%q，期望 %q\n"+
					"  归属列是给人看的定位信息（哪个补位列把文件带进本族），它变了要有人知道。",
					tc.file, via, tc.via)
			}
			// 归属命中与族归属必须一致：nullPaddedPredicateHit 说没命中，
			// 就不该有人被分到本族（反之亦然）。这两条判据曾经不同步过——
			// 一次是 logs_summary.go 靠「解析失败退回整文件」留在本族。
			if hit != inFam && !(fam == familyBodiesMixed || fam == familyBodies) {
				t.Errorf("%s：nullPaddedPredicateHit=%v 但族=%s —— 两个判据对不上。\n"+
					"  若该文件因读 bodies 而进的是 bodies 族，那是 switch 的优先级，可接受；\n"+
					"  否则说明归属判据与族判定在某条路径上脱节了。", tc.file, hit, fam)
			}
		})
	}
}

// TestNullPaddedAttributionNeverLosesALiteralLevelHit 是一条**方向性**门：
// 函数作用域是字面量作用域的超集，所以凡逐字面量能命中的，函数口径必须也命中。
//
// 它把那条「精确化不会漏掉真触发」的实测结论（strictOnly = 0）变成常驻断言。
// 没有它，将来有人把函数体缩到某个更窄的范围（例如只取含 SQL 字面量的函数）时，
// 漏判会以「门还是绿的」形式发生——因为没有对照。
func TestNullPaddedAttributionNeverLosesALiteralLevelHit(t *testing.T) {
	root := repoRootFromCaller(t)
	for file := range requestLogsReadInventory {
		raw, err := os.ReadFile(filepath.Join(root, file))
		if err != nil {
			t.Errorf("%s: 读取失败 %v", file, err)
			continue
		}
		src := string(raw)
		if !strings.Contains(strings.ToLower(src), "request_logs_with_current_month") {
			continue
		}
		// 逐字面量口径：只看**引用了该视图的字符串字面量**里有没有补位列。
		litHit := false
		for _, lit := range scratchStringLiterals(t, src) {
			if !strings.Contains(strings.ToLower(lit), "request_logs_with_current_month") {
				continue
			}
			for col := range sessionArmNullPaddedColumns {
				if nullPaddedColWordRE(col).MatchString(lit) {
					litHit = true
					break
				}
			}
		}
		if !litHit {
			continue
		}
		if funcHit, _ := nullPaddedPredicateHit(src); !funcHit {
			t.Errorf("%s：逐字面量口径命中，函数作用域口径却没命中。\n"+
				"  函数体 ⊇ 单个字面量，所以这在理论上不可能发生；发生了说明归属判据\n"+
				"  的区间取错了，那会放走真触发。", file)
		}
	}
}
