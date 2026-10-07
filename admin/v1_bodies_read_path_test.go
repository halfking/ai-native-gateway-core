//go:build !integration

package admin

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// v1_bodies_read_path_test.go —— 把 27 个 bodies 读方按「读源怎么来的」分成三类，
// 并钉住**其中一类此刻读的是 v1**（审计 §9.265）。
//
// # 这道门在补 §9.263 的一处过度结论
//
// §9.263 测到「bodies 的缺口是主表腿缺口的子集（bodies 独有缺口 = 0，5 个窗口全部成立）」，
// 据此写下：**bodies 腿不需要独立前置，27 个 bodies 读方与 76 个「必须迁」读方共用同一条前置**。
//
// ★ 那句话在**数据**上成立，在**代码路径**上不成立：
// 有 13 个 bodies 读方**只经切换层** `db.SessionBodiesSourceSQL()` 取源，
// 而该切换层的**默认支是 v1**：
//
//	func SessionBodiesSourceSQL() string {
//	    if settings.GetPlatformBool(SessionBodiesNativeReadSetting, false) {
//	        return SessionFamilyBodiesSourceSQL()   // 会话侧
//	    }
//	    return "request_logs_bodies_with_current_month"  // ★ v1（默认）
//	}
//
// 而 `storage.session_bodies_native_read` 在**本地**真库的 `settings_kv` 里**整行不存在**
// （同表的 `storage.session_turns_bodies_enabled` / `storage.admin_logs_native_turns_read` /
// `storage.session_final_full_enabled` **都在**）⇒ `GetPlatformBool(…, false)` 恒取 false。
//
// ⚠⚠ **「同表那两个也在」只在本地库成立**（审计 §9.265 七节，生产 252 只读实测）：
// 生产 `settings_kv` 里 `storage.` 前缀**只有 2 个键**
// （request_logs_write_enabled=true / session_final_full_enabled=false），
// 上列那两个在生产上**也不存在**。
// ⇒ **生产上 bodies 读端恒取 false 这条结论不变**（键不存在 ⇒ 取默认 false），
//   但**「bodies 是唯一漏掉的那一项」这个论证在生产上不成立** —— 别拿本地那两行当生产证据。
//   （`storage.admin_logs_native_turns_read` 默认 false 是**有意设计**，见
//    admin/logs_turns_source.go:40–55：原生源比视图少 27.6% 的行，按谓词形态禁止迁。）
//
// ⚠★ **只翻读端不够**：写端 `storage.session_turns_bodies_enabled`
// （domains/session/v2/session_writer_v2.go:674「S1b 灰度开关①」）在生产上**同样不存在**
// ⇒ 恒取 false ⇒ `RequestDeltaJSON`/`ResponseDeltaJSON` **根本没在写进 session_turns**。
// ⇒ 读端一旦切到会话侧，读到的就是**空正文** —— 即 §9.229.2 那个
// 「会话导出正文全变 `{}` 而接口仍 200」的形态。**两个开关必须成对翻。**

// ⇒ 那 13 个读方**此刻读的就是 v1**。停写之后它们会读到一份**冻结**的 v1 bodies，
// 而且**不报错**（多数调用点写 `COALESCE(rb.request_body, '{}')`）。
// §9.263 的「共用一条前置」漏了这一项。
//
// # 为什么「走切换层」不等于「读会话侧」
//
// 这是本文件最要紧的一句。切换层被登记、被接了 14 个消费点，看起来是迁移做完了；
// 但**开关的默认臂指向 v1**，所以「接了切换层」只说明「有了改指的**位置**」，
// 不说明「已经改指」。§9.232/§9.233 的登记只记了「消费点被机器识别」，
// 没人记「开关的默认臂是哪一边」。
//
// # 判据：认「调用」，不认「提到」
//
// 要把 `SessionBodiesSourceSQL`（切换层）和 `SessionFamilyBodiesSourceSQL`
// （会话侧）分开。两者的区别**不在前缀而在中间那个词**：
// 前者 `Session` 之后直接是 `Bodies`，后者是 `FamilyBodies`
// ⇒ `SessionBodiesSourceSQL` **不是** `SessionFamilyBodiesSourceSQL` 的子串，
// 裸 `strings.Contains` 本来就分得开。
//
// ⚠ 我第一版给正则加了 `(?<!Family)` 想「保险」，
// 结果 **RE2 不支持 lookbehind**（`error parsing regexp: invalid named capture`），
// `init()` 直接 panic ⇒ 整包 admin 门全崩。
// （同一轮里我在 RE2 上已经踩过不支持 lookahead，那是另一个坑。）
// ⇒ 这里不需要 lookbehind，也证明它不需要。门里配了对照把这件事钉住。
var bodiesSourceCallRE = regexp.MustCompile(`(?:dbpkg\.|db\.)?SessionBodiesSourceSQL\s*\(`)
var bodiesFamilyCallRE = regexp.MustCompile(`\bSessionFamilyBodiesSourceSQL\s*\(`)

