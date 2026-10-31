//go:build !integration

package admin

// session_family_delete_two_surface_test.go —— r1043 轮新增：
// ★ **会话族的「删除」也必须覆盖两面**（★ hot 与分区父表）。
//
// ★★ 起因（★ 本轮实测，★ 不是推演）：
//
//	上游在 `admin/session_family_two_surface_test.go` 里给「裸父表**读**」的判据
//	加了一条排除：`DELETE FROM <父表>` **不是读** ⇒ 本门只管读。
//	⇒ ★★ 这条排除本身是对的（★ DELETE 确实是写，不是读）。
//	⇒ ★★ **但它引用的依据是假的** —— 注释写「DELETE 的两面覆盖由
//	★ ★★ repair_two_surface_test.go 逐条断言 ⇒ 本门只管读」，
//	★ ★★ 而**那道门只覆盖 `cmd/tools/validate_sessions_v2/repair.go` 一条路径**
//	★ ★★ （★ 实测：★ `cmd/tools/validate_sessions_v2/repair_two_surface_test.go`
//	★ ★★ 里只有 `TestExecuteRepairDeletesBodiesFromBothSurfaces` 与
//	★ ★★ `TestExecuteRepairCountsBothSurfaces` ⇒ ★★ 断言的是那个工具，
//	★ ★★ **完全没覆盖 `admin/session_trash.go` / `admin/recording_consent.go`**）。
//	⇒ ⇒ ★★★ 于是：**admin 侧的两面 DELETE 覆盖，此前无人断言**。
//
// ★★ 而这两处的「两面」是**语义要害**（★ 仓内注释自认）：
//	★   回收站「移入回收站」：★ 只删 hot ⇒ 已从 hot 晋级到月分区的行留着
//	★     ⇒ 同一 session 的正文在两处可见 ⇒ 「回收站」语义破；
//	★   关闭录制同意：★ 只删 hot ⇒ 「默认关闭录制」的隐私承诺名存实亡。
//
// ★★ 本门就是那道**缺失的守卫**：★ 逐文件逐条核对
// ★ 「对分区父表的每个 DELETE，都必须有一条 WHERE **完全相同**的 hot DELETE」。
// ★★ 写法则不止「两面覆盖」，还要求 WHERE 一致 ——
// ★★ 因为 WHERE 不同 ⇒ 删的**不是同一批行** ⇒ 两面覆盖是假的。
//
// ★★ 与读门的关系（★ 为什么不是重复）：
//	★   读门（session_family_two_surface_test.go）管「读」⇒ ★★ 明确排除 DELETE；
//	★   本门管「删」⇒ ★★ 两门互补，★ 各管一面，★★ 不是同一件事的两个名字。

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// sessionFamilyHotParentPairs 是「hot 腿 ↔ 分区父表腿」的表对。
//
// ★ 与 session_family_two_surface_test.go 的 sessionFamilyParents 同源，
// ★ 但这里用的是**带 _hot 后缀的孪生表**。
var sessionFamilyHotParentPairs = []struct {
	parent string
	hot    string
}{
	{"public.session_bodies", "public.session_bodies_hot"},
	{"public.session_turns", "public.session_turns_hot"},
	{"public.session_turn_details", "public.session_turn_details_hot"},
}

// deleteLeg 是「一条删除语句」的归一化形态。
type deleteLeg struct {
	// where：★ 归一化后的 WHERE 子句（★ 空白已折叠）。
	where string
}

// reDeleteLeg 抓一条 DELETE 语句：★ 表名 + 其后的 WHERE 子句。
//
// ★ 形状说明：★ 本仓的删除写在 Go 原始字符串（反引号）里 ⇒ ★ 用反引号/单引号
// ★ 作为语句边界（★ 抓不到就说明这条语句不是本门要管的那种形态）。
var reDeleteLeg = regexp.MustCompile("(?is)DELETE\\s+FROM\\s+(public\\.session_[a-z_]+)\\s+(WHERE[^`']*)")

// parseDeleteLegs 从源码里解析出「对给定表」的删除腿。
//
// ★ 返回：★ 表名 ⇒ 各条删除的 WHERE 归一化列表（★ 保留重复 ⇒ ★ 计数有意义）。
func parseDeleteLegs(src string) map[string][]string {
	out := map[string][]string{}
	for _, m := range reDeleteLeg.FindAllStringSubmatch(src, -1) {
		tbl := m[1]
		where := normalizeWhere(m[2])
		out[tbl] = append(out[tbl], where)
	}
	return out
}

