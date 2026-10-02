package db

import (
	"sort"
	"strings"
)

// 会话分支的 NULL 补位列逐列裁决（2026-10-02，审计 §9.22 决策 2）。
//
// 背景：710 用 34 个 NULL 补位把 session 臂的缺源列对齐到 v1 的冻结列契约。
// 「补位」本身是契约内的合法状态，但它对读方是**静默**的：读方从 session 分臂
// 拿到 NULL、接口照样 200。所以每一列都必须有一个**具名裁决**——要么补源、要么
// 改读法、要么明确随 v1 退役——而不是默认停在补位上。
//
// 本文件是那份裁决的 SSOT，由 db/request_logs_view_padded_columns_test.go 强制：
//   - 会话投影里每一条 NULL 补位列都必须在 paddedColumnVerdicts 里有条目
//     （新增补位列而没裁决 ⇒ 门红）；
//   - rejectedProjections 的每一条都**不得**出现在视图契约里
//     （哪天有人把它投影了，裁决就过期了 ⇒ 门红，逼人删条目并改理由）。
//
// 判据是「session 侧的列与 v1 侧的是不是同一个东西」，不是「session 侧有没有
// 这个列名」。本文件里有两条就是靠这条判据被否掉的（id / client_ip），两条都
// 在 session_turns 上有同名列，都通过了「名字对得上」这一关。

// paddedColumnVerdict 是裁决的闭集。新增取值必须同时在这里和下面的证据里
// 说明它与既有取值的区别——闭集的意义就是「不能随手发明一个理由」。
type paddedColumnVerdict string

const (
	// verdictSameThingNoSource：session 侧有语义相同的列，只是当前没被投影。
	// 读方需要它 ⇒ 应走 815 式的投影补齐。
	verdictSameThingNoSource paddedColumnVerdict = "same-thing-not-projected"
	// verdictDifferentThing：session 侧**有**同名列，但不是同一个东西。投影它
	// 等于给读方一个语义已变的同名列——比 NULL 更坏，因为 NULL 至少会让人
	// 看见缺失。同 `id` / `client_ip` 两条。
	verdictDifferentThing paddedColumnVerdict = "same-name-different-thing"
	// verdictNoSessionSource：session 族整条链上都没有对应概念（不是"缺列"，
	// 是"没有这个事实"）。读方若需要，必须去别的族取。
	verdictNoSessionSource paddedColumnVerdict = "no-session-source"
	// verdictRetireWithV1：全仓无读方、也无写方（或写方只在 v1 且无读方），
	// 随 v1 退役即可，不需要迁移。
	verdictRetireWithV1 paddedColumnVerdict = "retire-with-v1"
)

// RequestLogsViewPaddedSessionColumns 返回 canonical 视图 session 臂上
// **仍为 NULL 补位**的列名（现网 6 列：id / test_col / test_tab_indent /
// provider_model / credits_rate_multiplier / client_ip）。
//
// 导出它是因为「哪些列在 session 臂是空的」这条事实被多处消费，而各处如果
// 各自维护一份清单，就会各自过期——本轮就撞上了：admin 侧那张 30 列的表钉在
// migration 710 的 $proj$ 块上（734 之前），而 734 已经把其中 24 列换成了
// session_turn_details 特征层的真实值（真库 details 覆盖 99.9996%：
// hot 1,344/1,344 无缺失，parent 1,683,104/1,683,098）。
//
// 取的是**生效投影**（withDetails=true），不是 projectionExprsV2——后者是 710
// 的无 details 形态，按它统计会把 24 列误报成「恒 NULL」，而实际后果是族分类器
// 把正确判定判成「谓词级空」并拒绝它。
func RequestLogsViewPaddedSessionColumns() []string {
	return paddedSessionColumns()
}

// SessionFamilyDetailsProjectionColumns 返回 734 用 session_turn_details 特征层
// 顶掉 NULL 占位的那批列名（30 列）。它们在 session 分臂**行级有值**，只在
// details 缺行时为 NULL——与 RequestLogsViewPaddedSessionColumns 的「恒 NULL」
// 是两类风险，调用方需要分开记账。
func SessionFamilyDetailsProjectionColumns() []string {
	out := make([]string, 0, len(detailsProjectionColumns))
	for c := range detailsProjectionColumns {
		out = append(out, c)
	}
	sort.Strings(out)
	return out
}

// paddedSessionColumns 从生效投影里解析出全部 NULL 补位列名（实现见同包测试
// 文件里的同名说明——判据必须与被测对象同源：生效投影 = 734/815 组装的那份）。
func paddedSessionColumns() []string {
	effective := buildSessionProjectionExprs(canonicalColumnOrderV2, true)
	var out []string
	for i, expr := range effective {
		if !strings.HasPrefix(strings.TrimSpace(expr), "NULL::") {
			continue
		}
		if i >= len(canonicalColumnOrderV2) {
			return out
		}
		out = append(out, canonicalColumnOrderV2[i])
	}
	return out
}

// paddedColumn 是单条裁决。evidence 必填——它不是注释，是这条裁决可被复查的
// 依据（实测口径 + 数字）。空证据的裁决等于没有裁决：三个月后没人能判断它是
// 过期了还是仍然成立。
type paddedColumn struct {
	verdict  paddedColumnVerdict
	evidence string
}