// v1BodiesRelationRE 匹配 v1 bodies 腿的关系名（**不含** viewsWithV1Arm 那种闭合集合，
// 这里只需要「指名了 v1 bodies」这一个判据）。
var v1BodiesRelationRE = regexp.MustCompile(`\brequest_logs_bodies(?:_hot|_with_current_month)?\b`)

// bodiesReadPath 是 27 个 bodies 读方里某一条的取源路径。
type bodiesReadPath string

const (
	// bodiesViaSwitchOnly：只经切换层取源，别处不指名 v1 bodies。
	// ⇒ 翻 `storage.session_bodies_native_read` 就能改指，**但它现在没翻**。
	bodiesViaSwitchOnly bodiesReadPath = "switch_only"
	// bodiesLiteralOnly：源码里直接指名 v1 bodies 关系，**无开关可翻** ⇒ 必须改 SQL。
	bodiesLiteralOnly bodiesReadPath = "literal_only"
	// bodiesMixed：两者都有 —— 切换层只覆盖其中一条读法。
	bodiesMixed bodiesReadPath = "mixed"
)

// classifyBodiesReadPath 判定一个文件的取源路径。注释已在上游剥掉。
func classifyBodiesReadPath(stripped string) bodiesReadPath {
	callsSwitch := bodiesSourceCallRE.MatchString(stripped)
	namesV1 := v1BodiesRelationRE.MatchString(stripped)
	switch {
	case callsSwitch && namesV1:
		return bodiesMixed
	case callsSwitch:
		return bodiesViaSwitchOnly
	case namesV1:
		return bodiesLiteralOnly
	default:
		return ""
	}
}

// ⚠ 本文件**不再**自己断言「默认臂是 v1」——
// `admin/session_bodies_source_test.go` 的 `TestBodiesSwitchDefaultsToV1`
// 已经断言了那件事（`GetPlatformBool(…, false)` 字面量 + 端到端返回值 + spec 的 Default），
// 而且它的注释里带着一条我算不出来的事实：
// 默认开启会让生产 2026-09-30 的会话导出正文全变 `{}`（1,113 条 turn，§9.229.2 实测）。
//
// 我第一版在这里又写了一道同义的门。两道同义的门有一个坏形态：
// **改对的人只需要改一道，另一道会红**，于是后来的人会误以为判据有矛盾。
// ⇒ 删掉自己那道，改为**引用**它，并把「默认臂 = v1」当作已由别处保证的前提使用。
//
// 这里真正独有的是两件：
//  1. 三条取源路径的分类（TestV1BodiesReadPathsArePinned）
//  2. 「那个键在真库里的实际值」—— 代码门看不见它，
//     而它决定 13 个读方此刻读的是 v1 还是会话侧（TestBodiesNativeReadKeyInRealDB）

// TestBodiesReadPathClassifierSeparatesTheTwoHelpers 分类器的对照。
func TestBodiesReadPathClassifierSeparatesTheTwoHelpers(t *testing.T) {
	// 只调 SessionFamilyBodiesSourceSQL（会话侧，**不是**切换层）⇒ 不得判成 switch_only
	familyOnly := "func x() string { return SessionFamilyBodiesSourceSQL() }"
	if got := classifyBodiesReadPath(familyOnly); got != "" {
		t.Errorf("只调用 SessionFamilyBodiesSourceSQL 的片段被判成 %q，期望空 —— "+
			"`SessionBodiesSourceSQL` 是 `SessionFamilyBodiesSourceSQL` 的子串，"+
			"裸 Contains 会把两者混成一处", got)
	}
	// 真切换层 ⇒ switch_only
	if got := classifyBodiesReadPath("LEFT JOIN " + bodiesSourceCallRE.FindString("dbpkg.SessionBodiesSourceSQL()")); got != bodiesViaSwitchOnly {
		t.Errorf("真切换层被判成 %q，期望 %q", got, bodiesViaSwitchOnly)
	}
	// 字面量 ⇒ literal_only
	if got := classifyBodiesReadPath("FROM request_logs_bodies_with_current_month rb"); got != bodiesLiteralOnly {
		t.Errorf("字面量读被判成 %q，期望 %q", got, bodiesLiteralOnly)
	}
	// 两者都有 ⇒ mixed
	if got := classifyBodiesReadPath("FROM request_logs_bodies_hot rb " +
		bodiesSourceCallRE.FindString("dbpkg.SessionBodiesSourceSQL()")); got != bodiesMixed {
		t.Errorf("混合形态被判成 %q，期望 %q", got, bodiesMixed)
	}
}

