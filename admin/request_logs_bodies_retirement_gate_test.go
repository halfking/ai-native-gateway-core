//go:build !integration

package admin

import (
	"fmt"
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
// `db.SessionBodiesSourceSQL()` 取源 ⇒ 该文件源码里**一个 bodies 关系名字面量都没有**。
// ⇒ 引用这一段当现状会误导下一个人，必须连着看 scanV1BodiesReaders 末尾
// 那段「间接读方并入总体」的注释（本轮新增，否则总体会静悄悄少两个人）。
//
// 但它读的 `request_body` / `response_body` **不在 canonical 合同里**——
// 合同是 `_request_logs` 自己的 118 列，而 bodies 是**另一张表**。
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
	// 期望的 4 个是 _request_logs_bodies / _hot / _with_current_month /
	// _hot 的包装视图；少了任何一个都会让这道门变成恒零。
	if len(out) < 3 {
		t.Fatalf("v1 bodies 关系名只推导出 %d 个（期望 ≥3）：%v —— "+
			"这不是「bodies 腿就这么点」，是上游 SSOT 推导坏了", len(out), out)
	}
	return out
}

// v1BodiesReadPattern 由 SSOT 构出，**最长在前**（Go 的 alternation 是
// leftmost-first，不是 longest-match；写成 (_request_logs_bodies|_request_logs_bodies_hot)
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
	skipDir := map[string]bool{
		".git": true, "docs": true, "node_modules": true, "vendor": true,
		".audit": true, ".audit-validation": true, ".audit-workspace": true,
	}
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
// 登记一项要写清三件事，缺一不可（沿用 indirectReader 的形状）：
// 读哪张 bodies 关系、退役后由什么替代、为什么那个替代够用。
// 只写文件名不写替代来源的登记，等于把「已看过」和「没问题」混成一句话。
//
// # 2026-10-13 停写复审计轮：25 文件逐点评估完毕，本表首次填齐
//
// 评估依据（真库实测，本地 host-first 库，翻转点 2026-10-13 07:01:04）：
//
//	_request_logs / _request_logs_bodies        35,506 行（冻结）
//	session_turns / session_bodies            12,011 行（翻转后持续承接）
//	v1_only（bodies 只在 v1）                 23,795 行 = 探针观测 22,951
//	                                          （无会话头，R51 设计不镜像）
//	                                          + 回环/非终态 844
//	带会话头而未镜像（drift 校验器口径的真损失候选）= 0
//
// ⇒ 会话族对「应镜像流量」的正文覆盖是完整的；DROP _request_logs_bodies
// 不丢任何用户可见正文。会丢的只有两类已登记可接受损失：探针观测正文
// （其事实主存 node_probe_runs 族）与 title/summary 回环正文（生成器中间
// 产物，非用户轮次）。生产删表前由 owner 在生产库复核同一口径计数。
//
// 详见 docs/audit/2026-10-13-s3-wave4-bodies-retirement-assessment.md。
//
// bodiesSwitchRetireNote 是「经切换层取 bodies」读方的共同退役理由；
// 抽成常量避免十几份手抄漂移（判据纪律：理由与判据同源）。
const bodiesSwitchRetireNote = "退役后替代：切换层 v1 臂删除，SessionFamilyBodiesSourceSQL（session_bodies hot∪父表，request_id/request_body/response_body/outbound_body 四列合同）转唯一来源。" +
	"为什么够用：取源已走 db.SessionBodiesSourceSQL()（storage.session_bodies_native_read=true ⇒ 会话臂，非「改指切换层但停在 v1 臂」的假迁移）；" +
	"2026-10-13 停写复审计实测带会话头未镜像行=844 全为回环/非终态（真损失 0），会话臂覆盖全部用户可见正文。"

