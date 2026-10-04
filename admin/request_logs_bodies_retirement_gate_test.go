//go:build !integration

package admin

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// v1 族里 **bodies 腿**的退役门（审计 §9.226）。
//
// # 这道门补的是一个「量具看不见，所以报 clean」的口子
//
// `TestRequestLogsRetirementExposure` 把文件分成 breaks / undercounts / clean，
// 判据是**该文件的 v1 SQL 字面量里出现了哪些 canonical 合同列**。
// `admin/session_export.go` 因此落在 **clean**：
//
//	FROM ` + dbpkg.SessionFamilyTurnsForSessionSQL() + ` rl
//	LEFT JOIN request_logs_bodies_with_current_month rb ON rb.request_id = rl.request_id
//	… COALESCE(rb.request_body, '{}'::jsonb) AS request_body …
//
// ⚠ **上面这两行是 §9.226 当时的源码。§9.230 之后不是了**：bodies 腿改成经
// `sessionBodiesFromSQL()` 取源 ⇒ 该文件源码里**一个 bodies 关系名字面量都没有**。
// ⇒ 引用这一段当现状会误导下一个人，必须连着看 scanV1BodiesReaders 末尾
// 那段「间接读方并入总体」的注释（本轮新增，否则总体会静悄悄少两个人）。
//
// 但它读的 `request_body` / `response_body` **不在 canonical 合同里**——
// 合同是 `request_logs` 自己的 118 列，而 bodies 是**另一张表**。
// ⇒ 「没有命中暴露列」在这里的含义是**本量具不测 bodies**，
// 不是「这个文件退役后照常工作」。
//
// 这个区分是承重的，因为它指向的失效形态是本文件族反复写下的那句：
// **一个返回 18% 数据的查询，和一个正常工作的查询，从外面看一模一样。**
// bodies 这条更糟一档：上面那句 SQL 写的是 `COALESCE(rb.request_body, '{}')`，
// 所以 DROP 之后它**不报错、不返回空、照常导出一个完整的会话包**——
// 只是每条消息的正文都是 `{}`。用户拿到的是「看起来成功的空数据」。
//
// # 为什么不能用「clean 就是安全」
//
// 事故形态与 §9.167 同型：那里的三个文件因为别名正则丢了一个限定符，
// 拿到空列集合后被判成 `repoint-safe`；这里是 bodies 列不在合同里，
// 于是被判成 `clean`。两者的错误方向一致：**没有证据被当成了通过。**
// §9.167 的处置是「零列即错误，不是日志」；这里照抄那条处置。
//
// # 口径
//
// 与 requestLogsReadInventory 同源（同一批非 _test.go 生产文件、同样的
// 注释剔除），只把关系名收窄到 bodies 腿。关系名**从 SSOT 推导**而不是手抄：
// v1BaseTableNames + viewChainNames 里凡是名字含 `bodies` 的都算
// （本项目栽过同族错误：再抄第三份名单，两份工具迟早分叉）。

// v1BodiesRelations 返回 v1 族里 bodies 腿的全部关系名。
func v1BodiesRelations(t *testing.T) []string {
	t.Helper()
	var out []string
	for _, n := range append(append([]string{}, v1BaseTableNames...), viewChainNames(t)...) {
		l := strings.ToLower(n)
		if l == "" || !strings.Contains(l, "bodies") {
			continue
		}
		out = append(out, l)
	}
	sort.Strings(out)
	// 地板断言：bodies 腿被推导空了 ⇒ 上游 SSOT 坏了，不是「族里就这么点」。
	// 期望的 4 个是 request_logs_bodies / _hot / _with_current_month /
	// _hot 的包装视图；少了任何一个都会让这道门变成恒零。
	if len(out) < 3 {
		t.Fatalf("v1 bodies 关系名只推导出 %d 个（期望 ≥3）：%v —— "+
			"这不是「bodies 腿就这么点」，是上游 SSOT 推导坏了", len(out), out)
	}
	return out
}

// v1BodiesReadPattern 由 SSOT 构出，**最长在前**（Go 的 alternation 是
// leftmost-first，不是 longest-match；写成 (request_logs_bodies|request_logs_bodies_hot)
// 会先匹配前缀，别名整支被丢掉——v1AliasRe 的注释记过同族事故）。
func v1BodiesReadPattern(t *testing.T) *regexp.Regexp {
	t.Helper()
	names := v1BodiesRelations(t)
	alt := make([]string, len(names))
	for i, n := range names {
		alt[i] = regexp.QuoteMeta(n)
	}
	sort.Slice(alt, func(i, j int) bool { return len(alt[i]) > len(alt[j]) })
	return regexp.MustCompile(`(?i)\b(?:from|join)\s+(` + strings.Join(alt, "|") + `)\b`)
}