// TestV1BodiesReadPathsArePinned 逐个点名三条路径的成员，并要求它们**互斥且穷尽**
// 27 个 bodies 读方。
//
// 名单是**从源码树重算**后与声明双向比对的，不只查「列表里每个都还成立」：
// 单向检查在清单变小时照样绿，而「清单变小」正是少了一个有风险读方的方向。
func TestV1BodiesReadPathsArePinned(t *testing.T) {
	root := repoRootFromCaller(t)
	measured := scanV1BodiesReaders(t, root)
	if len(measured) == 0 {
		t.Fatalf("bodies 读方总体为 0 —— 扫描器坏了，不是「bodies 没人读」")
	}

	wantSwitchOnly := []string{
		"admin/auto_title_generator.go",
		"admin/compression_stats.go",
		"admin/logs_summary.go",
		"admin/memora_handlers.go",
		"admin/no_topic_session.go",
		"admin/session_compare.go",
		"admin/session_export.go",
		"admin/session_sanitize_matches.go",
		// ★ 它的 `request_logs_bodies_with_current_month` 只出现在
		//   `LEFT JOIN `+dbpkg.SessionBodiesSourceSQL()+` rb /* … */`
		//   的 **SQL 块注释**里 —— 有人在那里标注了这个调用的默认臂。
		//   剥掉 SQL 注释后该文件不指名任何 v1 bodies 关系 ⇒ 属 switch_only。
		"admin/session_title.go",
		"bg/passive_probe_listener.go",
		"domains/sessionforensics/export.go",
		"domains/sessionsummary/summarizer.go",
		"domains/sessionsummary/system_prompt_prefix.go",
	}
	wantLiteralOnly := []string{
		"admin/body_resolver.go",
		"admin/compression_sessions.go",
		"admin/data_lifecycle.go",
		"admin/data_lifecycle_blobs.go",
		"admin/logs.go",
		"admin/quality_correlations.go",
		"admin/session_bodies_batch.go",
		"admin/unified_detail.go",
		"cmd/compression-bench/main.go",
		"cmd/scenario_driver/main.go",
		"cmd/tools/backfill_session_bodies/main.go",
		"cmd/tools/validate_sessions_v2/loader.go",
		"domains/hooks/goal/history_store.go",
	}
	wantMixed := []string{
		// 切换层自己：`return "request_logs_bodies_with_current_month"` 是**活代码**，
		// 而 `SessionFamilyBodiesSourceSQL()` 也在同一文件 ⇒ 它同时持有两臂。
		"db/request_logs_view_schema.go",
	}

	got := map[bodiesReadPath]map[string]bool{}
	for _, k := range []bodiesReadPath{bodiesViaSwitchOnly, bodiesLiteralOnly, bodiesMixed} {
		got[k] = map[string]bool{}
	}
	for f := range measured {
		b, err := readFileForEvidence(filepath.Join(root, f))
		if err != nil {
			t.Errorf("读不到 %s：%v", f, err)
			continue
		}
		p := classifyBodiesReadPath(stripGoAndSQLComments(b))
		if p == "" {
			t.Errorf("%s 被 bodies 门算进总体，但三条路径一条都不匹配 —— "+
				"总体与分类判据不同源，盯的就是这种缝", f)
			continue
		}
		got[p][f] = true
	}
	assertBodiesPath(t, got, bodiesViaSwitchOnly, wantSwitchOnly)
	assertBodiesPath(t, got, bodiesLiteralOnly, wantLiteralOnly)
	assertBodiesPath(t, got, bodiesMixed, wantMixed)

	// 算式自检：三条互斥且穷尽，总体必须对得上。
	sum := len(got[bodiesViaSwitchOnly]) + len(got[bodiesLiteralOnly]) + len(got[bodiesMixed])
	if sum != len(measured) {
		t.Errorf("三条路径合计 %d，与 bodies 总体 %d 不符", sum, len(measured))
	}
	t.Logf("27 个 bodies 读方按取源路径：只经切换层 %d（★ 开关未设 ⇒ 此刻读 v1）· "+
		"只字面量 %d（无开关可翻，必须改 SQL）· 混合 %d",
		len(got[bodiesViaSwitchOnly]), len(got[bodiesLiteralOnly]), len(got[bodiesMixed]))
}