// paddedColumnVerdicts 覆盖会话投影里**全部** NULL 补位列。
//
// 列集合 = **生效投影**（734 details-joined / 815 三列）中形如 `NULL::<type>`
// 的条目对应列名，即 paddedSessionColumns() 的输出。
// 真值以生效投影为准，本表由 TestEveryPaddedSessionColumnHasAVerdict 强制对齐。
var paddedColumnVerdicts = map[string]paddedColumn{
	"id": {
		verdict: verdictDifferentThing,
		evidence: "v1 的 request_logs.id 是**请求行 id**，session_turns.id 是 **turn id**。" +
			"真库按 request_id 配对约 1.516M 行（2026-10-02 14:0x 快照 1,515,984；" +
			"活库持续增长，同一查询稍后为 1,515,997，**结论一致：命中 0 次**），`r.id = t.id`；" +
			"两个值域虽有重叠（v1 34,616–2,513,875 / turn 400,060–2,091,912）但从不逐行相等。" +
			"判据是「同一个东西」而非「session 侧有这个列名」，故保持 NULL 补位。",
	},
	"test_col": {
		verdict: verdictRetireWithV1,
		evidence: "全仓无任何 SQL 读它（唯一的命中是 deploy/sql 基线 dump 里的视图列清单）。" +
			"v1 侧 2,162,951 行全部非空（DEFAULT '{}'），session 侧无该列。",
	},
	"test_tab_indent": {
		verdict: verdictRetireWithV1,
		evidence: "全仓无读方；v1 侧非空行数 **0**（2,162,951 行全 NULL），session 侧无该列。" +
			"两处都没有事实来源，属于建表期遗留的调试列。",
	},
	"provider_model": {
		verdict: verdictRetireWithV1,
		evidence: "v1 侧非空 **0**/2,162,951；session_turn_details.provider_model 存在但非空 " +
			"**0**/1,683,061 ⇒ 两侧都没有写方。与 provider_models 表的同名字段无关（本裁决只谈列）。",
	},
	"credits_rate_multiplier": {
		verdict: verdictNoSessionSource,
		evidence: "738 引入的 rollup 维度列，源是中层包装链上的倍率列。session_turns / " +
			"session_turn_details / sessions 上不存在任何倍率列（唯一含 rate 的列是 " +
			"session_turn_details.rate_limit_status，是状态不是倍率）⇒ 整个会话族没有" +
			"「这一行按什么倍率计价」这个事实，补不出有源的投影。",
	},
	"client_ip": {
		verdict: verdictSameThingNoSource,
		evidence: "**2026-10-02 在 252 生产库复测后改判**（审计 §9.42.6）。原裁决为 " +
			"verdictDifferentThing，理由是「session 侧 client_ip 与 client_forwarded_for " +
			"逐行相同 202,014/202,014 ⇒ 它是写在 client_ip 名下的转发头副本」。" +
			"**该理由已被证伪**：那条测量取自本机库，而本机全库 client_forwarded_for " +
			"只有 6 个 distinct 取值（全是回环与 Docker 网桥）、多跳链路 0 条——" +
			"在这种数据上「两列相同」无分辨力。252 生产库近 7 天实测：" +
			"链路形态 12,468 单跳 / 138 多跳 / 181 个 distinct 取值；" +
			"session_turns 侧 17,586 行有值，其中单跳 14,236 行 client_ip == " +
			"client_forwarded_for（100%），**多跳 3,350 行不等（0/3,350）**。" +
			"多跳样本 `client_ip=172.64.217.81` / `cff=2a06:98c0:3600::103, " +
			"172.64.217.81` ⇒ client_ip 是链路的**末跳（X-Real-IP 解析出的真实客户端）**，" +
			"不是首跳、也不是转发头副本。" +
			"**两族同义性直接配对验证**：同 request_id 配对 826 行，" +
			"session_turns.client_ip == host(request_logs.client_ip) **826/826、差异 0**。" +
			"**可投影性**：近 30 天 18,870 行全部匹配 IP 形态且 client_ip::inet " +
			"全部转换成功 ⇒ 投影进 inet 型视图列无类型风险。" +
			"⇒ 改判 verdictSameThingNoSource：session 侧有语义相同的列、只是尚未投影，" +
			"正解是走 815 式投影补齐。**底表的三个选项（改名 / 补真源 / 删列）全部不成立**——" +
			"这一列存的就是真源对端 IP，它是正确的。",
	},
}

// rejectedProjections 是「session 侧有同名列、但裁决为不投影」的清单。
//
// 与 paddedColumnVerdicts 分开是因为它们的失效模式不同：padded 是「忘了裁决」，
// rejected 是「裁决被推翻而没人改登记」。真值以 canonicalColumnOrderV2 为准。
var rejectedProjections = map[string]paddedColumn{
	"trace_events": {
		verdict: verdictSameThingNoSource,
		evidence: "A 类（session_turns 有该列）但**不投影**。真库 1d/7d/30 天三个窗口的" +
			"非空率**恒为 0**（镜像从不写它），而 v1 侧有 691,883 行带值、且这些行的 " +
			"request_id 已在 session_turns 里 ⇒ 会被视图的反连接丢弃。投影它等于把 " +
			"691,883 行真实值换成 NULL：读方从物理表能看到的比从视图看到的更多。" +
			"这是 §9.18「修好了但变全盲」的同一形状，触发条件是覆盖缺口而非语义分歧" +
			"（同批投影的 origin_stage/token_band/client_forwarded_for 在有值处与 v1 " +
			"逐值一致，both_differ = 0/1,515,960）。正解是先让镜像写该列再投影。",
	},
}