// normalizeWhere 折叠 WHERE 子句的空白（★ 让「只差一个空格」不构成差异）。
func normalizeWhere(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimSuffix(s, ";")
	return strings.Join(strings.Fields(s), " ")
}

// surfaceDeleteGaps 返回「两面删除不配对」的缺口清单（★ 空 = 配对）。
//
// ★ 判据：★ 对每一对表（parent/hot）：
//
//	★   ① 父表腿数必须 == hot 腿数（★ 少一条 ⇒ 漏面；多一条 ⇒ 多余面）；
//	★   ② 归一化后的 WHERE 集合必须**逐条相等**（★ WHERE 不同 ⇒ 删的不是同一批行）。
func surfaceDeleteGaps(legs map[string][]string) []string {
	var gaps []string
	for _, pair := range sessionFamilyHotParentPairs {
		// ★ 防御性归一化：★ 本函数收到的一切 WHERE 都先折叠空白
		// ★ ⇒ ★ 判据不依赖调用方是否归一化过（★ 单元测试直接喂原始腿）。
		norm := func(in []string) []string {
			out := make([]string, 0, len(in))
			for _, w := range in {
				out = append(out, normalizeWhere(w))
			}
			return out
		}
		parents := norm(legs[pair.parent])
		hots := norm(legs[pair.hot])
		if len(parents) == 0 && len(hots) == 0 {
			continue
		}
		if len(parents) != len(hots) {
			gaps = append(gaps, fmt.Sprintf(
				"★ %s 删了 %d 条、%s 删了 %d 条 ⇒ ⇒ 两面不配对（★ 只删一面 ⇒ 已从 hot 晋级到月分区的行留着）",
				pair.parent, len(parents), pair.hot, len(hots)))
			continue
		}
		// ★ WHERE 逐条配对（★ 用多重集比较：★ 顺序无关，★ 计数有关）。
		used := make([]bool, len(hots))
		for _, w := range parents {
			found := false
			for i, h := range hots {
				if !used[i] && h == w {
					used[i] = true
					found = true
					break
				}
			}
			if !found {
				gaps = append(gaps, fmt.Sprintf(
					"★ %s 有一条删除（WHERE %s）在 %s 上找不到同 WHERE 的孪生腿 ⇒ ⇒ 删的不是同一批行 ⇒ 两面覆盖是假的",
					pair.parent, w, pair.hot))
			}
		}
	}
	sort.Strings(gaps)
	return gaps
}

// TestSessionFamilyDeleteCoversBothSurfaces：★ admin 侧会话族的删除必须两面覆盖。
//
// ★ 这道门是「读门排除 DELETE」的对冲面：★ 读门不管删 ⇒ ⇒ 本门必须管。
func TestSessionFamilyDeleteCoversBothSurfaces(t *testing.T) {
	root := repoRoot(t)
	entries, err := os.ReadDir(filepath.Join(root, "admin"))
	if err != nil {
		t.Fatalf("读 admin 目录失败：%v", err)
	}
	var gaps []string
	covered := 0
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(root, "admin", e.Name()))
		if err != nil {
			t.Fatalf("读 admin/%s 失败：%v", e.Name(), err)
		}
		legs := parseDeleteLegs(string(b))
		g := surfaceDeleteGaps(legs)
		if len(g) == 0 {
			for _, pair := range sessionFamilyHotParentPairs {
				if len(legs[pair.parent]) > 0 {
					covered++
					break
				}
			}
			continue
		}
		for _, one := range g {
			gaps = append(gaps, "admin/"+e.Name()+"："+one)
		}
	}
	sort.Strings(gaps)
	if len(gaps) > 0 {
		t.Fatalf("★ 会话族删除的两面覆盖被破坏（%d 处）：\n  %s\n"+
			"  ⇒ 只删一面 ⇒ 已从 hot 晋级到月分区的行留着 ⇒ 「回收站」「关闭录制清空正文」两个语义都破",
			len(gaps), strings.Join(gaps, "\n  "))
	}
	if covered == 0 {
		t.Fatalf("★ 本门【一条都没覆盖到】（admin 下没有任何会话族父表删除）⇒ ⇒ 门是空的 ⇒ ⇒ 白绿")
	}
	t.Logf("★ 会话族删除两面覆盖：%d 个文件族 ✓", covered)
}