var v1BodiesReaders = map[string]indirectReader{
	// ★ 2026-10-20（迁移 890，v1 族真删）：本表**已清空**。
	//
	// 24 条登记的对象全是「读 v1 bodies 腿」的文件；而 890 之后
	// **没有任何生产文件再读 v1 bodies 腿**（bodies 读端只剩会话族）⇒
	// 「bodies 退役评估」这件事已经没有对象。
	//
	// ⇒ 本表保留（不删）：它守的是「**不许出现新的 v1 bodies 读方**」。
	//
	// ⚠ 不要在这里手填「已知的例外」—— 已退役关系的残留引用登记在
	// retiredV1ReferenceExceptions（生产缺陷台账）。
}

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

	// ★ 890 之后（2026-10-20）：总体与登记表都必须为空。
	//
	// v1 族被物理删除 ⇒ bodies 腿没有 v1 可退役 ⇒ 「bodies 退役评估」没有对象。
	// ⇒ 本门从「登记表覆盖全部 bodies 读方」翻成
	//「**登记表必须为空，且实测 bodies 读方必须为空**」。
	if len(v1BodiesReaders) > 0 {
		t.Errorf("v1 bodies 读方登记仍有 %d 条 —— 890 之后 v1 族已物理删除，"+
			"没有任何生产文件再读 v1 bodies 腿。", len(v1BodiesReaders))
	}
	if len(measured) > 0 {
		var names []string
		for f, n := range measured {
			names = append(names, fmt.Sprintf("%s(%d)", f, n))
		}
		sort.Strings(names)
		t.Errorf("%d 个文件仍被实测为读 v1 bodies 腿：\n  %s\n"+
			"⇒ 它们运行时会 42P01（890 已删这些关系）。", len(measured), strings.Join(names, "\n  "))
	}

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
	for f, n := range measured {
		t.Logf("  %-58s %d", f, n)
	}

	if len(unregistered) > 0 {
		t.Errorf("%d 个文件读 v1 bodies 腿但未登记 —— 在逐点评估之前，"+
			"**不能**把 _request_logs_bodies 判定为可删：\n  %s", len(unregistered),
			strings.Join(unregistered, "\n  "))
	}
	if len(stale) > 0 {
		t.Errorf("%d 条 bodies 读方登记已失效（实测 0 处）——多半是读法变了：\n  %s",
			len(stale), strings.Join(stale, "\n  "))
	}

	// ★ 阳性对照：扫描器必须仍能看见 v1 bodies 引用。
	//
	// ⚠ 没有这一条，「实测为 0」无法与「扫描器坏了」区分 —— 这正是
	// TestV1BodiesGateIsNotSilentlyVacuous 当年要防的失效形态。
	pat := v1BodiesReadPattern(t)
	if !pat.MatchString("FROM _request_logs_bodies_hot rb") {
		t.Error("v1BodiesReadPattern 匹配不上 v1 bodies 热表 —— 扫描器没有区分力，" +
			"上面「实测为 0」是空洞的")
	}
	if !pat.MatchString("LEFT JOIN request_logs_bodies_with_current_month rb") {
		t.Error("v1BodiesReadPattern 匹配不上 v1 bodies 视图 —— 扫描器没有区分力，" +
			"上面「实测为 0」是空洞的")
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

	// ★ 890 之后（2026-10-20）：**总体为空是正确状态**，不是「量具坏了」。
	//
	// 原来这里是一条地板（总体为 0 ⇒ Fatal），因为那时总体为空只可能意味着
	// 「扫描器坏了」。890 之后 v1 族被物理删除 ⇒ bodies 读方总体真的归零。
	//
	// ⇒ 地板翻成：**总量为空必须是因为扫描器扫过而没命中**。
	// 判据：扫描器的模式必须仍能认出 v1 bodies 形态（阳性对照）。
	if len(measured) == 0 {
		pat := v1BodiesReadPattern(t)
		if !pat.MatchString("FROM _request_logs_bodies_hot rb") {
			t.Fatal("v1 bodies 读方实测为 0，**且**扫描模式认不出 v1 bodies 形态 —— " +
				"这不是「bodies 腿已经没人读了」，是扫描器坏了（SSOT 推导或路径过滤出了问题）。")
		}
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

	// ★ 890 之后（2026-10-20）：间接读方表已清空 ⇒ 「间接 bodies 读方」必须为空。
	//
	// 原来这里是一条地板（want 为 0 ⇒ Fatal），因为那时它只可能意味着
	// 「登记被删了 / SSOT 推不出 bodies 关系名」。890 之后 bodies 腿的切换层
	// 已经恒等于会话族（db.SessionBodiesSourceSQL = SessionFamilyBodiesSourceSQL），
	// **不再存在间接读 v1 bodies 的文件** ⇒ 0 是正确值。
	if len(want) > 0 {
		t.Errorf("仍有 %d 个文件被登记为间接 bodies 读方 —— 890 之后 bodies 切换层恒为会话族，"+
			"不再有间接读 v1 bodies 的文件：\n  %s", len(want), strings.Join(want, "\n  "))
	}
	if len(missing) > 0 {
		t.Errorf("这些文件登记为间接 v1 bodies 读方，但没出现在实测总体里：%v\n"+
			"⇒ scanV1BodiesReaders 末尾的「间接读方并入总体」被删掉或失效了。", missing)
	}

	// ★ 阳性对照：「并入总体」这段逻辑本身必须仍然有效。
	//
	// ⚠ 合成一个间接 bodies 登记 + 一个消费点，证明总体确实会把它们并进来。
	// （否则表空着的时候，这段逻辑恒不执行、没人知道它是否还在。）
	synthetic := map[string]int{}
	if !mergeIndirectBodiesReaders(synthetic, map[string]bool{"synthetic/indirect.go": true},
		map[string]bool{"synthetic/consumer.go": true}, "synthetic/indirect.go") {
		t.Error("mergeIndirectBodiesReaders 没有把已登记的间接 bodies 读方并入总体 —— " +
			"「间接读方并入总体」这段逻辑坏了")
	}
	if len(synthetic) != 2 {
		t.Errorf("mergeIndirectBodiesReaders 并入后总体只有 %d 个文件，期望 2（间接读方 + 消费点）", len(synthetic))
	}
}

// mergeIndirectBodiesReaders 是 scanV1BodiesReaders 末尾「把间接 bodies 读方
// 与其消费点并入总体」那段逻辑的**可测版本**。
//
// 为什么抽成函数：890 之后间接读方表为空 ⇒ 那段逻辑在真实数据上不再执行 ⇒
// 「它还在不在、还对不对」没人能证明。抽出来才能用合成数据直接驱动它。
//
// 参数：
//   - out：总体（原地修改）
//   - indirect：已登记为间接 bodies 读方的文件集合
//   - consumers：已登记切换层的消费点集合
//   - file：当前正在处理的间接读方文件
//
// 返回：file 是否被并入总体。
func mergeIndirectBodiesReaders(out map[string]int, indirect, consumers map[string]bool, file string) bool {
	if !indirect[file] {
		return false
	}
	if out[file] == 0 {
		out[file] = 1
	}
	for c := range consumers {
		if out[c] == 0 && c != file {
			out[c] = 1
		}
	}
	return true
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
	fjPattern := v1BodiesReadPattern(t)

	var joinOnly []string
	for f := range measured {
		src, err := os.ReadFile(filepath.Join(root, f))
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		if isJoinOnlyBodiesReader(string(src), fromOnly, fjPattern) {
			joinOnly = append(joinOnly, f)
		}
	}
	sort.Strings(joinOnly)

	// ★ 890 之后（2026-10-20）：**「只 JOIN 不 FROM」的读方为 0 是正确状态**。
	//
	// 原来这里是一条「必须 ≥1」的地板，因为那时总体里至少有两个只 JOIN 的读方
	// （admin/session_export.go / admin/session_compare.go）。890 之后没有文件
	// 再读 v1 bodies ⇒ 这个盲区**在总体里已经不存在**。
	//
	// ⇒ 地板翻成：**总体为 0 是正确的，但「只 JOIN 也能被看见」这条能力必须仍在**。
	// 判据：用**合成源码**证明 isJoinOnlyBodiesReader 仍能认出只 JOIN 的形态。
	if len(joinOnly) > 0 {
		t.Errorf("总体里出现了 %d 个「只 JOIN 不 FROM」的 bodies 读方 —— 890 之后不该再有：\n  %v",
			len(joinOnly), joinOnly)
	}
	joinOnlySynthetic := "SELECT rl.request_id\n" +
		"FROM public.session_turns_hot rl\n" +
		"LEFT JOIN request_logs_bodies_with_current_month rb ON rb.request_id = rl.request_id"
	if !isJoinOnlyBodiesReader(joinOnlySynthetic, fromOnly, fjPattern) {
		t.Error("isJoinOnlyBodiesReader 认不出「只 JOIN 不 FROM」的 bodies 读方 —— " +
			"扫描口径被收窄回 from-only 了（§9.226 修掉的盲区复发）")
	}
	fromSynthetic := "SELECT rb.request_id FROM _request_logs_bodies_hot rb"
	if isJoinOnlyBodiesReader(fromSynthetic, fromOnly, fjPattern) {
		t.Error("isJoinOnlyBodiesReader 把「有 FROM」的读方判成只 JOIN —— 判据写坏了")
	}
	t.Logf("只 JOIN 不 FROM 的 bodies 读方：%d 个 %v", len(joinOnly), joinOnly)
}

// isJoinOnlyBodiesReader 报告一段源码是否是「只 JOIN 不 FROM」的 bodies 读方。
//
// 抽成纯函数是为了能用**合成源码**直接驱动它（总体为空时，真实样本证明不了
// 「只 JOIN 也算数」这条能力还在）。
func isJoinOnlyBodiesReader(src string, fromOnly, fjPattern *regexp.Regexp) bool {
	hasFrom, hasJoin := false, false
	for _, line := range strings.Split(src, "\n") {
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
	return hasJoin && !hasFrom
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
		for _, p := range v1BodiesReaderProblems(file, e, bodiesRel) {
			t.Errorf("%s：%s", file, p)
		}
	}

	// ★ 890 之后（2026-10-20）：表为空 ⇒ 逐条校验不执行 ⇒ 判据必须有牙。
	//
	// ⇒ 用**合成条目**证明自检逻辑有效：缺 Reason / 缺 ResolvesTo /
	// ResolvesTo 不在 bodies 族里 / Family 为空 —— 四种都必须被判出问题；
	// 一条各项都合规的必须不被判出问题。
	bad := map[string]indirectReader{
		"缺 Reason":     {Family: familyBodies, ResolvesTo: "_request_logs_bodies"},
		"缺 ResolvesTo": {Family: familyBodies, Reason: "合成条目"},
		"ResolvesTo 不在 bodies 族": {
			Family: familyBodies, ResolvesTo: "not_a_bodies_relation", Reason: "合成条目",
		},
		"Family 为空": {ResolvesTo: "_request_logs_bodies", Reason: "合成条目"},
	}
	for name, e := range bad {
		if len(v1BodiesReaderProblems(name, e, bodiesRel)) == 0 {
			t.Errorf("合成条目「%s」没有被自检逻辑判出问题 —— 自检逻辑没有牙"+
				"（表空着的时候，这是唯一还能证明它有效的手段）", name)
		}
	}
	ok := indirectReader{
		Family:     familyBodies,
		ResolvesTo: "_request_logs_bodies",
		Reason:     "合成条目：各项合规",
	}
	if problems := v1BodiesReaderProblems("synthetic/ok.go", ok, bodiesRel); len(problems) != 0 {
		t.Errorf("一个各项都合规的合成条目被判出问题 %v —— 自检逻辑本身坏了", problems)
	}
}

// v1BodiesReaderProblems 是一条 bodies 读方登记的**自检**（返回问题列表，空 = 合格）。
func v1BodiesReaderProblems(file string, e indirectReader, bodiesRel map[string]bool) []string {
	var problems []string
	if strings.TrimSpace(e.Reason) == "" {
		problems = append(problems, "Reason 为空——空理由的登记等于没有登记；"+
			"这一项要写清「退役后由什么替代、为什么那个替代够用」")
	}
	if strings.TrimSpace(e.ResolvesTo) == "" {
		problems = append(problems, "ResolvesTo 为空。不写它，「它读的是 v1 bodies」就是一句断言")
	} else if !bodiesRel[strings.ToLower(e.ResolvesTo)] {
		problems = append(problems, "ResolvesTo 不在 v1 bodies 关系名集合里")
	}
	if strings.TrimSpace(e.Family) == "" {
		problems = append(problems, "Family 为空。这一项用于说明它读的是哪一族")
	}
	return problems
}