// scanV1BodiesReaders 数每个生产文件里 bodies 腿的读点数。
func scanV1BodiesReaders(t *testing.T, root string) map[string]int {
	t.Helper()
	pat := v1BodiesReadPattern(t)
	out := map[string]int{}
	skipDir := map[string]bool{".git": true, "docs": true, "node_modules": true, "vendor": true}
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if skipDir[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		name := d.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			return nil
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		n := 0
		for _, line := range strings.Split(string(src), "\n") {
			if !pat.MatchString(line) {
				continue
			}
			trimmed := strings.TrimSpace(line)
			// 注释不是读点（与 requestLogsReadInventory 同一处置）。
			if strings.HasPrefix(trimmed, "//") || strings.HasPrefix(trimmed, "*") ||
				strings.HasPrefix(trimmed, "/*") {
				continue
			}
			n++
		}
		if n > 0 {
			rel, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			out[filepath.ToSlash(rel)] = n
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk repo: %v", err)
	}
	// 间接 bodies 读方并入总体（审计 §9.230）。
	//
	// ⚠ **不加这一段就是一次假绿的前奏。** `admin/session_bodies_source.go`
	// 的 v1 那一臂是一行 `return "request_logs_bodies_with_current_month rb"` ——
	// 它**没有** FROM/JOIN 关键字，所以按行扫描测到 0 处、整个文件不进总体。
	// 而它的两个消费点（session_export.go / session_compare.go）在改成经这个
	// 开关取 bodies 源之后，源码里也不再有任何 bodies 关系名字面量。
	// ⇒ 三处一起从总体里消失，总体从 26 文件/44 调用点缩到 24/42，
	// 而**一个 bodies 依赖都没被评估过**。这是本文件头注释警告的那种
	// 「扫描器扫不到 ⇒ 门全绿」的前半段，只是这次门仍然红，只是红得**少了两个人**。
	//
	// 处置与 §9.49 的两张表分工一致：字面量读方按行扫，间接读方由
	// indirectRequestLogsReaders 登记并在这里并入。判据是 `ResolvesTo`
	// 落在 v1 bodies 关系名集合里 —— 不是「文件名叫 session_bodies 就收」。
	for file, e := range indirectRequestLogsReaders {
		rel := strings.ToLower(e.ResolvesTo)
		if !bodiesRelIsV1Bodies(t, rel) {
			continue // 读的是 turns 族，与本门总体无关
		}
		if out[file] == 0 {
			out[file] = 1
		}
		// ★ 消费点也要并进来（§9.232）。它们把 bodies 腿改走切换层后，
		// 源码里不再有 bodies 关系名字面量 ⇒ 会**逐个**从本门总体里消失。
		// 而本门**本来就故意红** ⇒ 少 11 个还是少 15 个都不改退出码。
		// 这一段是 §9.230.3 那个坑的批量版本。
		if e.SwitchFunc != "" {
			for c := range indirectSourceConsumers(t, root) {
				if out[c] == 0 && c != file {
					out[c] = 1
				}
			}
		}
	}
	return out
}

// bodiesRelIsV1Bodies 判断一个登记的 ResolvesTo 是否落在 v1 bodies 关系名集合里。
func bodiesRelIsV1Bodies(t *testing.T, rel string) bool {
	t.Helper()
	for _, n := range v1BodiesRelations(t) {
		if rel == n || rel+"_with_current_month" == n || rel+"_hot" == n {
			return true
		}
	}
	return false
}

// v1BodiesReaders 是**已复核**的 bodies 读方登记。
//
// ⚠ **本表是空的，而这是有意的。**
// bodies 腿从未被逐点评估过：`TestRequestLogsRetirementExposure` 判它 clean，
// 不是因为它安全，是因为合同里没有 bodies 列。空表 + 下面的双向门 =
// **故意红**，直到有人逐条评估并登记。
//
// 登记一项要写清三件事，缺一不可（沿用 indirectReader 的形状）：
// 读哪张 bodies 关系、退役后由什么替代、为什么那个替代够用。
// 只写文件名不写替代来源的登记，等于把「已看过」和「没问题」混成一句话。
var v1BodiesReaders = map[string]indirectReader{}

// TestV1BodiesReadersAreAssessed 是 bodies 腿的双向门。
//
// 两条方向：
//   - 实测到 bodies 读点但没登记 ⇒ **红**。这是本门当前的状态。
//   - 登记了但实测为 0 ⇒ 文件被 repoint 了（或读法变了），登记应删。
//
// 门红时的提示必须**可执行**：指出替代来源的方向，而不是只说「有问题」。
func TestV1BodiesReadersAreAssessed(t *testing.T) {
	root := repoRootFromCaller(t)
	measured := scanV1BodiesReaders(t, root)

	var unregistered, stale []string
	for f, n := range measured {
		if _, ok := v1BodiesReaders[f]; !ok {
			unregistered = append(unregistered, f+": "+strconv.Itoa(n)+" 处")
		}
	}
	for f := range v1BodiesReaders {
		if _, ok := measured[f]; !ok {
			stale = append(stale, f)
		}
	}
	sort.Strings(unregistered)
	sort.Strings(stale)

	total := 0
	for _, n := range measured {
		total += n
	}
	t.Logf("v1 bodies 读方：%d 文件 / %d 调用点（已登记 %d）",
		len(measured), total, len(v1BodiesReaders))
	// 把「当前应当是红的」写进输出。理由：本门与 S4 门、回填门一样是**故意红**的
	// 负向判据，而 CI 输出里一个不带标记的 FAIL 会被读成回归。
	// ⚠ 措辞不能含糊：它是**预期红**，但**不代表 bodies 已可删**。
	if len(unregistered) > 0 {
		t.Logf("【预期红】本门故意红：bodies 腿尚未逐点评估。红了 = 判据在拦；" +
			"全绿 = 登记表已覆盖全部 bodies 读方（那时才谈得上 request_logs_bodies 可删）。")
	}
	for f, n := range measured {
		t.Logf("  %-58s %d", f, n)
	}

	if len(unregistered) > 0 {
		t.Errorf("%d 个文件读 v1 bodies 腿但未登记 —— 在逐点评估之前，"+
			"**不能**把 request_logs_bodies 判定为可删：\n  %s\n\n"+
			"为什么这必须红而不是记一条日志：DROP 之后这些 JOIN 不会报错，"+
			"它们读到 NULL，而多数调用点写的是 COALESCE(…, '{}') —— "+
			"于是导出/摘要照常成功，只是每条消息的正文为空。"+
			"「返回 18%% 数据的查询和正常工作的查询看起来一样」在这里还要更糟一档。\n\n"+
			"处置方向：会话族有 session_bodies 表"+
			"（promote 函数见 db/db.go:3267，回填工具 cmd/tools/backfill_session_bodies）。"+
			"但 db 包**没有** bodies 源的 SQL helper —— turns 侧有 "+
			"SessionFamilyTurnsSourceSQL / SessionFamilyTurnsForSessionSQL，bodies 侧没有。"+
			"⇒ 先补那个 helper，再逐个把 LEFT JOIN request_logs_bodies* 改指过去，"+
			"然后在本表登记（含替代来源与充分性理由）。",
			len(unregistered), strings.Join(unregistered, "\n  "))
	}
	if len(stale) > 0 {
		t.Errorf("%d 条 bodies 读方登记已失效（实测 0 处）——多半是读法变了：\n  %s",
			len(stale), strings.Join(stale, "\n  "))
	}
}

// TestV1BodiesGateIsNotSilentlyVacuous 挡住「读方集合为空 ⇒ 门全绿」这种
// 最省事的假绿。
//
// 这道门当前**故意红**。它最可能的失效方式不是变绿，而是某次重构让
// scanV1BodiesReaders 扫不到东西，于是 0 个未登记、0 条失效、门全绿，
// 而 bodies 依赖一个都没被评估。⇒ 总体为 0 必须判红。
func TestV1BodiesGateIsNotSilentlyVacuous(t *testing.T) {
	root := repoRootFromCaller(t)
	measured := scanV1BodiesReaders(t, root)
	if len(measured) == 0 {
		t.Fatal("v1 bodies 读方实测为 0 个文件 —— 这不是「bodies 腿已经没人读了」，" +
			"是扫描器坏了：SSOT 推导或路径过滤出了问题。在这种状态下上面那道门会全绿，" +
			"而一个 bodies 依赖都没被评估。")
	}
	if len(v1BodiesRelations(t)) == 0 {
		t.Fatal("v1 bodies 关系名集合为空（地板断言本该拦住，见 v1BodiesRelations）")
	}
}

// TestV1BodiesScanIncludesRegisteredIndirectReaders 钉住「间接读方并入总体」本身。
//
// # 为什么这道门不能省
//
// scanV1BodiesReaders 末尾那段「把 indirectRequestLogsReaders 里 ResolvesTo 落在
// v1 bodies 的文件并进总体」是 §9.230 加的。把**它**删掉，总体从 25 文件/43 调用点
// 悄悄缩回 24/42，而 `TestV1BodiesReadersAreAssessed` **照样红**——
// 它本来就是故意红的，24 个和 25 个都不改变退出码。
// ⇒ 删掉修复之后没有任何门变绿或变红，这个修复就是**未经证实的**。
//
// 判据不能写成「总体数 ≥ 25」：那是把一个会随别的读方增减而漂的数钉死，
// 下一个改 bodies 读方的人会为了过门去调门槛。⇒ 判据是**集合关系**：
// 凡登记为间接 bodies 读方的文件，必须逐个出现在实测总体里。
func TestV1BodiesScanIncludesRegisteredIndirectReaders(t *testing.T) {
	root := repoRootFromCaller(t)
	measured := scanV1BodiesReaders(t, root)
	rels := map[string]bool{}
	for _, n := range v1BodiesRelations(t) {
		rels[n] = true
	}

	var want, missing []string
	for file, e := range indirectRequestLogsReaders {
		rel := strings.ToLower(e.ResolvesTo)
		isBodies := rels[rel] || rels[rel+"_with_current_month"] || rels[rel+"_hot"]
		if !isBodies {
			continue // 读的是 turns 族，与本门总体无关
		}
		want = append(want, file)
		if measured[file] == 0 {
			missing = append(missing, file)
		}
	}
	sort.Strings(want)
	sort.Strings(missing)
	t.Logf("登记为间接 bodies 读方的文件 %d 个：%v", len(want), want)
	if len(want) == 0 {
		t.Fatal("没有任何文件被登记为间接 bodies 读方 —— " +
			"要么 indirectRequestLogsReaders 的 bodies 登记被删了（本该有一项），" +
			"要么 v1BodiesRelations 推不出 bodies 关系名。两种都让这道门失去意义。")
	}
	if len(missing) > 0 {
		t.Errorf("这些文件登记为间接 v1 bodies 读方，但没出现在实测总体里：%v\n"+
			"⇒ scanV1BodiesReaders 末尾的「间接读方并入总体」被删掉或失效了。"+
			"它们的 v1 bodies 依赖会从退役证据里消失，而门**仍然红**"+
			"（本来就红，25 个还是 24 个都不改退出码）。", missing)
	}
}

// TestV1BodiesScanSeesJoinOnlyReaders 钉住「只有 JOIN、没有 FROM」也必须算数。
//
// # 为什么这道门不能只靠「红/绿」自证
//
// TestV1BodiesReadersAreAssessed 的判据是「登记表覆盖全部 bodies 读方」。
// 若有人把扫描模式从 `from|join` 收窄回 `from`，实测会**照样红**——
// 26 个文件变成 23 个，剩下的照样没登记。门红得像在正常工作，
// 而 `admin/session_export.go` / `admin/session_compare.go` 这两个
// **只 JOIN 不 FROM** 的读方已经悄悄从总体里消失了。
//
// 这正是本轮 §9.226 在 requestLogsReadInventory 上实测到的原缺陷
// （那里是 3 个文件对门完全不可见），同一个坑在两个门上各踩一次。
// ⇒ 必须有一条**不依赖红绿**的判据：总体里必须存在只靠 JOIN 才被看见的文件。
func TestV1BodiesScanSeesJoinOnlyReaders(t *testing.T) {
	root := repoRootFromCaller(t)
	measured := scanV1BodiesReaders(t, root)

	// 同一批文件、同样的注释剔除，只把 join 去掉。
	fromOnly := regexp.MustCompile(`(?i)\bfrom\s+(?:` +
		strings.Join(quoteAll(v1BodiesRelations(t)), "|") + `)\b`)

	// ⚠ 两个模式都**提到循环外**。第一版把 v1BodiesReadPattern(t) 写在
	// per-file 循环里，于是每读一个文件就重建一次正则（含 SSOT 推导与排序）。
	// 26 个文件不致命，但它把这条门从「毫秒」拖成「看着像卡住」——
	// 而一个让人觉得卡的门，人会先去怀疑它而不是去读它。
	fjPattern := v1BodiesReadPattern(t)

	var joinOnly []string
	for f := range measured {
		src, err := os.ReadFile(filepath.Join(root, f))
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		hasFrom, hasJoin := false, false
		for _, line := range strings.Split(string(src), "\n") {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "//") || strings.HasPrefix(trimmed, "*") ||
				strings.HasPrefix(trimmed, "/*") {
				continue
			}
			if fromOnly.MatchString(line) {
				hasFrom = true
			}
			if fjPattern.MatchString(line) {
				hasJoin = true
			}
		}
		if hasJoin && !hasFrom {
			joinOnly = append(joinOnly, f)
		}
	}
	sort.Strings(joinOnly)
	if len(joinOnly) == 0 {
		t.Errorf("总体里没有任何「只 JOIN 不 FROM」的 bodies 读方 —— " +
			"这正是 §9.226 修掉的盲区形状。若这里为 0，要么扫描模式被收窄回 from-only，" +
			"要么这些读方已经改完；两者都需要有人显式确认，不能默认。")
	}
	t.Logf("只 JOIN 不 FROM 的 bodies 读方：%d 个 %v", len(joinOnly), joinOnly)
}