// TestBodiesReadPathAgreesWithTheShapeRegistry 把本文件的**机械**划分与既有登记表
// `v1BodiesReaderShapes` **交叉核对**。
//
// # 为什么需要这道门：两张表答的不是同一个问题
//
// | | 判据 | 答什么 |
// |---|---|---|
// | `v1BodiesReaderShapes`（既有） | **人手写**的迁移形状 | 「这个读法能不能换 helper」 |
// | 本文件 | **从源码树机械重算**的取源路径 | 「它此刻实际从哪取数」 |
//
// ★ 既有表里 `shapeAlreadySwitched` 的理由**逐字写着**「默认臂 = v1 ⇒ 停写/退役后果不变」
// —— 也就是说「走切换层不等于读会话侧」这件事，**仓库早就登记了**，不是本轮的发现。
// 本轮独有的只有两件：① 那个键在**真库 `settings_kv` 里的实际值**（登记表只看代码，读不到它）；
// ② 互补的那一半 —— 13 个**没有**开关可翻、必须改 SQL 的字面量读方。
//
// ⇒ 那就不该把本文件当第二份真相源摆着，而要让它**与既有表对质**：
// 任何一边单独改而另一边没跟上，当场变红。
func TestBodiesReadPathAgreesWithTheShapeRegistry(t *testing.T) {
	root := repoRootFromCaller(t)

	// 既有登记表里标成 already-switched 的文件
	registered := map[string]bool{}
	for f, sh := range v1BodiesReaderShapes {
		if sh.Shape == shapeAlreadySwitched {
			registered[f] = true
		}
	}
	if len(registered) == 0 {
		t.Fatalf("既有登记表里没有一条 shapeAlreadySwitched —— 分类常量或登记表变了，" +
			"本门与它的交叉核对失去对象")
	}

	// 机械重算：真调用切换层且剥注释后不指名 v1 bodies 的文件
	var mechanical []string
	for f := range scanV1BodiesReaders(t, root) {
		b, err := readFileForEvidence(filepath.Join(root, f))
		if err != nil {
			t.Errorf("读不到 %s：%v", f, err)
			continue
		}
		if classifyBodiesReadPath(stripGoAndSQLComments(b)) == bodiesViaSwitchOnly {
			mechanical = append(mechanical, f)
		}
	}

	// ① 机械算出的「只经切换层」必须**全都是**既有表登记的 already-switched。
	//    少一个 = 有人接了切换层却没登记退役风险（与 §9.232 那个 4 文件静默消失同型）。
	var unregistered []string
	for _, f := range mechanical {
		if !registered[f] {
			unregistered = append(unregistered, f)
		}
	}
	sort.Strings(unregistered)
	if len(unregistered) > 0 {
		t.Errorf("以下 %d 个文件**实际**只经切换层取 bodies，却不在 v1BodiesReaderShapes 的 "+
			"shapeAlreadySwitched 里 —— 它们的停写/退役后果没有登记：%s",
			len(unregistered), strings.Join(unregistered, " "))
	}

	// ② 反向：既有表说已切、而机械重算说「切换层自己」（mixed）的那一个，
	//    只能是切换层的定义处。登记表把它和消费点放同一档，本门把它单列 ——
	//    这不是分歧，是**切缝不同**，但切缝必须钉住，否则两边会各说各话。
	onlyMixedInRegistry := []string{}
	for f := range registered {
		b, err := readFileForEvidence(filepath.Join(root, f))
		if err != nil {
			continue
		}
		if classifyBodiesReadPath(stripGoAndSQLComments(b)) == bodiesMixed {
			onlyMixedInRegistry = append(onlyMixedInRegistry, f)
		}
	}
	sort.Strings(onlyMixedInRegistry)
	wantMixedInRegistry := []string{"db/request_logs_view_schema.go"}
	if strings.Join(onlyMixedInRegistry, " ") != strings.Join(wantMixedInRegistry, " ") {
		t.Errorf("登记表标 already-switched、而机械重算为 mixed 的文件应是 %v（切缝差异），"+
			"实际 %v", wantMixedInRegistry, onlyMixedInRegistry)
	}
	t.Logf("交叉核对：机械算出「只经切换层」%d 个，全部已在既有登记表的 shapeAlreadySwitched 里；"+
		"另 %d 个（%s）登记表同档但机械判 mixed —— 那是切换层的定义处，切缝不同。",
		len(mechanical), len(onlyMixedInRegistry), strings.Join(onlyMixedInRegistry, " "))
}