// TestSessionFamilyDeleteTwoSurfaceDetectorHasTeeth：★ 自证有牙 ——
// ★ 「少一条腿」「WHERE 不同」「多一条腿」三种破坏，判据都必须报出来。
func TestSessionFamilyDeleteTwoSurfaceDetectorHasTeeth(t *testing.T) {
	both := map[string][]string{
		"public.session_bodies":     {"tenant_id = $1 AND session_id = $2"},
		"public.session_bodies_hot": {"tenant_id = $1 AND session_id = $2"},
	}
	if gaps := surfaceDeleteGaps(both); len(gaps) != 0 {
		t.Fatalf("★ 配对的两面被判出缺口（%v）⇒ ⇒ 判据过宽 ⇒ ⇒ 会误报", gaps)
	}

	// ★ 变异臂 1：★ 少一条 hot 腿（★ 只删父表）。
	onlyParent := map[string][]string{
		"public.session_bodies": {"tenant_id = $1 AND session_id = $2"},
	}
	gaps := surfaceDeleteGaps(onlyParent)
	if len(gaps) != 1 || !strings.Contains(gaps[0], "两面不配对") {
		t.Fatalf("★ 只删父表时判据没报出「两面不配对」（%v）⇒ ⇒ 门没牙", gaps)
	}

	// ★ 变异臂 2：★ 两面都在，但 WHERE 不同（★ 删的不是同一批行）。
	diffWhere := map[string][]string{
		"public.session_bodies":     {"tenant_id = $1 AND session_id = $2"},
		"public.session_bodies_hot": {"tenant_id = $1"},
	}
	gaps = surfaceDeleteGaps(diffWhere)
	if len(gaps) != 1 || !strings.Contains(gaps[0], "找不到同 WHERE 的孪生腿") {
		t.Fatalf("★ WHERE 不同时判据没报出「找不到同 WHERE 的孪生腿」（%v）⇒ ⇒ 门没牙", gaps)
	}

	// ★ 变异臂 3：★ 多一条 hot 腿（★ 多删了 hot 面上不该删的行）。
	extraHot := map[string][]string{
		"public.session_bodies":     {"tenant_id = $1 AND session_id = $2"},
		"public.session_bodies_hot": {"tenant_id = $1 AND session_id = $2", "tenant_id = $1"},
	}
	gaps = surfaceDeleteGaps(extraHot)
	if len(gaps) != 1 || !strings.Contains(gaps[0], "两面不配对") {
		t.Fatalf("★ 多一条 hot 腿时判据没报出「两面不配对」（%v）⇒ ⇒ 门没牙", gaps)
	}

	// ★ 变异臂 4：★ 归一化 ⇒ 只差空白的 WHERE 应当**判为配对**。
	spaced := map[string][]string{
		"public.session_bodies":     {"tenant_id = $1  AND   session_id = $2"},
		"public.session_bodies_hot": {"tenant_id = $1 AND session_id = $2"},
	}
	if gaps := surfaceDeleteGaps(spaced); len(gaps) != 0 {
		t.Fatalf("★ 只差空白的 WHERE 被判成不配对（%v）⇒ ⇒ 判据把排版差异当语义差异", gaps)
	}

	// ★ 变异臂 5（★ 端到端）：★ 走【解析 + 归一化】整条链 ⇒
	// ★ 排版不同（换行 / 多空格）的两条腿仍然配对。
	// ★ ★ 这条臂防的是「解析器把排版差异吃成了语义差异」或反过来。
	src := "tx.Exec(ctx, `DELETE FROM public.session_bodies_hot\n  WHERE tenant_id = $1 AND session_id = $2`)\n" +
		"tx.Exec(ctx, `DELETE FROM public.session_bodies WHERE tenant_id = $1 AND session_id = $2`)"
	parsed := parseDeleteLegs(src)
	if len(parsed["public.session_bodies"]) != 1 || len(parsed["public.session_bodies_hot"]) != 1 {
		t.Fatalf("★ 解析器没抓到两条腿（%v）⇒ ⇒ 夹具写法与解析器不匹配", parsed)
	}
	if gaps := surfaceDeleteGaps(parsed); len(gaps) != 0 {
		t.Fatalf("★ 端到端（解析+归一化）把配对的两腿判成不配对（%v）⇒ ⇒ 整条链有偏", gaps)
	}
}