func quoteAll(in []string) []string {
	out := make([]string, len(in))
	for i, s := range in {
		out[i] = regexp.QuoteMeta(s)
	}
	return out
}

// TestV1BodiesReadersAreDeclaredWell 挡住「登记了但说不清」。
//
// # 为什么这道门是必需的，而不是「照抄旁边那个」
//
// TestV1BodiesReadersAreAssessed 的判据是「登记表覆盖全部 bodies 读方」。
// 而 `indirectReader` 的三个字段**没有一个是 Go 编译器要求填的**：
// 加一条 `"admin/session_export.go": indirectReader{}` 就让这道门**变绿**，
// 实际评估量为零。
//
// 旁边那道 TestIndirectRequestLogsReadersAreDeclaredWell 就是干这个的
// （它还多校验了 `ResolvesTo` 必须是 `v1DirectTables` 里的真表名，
// 好让「它读的是 v1」这句话可核而不只是断言）。
// 本表复用同一个结构体 ⇒ **必须自己再写一道**，
// 否则「登记」与「填了一个空结构体」在退出码上不可区分。
//
// ⚠ 本表校验的是 **bodies** 关系名（从 SSOT 推导），不是 `v1DirectTables`：
// bodies 读方登记的 `ResolvesTo` 应当是它读的那张 bodies 表，
// 拿 turns 的集合去校它会把这个表变成一张「查不到任何东西」的表。
func TestV1BodiesReadersAreDeclaredWell(t *testing.T) {
	names := v1BodiesRelations(t)
	bodiesRel := map[string]bool{}
	for _, n := range names {
		bodiesRel[n] = true
	}
	for file, e := range v1BodiesReaders {
		if strings.TrimSpace(e.Reason) == "" {
			t.Errorf("%s：bodies 读方登记的 Reason 为空——空理由的登记等于没有登记；"+
				"这一项要写清「退役后由什么替代、为什么那个替代够用」", file)
		}
		if strings.TrimSpace(e.ResolvesTo) == "" {
			t.Errorf("%s：ResolvesTo 为空。不写它，「它读的是 v1 bodies」就是一句断言；\n"+
				"  写上表名之后，这句话可以被 v1BodiesRelations 核对。", file)
		} else if !bodiesRel[strings.ToLower(e.ResolvesTo)] {
			t.Errorf("%s：ResolvesTo = %q，不在 v1 bodies 关系名集合里。\n"+
				"  可选：%s\n"+
				"  若它其实指向别的表，应该从本表删掉，而不是改成一个不存在的表名。",
				file, e.ResolvesTo, strings.Join(names, " / "))
		}
		if strings.TrimSpace(e.Family) == "" {
			t.Errorf("%s：Family 为空。这一项用于说明它读的是哪一族（"+
				"本表的读者分不清「已评估」与「已登记但没看」）。", file)
		}
	}
}