func assertBodiesPath(t *testing.T, got map[bodiesReadPath]map[string]bool, p bodiesReadPath, want []string) {
	t.Helper()
	var missing, extra []string
	for _, f := range want {
		if !got[p][f] {
			missing = append(missing, f)
		}
	}
	wantSet := map[string]bool{}
	for _, f := range want {
		wantSet[f] = true
	}
	for f := range got[p] {
		if !wantSet[f] {
			extra = append(extra, f)
		}
	}
	sort.Strings(missing)
	sort.Strings(extra)
	if len(missing) > 0 {
		t.Errorf("路径 %s 缺 %d 个成员：%s", p, len(missing), strings.Join(missing, " "))
	}
	if len(extra) > 0 {
		t.Errorf("路径 %s 多出 %d 个成员（改判要同时改门）：%s", p, len(extra), strings.Join(extra, " "))
	}
}

var _ = os.Getenv

// ★ bodiesSwitchPair —— P1 的两个开关（审计 §9.265 七节，生产 252 只读实测补出）
//
// 为什么不把 P1 写成「把 `storage.session_bodies_native_read` 翻成 true」这一句：
// 读端切换层（db/request_logs_view_schema.go:1082）翻过去之后，读到的是**会话侧**；
// 而**会话侧的正文由 writer 侧另一个开关决定**（domains/session/v2/session_writer_v2.go:674
// 「S1b 灰度开关①」，控制 RequestDeltaJSON / ResponseDeltaJSON 是否写进 session_turns）。
// 生产 252 上**两个键都不存在** ⇒ 都取 false ⇒ 读端若单翻，会读到**空正文**。
// 这就是 §9.229.2 那个「会话导出正文全变 `{}` 而接口仍 200」的形态。
//
// 所以 P1 必须是**一对**，而这一对要在这里逐个点名 —— 少写一个，
// 「只翻读端」这个方向就会重新变成一个看起来成立的选项。
// 两个键单列出来，P1 清单与下面的源码断言共用同一份定义，避免两处各写一遍字符串。
const (
	bodiesReadKey  = "storage.session_bodies_native_read"
	bodiesWriteKey = "storage.session_turns_bodies_enabled"
)

var bodiesSwitchPair = []struct {
	Side string // 读端 / 写端
	Key  string
	Why  string
}{
	{
		Side: "读端",
		Key:  bodiesReadKey,
		Why:  "SessionBodiesSourceSQL 的默认支；false ⇒ 13 个只经切换层的 bodies 读方此刻读 v1",
	},
	{
		Side: "写端",
		Key:  bodiesWriteKey,
		Why: "S1b 灰度开关①；false ⇒ RequestDeltaJSON/ResponseDeltaJSON 不写进 session_turns，" +
			"读端切过去读到的是空正文",
	},
}

// TestBodiesSwitchPairIsPinnedAndPaired 把 P1 的两个开关钉在同一个清单里。
//
// 三个方向各要红一次：删掉一个成员、把两边的 Side 写成同一个、
// 把某个 Key 换成表里真实存在的另一个键（那种改动读起来「很有道理」——
// 例如误以为 `storage.admin_logs_native_turns_read` 就是 bodies 写端开关）。
func TestBodiesSwitchPairIsPinnedAndPaired(t *testing.T) {
	if len(bodiesSwitchPair) != 2 {
		t.Fatalf("P1 必须是一对开关，实为 %d 个：%+v", len(bodiesSwitchPair), bodiesSwitchPair)
	}
	want := map[string]string{
		"读端": "storage.session_bodies_native_read",
		"写端": "storage.session_turns_bodies_enabled",
	}
	seen := map[string]bool{}
	for _, p := range bodiesSwitchPair {
		if seen[p.Side] {
			t.Errorf("P1 里 %q 这一侧出现了两次 —— 清单必须是「读端 + 写端」各一个", p.Side)
		}
		seen[p.Side] = true
		if got, ok := want[p.Side]; !ok {
			t.Errorf("P1 出现未知的一侧 %q（只允许 读端 / 写端）", p.Side)
		} else if p.Key != got {
			t.Errorf("P1 的%s键是 %q，期望 %q", p.Side, p.Key, got)
		}
		if strings.TrimSpace(p.Why) == "" {
			t.Errorf("P1 的%s（%s）没有写为什么 —— 空理由的条目会被后来人当 filler 删掉", p.Side, p.Key)
		}
	}
	for side := range want {
		if !seen[side] {
			t.Errorf("P1 缺了%s —— **只翻一半就是这个门要拦的形态**", side)
		}
	}

	// 两个键必须**真的**在各自那一侧的源码里被读到，且都带 false 兜底。
	// 兜底从 false 改成 true 是危险方向：会让「键不存在」等价于「已切会话侧」。
	//
	// ⚠ 这里刻意**允许两种调用形态**（字面量 / 常量名），因为两侧本来就不一样：
	// 读端用常量 `SessionBodiesNativeReadSetting`（db/request_logs_view_schema.go:1094），
	// 写端直接写字面量。我第一版只写了「键名字面量」，两侧同时判红 ——
	// 那是**我的正则错了**，不是产品代码错了。
	// 现在改成「键必须以字面量或该侧指定的常量名出现」+「兜底必须是 false」两段独立断言。
	for _, chk := range []struct{ rel, key, constName, anchor string }{
		{
			rel: "db/request_logs_view_schema.go", key: bodiesReadKey,
			constName: "SessionBodiesNativeReadSetting", anchor: "SessionBodiesSourceSQL",
		},
		{
			rel: "domains/session/v2/session_writer_v2.go", key: bodiesWriteKey,
			anchor: "RequestDeltaJSON",
		},
	} {
		body, err := os.ReadFile(filepath.Join(repoRootFromCaller(t), chk.rel))
		if err != nil {
			t.Fatalf("读 %s: %v", chk.rel, err)
		}
		src := string(body)
		if !strings.Contains(src, chk.anchor) {
			t.Errorf("%s 里找不到 %q —— 该开关的落点变了，P1 清单需要重新取证", chk.rel, chk.anchor)
		}
		// ① 键必须以字面量出现，或（读端）以本侧常量名出现，且该常量绑定到这个键。
		byLiteral := strings.Contains(src, `"`+chk.key+`"`)
		byConst := chk.constName != "" &&
			strings.Contains(src, chk.constName) &&
			strings.Contains(src, `const `+chk.constName+` = "`+chk.key+`"`)
		if !byLiteral && !byConst {
			t.Errorf("%s 里既没有 %q 这个键的字面量%s —— P1 清单与源码对不上了",
				chk.rel, chk.key,
				map[bool]string{true: "，也没有本侧常量 " + chk.constName + " 绑定到它", false: ""}[byConst || chk.constName != ""])
		}
		// ② 兜底必须是 false。键名/常量名都用 `[^)]*` 放过，
		//    这样形态可变而「兜底被改成 true」这个危险变异仍会被抓住。
		call := regexp.MustCompile(`GetPlatformBool\([^)]*,\s*false\)`)
		if !call.MatchString(src) {
			t.Errorf("%s 里找不到任何 `GetPlatformBool(…, false)` 形式的读取 —— "+
				"若兜底被改成 true，「键不存在⇒取 false」这条前提就失效了，P1 必须重新取证", chk.rel)
		}
		if re := regexp.MustCompile(`GetPlatformBool\([^)]*,\s*true\)`); re.MatchString(src) {
			t.Logf("%s 里存在 `GetPlatformBool(…, true)` 形式的调用（可能是别的开关，"+
				"人工确认它不是本清单这一侧）", chk.rel)
		}
	}
}
