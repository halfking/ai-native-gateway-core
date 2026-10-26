package bg

// auto_index_refresher_s3_wave3_test.go — S3 波 3（2026-10-13，停写切换
// 读侧第二分片）形状钉桩：rollupCredentialModelIndexSQL 的双源化。
//
// 停写后 _request_logs_hot 排空，autoroute 候选池的核心指标源若无会话腿，
// 每 5 分钟退化为 half-2 cold-start 基线（success=0.9/p95=1000/分数 50），
// 路由质量静默坍缩。本判据钉三件事：
//  1. half-1 带会话腿（session_turns_hot + 冻结点守卫 + alias 链 offers 匹配）；
//  2. half-2 的反连接带同源会话抑制腿——half-1/half-2 必须同迁，否则基线
//     经 ON CONFLICT 每 5 分钟覆盖真实指标；
//  3. ★ 2026-10-20（迁移 890，v1 族退役）：**v1 腿必须不在**——890 把 v1 族
//     连表带分区一起 DROP，同一条 FROM 里提到 v1 关系会让整条语句 42P01。
//     本条第 3 点原先是「v1 腿必须保留」（写门开 = 生产/回滚期它是唯一流量源），
//     890 之后该前提作废，方向随之反转。
//
// 判据纪律：断言驱动包级变量文本（它就是发给 PostgreSQL 的语句本体）。
// 真库语义验证（alias 链匹配率 96.5% vs v1 96.7%、整句 INSERT 可执行、
// 会话腿产出 offers 名形态行）见审计文档
// docs/audit/2026-10-13-s3-wave2-requestlogs-readsource-migration.md 波 3 节。

import (
	"os"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/db"
)

func TestRollupCredentialModelIndex_ServesSessionFamily(t *testing.T) {
	sql := rollupCredentialModelIndexSQL
	// half-1 流量腿与 half-2 抑制腿各一条（count==2：只查在场会被
	// 「删 half-2 腿留 half-1 腿」的变异骗过——那种状态下基线每 5 分钟
	// 覆盖真实指标，是最危险的静默回退）。
	if got := strings.Count(sql, "FROM session_turns_hot t"); got != 2 {
		t.Fatalf("both half-1 traffic leg and half-2 suppression leg must read session_turns_hot (found %d); got:\n%s", got, sql)
	}
	if got := strings.Count(sql, "t.ts > "+db.V1FreezeCutoffSQL); got != 2 {
		t.Fatalf("both the half-1 traffic leg and the half-2 suppression leg must be freeze-guarded (found %d); got:\n%s", got, sql)
	}
	// 会话腿的 offers 匹配必须是 alias 链（canonical 粒度）：真库实测
	// raw_model_name 精确等值只 54%（会话侧是剥前缀归一化名），alias 链
	// 恢复 96.5%。钉住链条形状，防回退成精确等值。
	if !strings.Contains(sql, "mo.canonical_id IN (SELECT ma.canonical_id FROM model_aliases ma") {
		t.Fatalf("session leg must match offers through the alias chain (canonical granularity); got:\n%s", sql)
	}
	// ★ 2026-10-20（迁移 890，v1 族退役）：**v1 腿整条删除**，本门从
	// 「v1 腿必须在」**反转**为「v1 腿必须不在」。
	//
	// 理由：890 把 v1 族（_request_logs*）连表带分区一起 DROP ⇒ 同一条 FROM
	// 里提到 v1 关系会让**整条语句** 42P01（不是「那条腿失败」）⇒ half-1 与
	// half-2 一起挂。停写后 v1 腿本就恒空（v1 hot 排空），删掉不损失供数。
	//
	// ⚠ 负向断言的正向对照就在上面两条：会话腿 ×2 + 冻结点守卫 ×2。
	// 「删 half-2 腿留 half-1 腿」那种变异仍会被上面两条抓住（计数会变成 1），
	// 而「把 v1 腿塞回来」被本条抓住 —— 两个方向都有门。
	if strings.Contains(sql, "FROM _request_logs_hot") {
		t.Fatalf("half-1/half-2 不得再引用退役的 v1 族（迁移 890 已 DROP，提及即整条语句 42P01）；got:\n%s", sql)
	}
	// half-2 抑制腿：同 alias 链反向（canonical → aliases → lower(t.model)）。
	if !strings.Contains(sql, "lower(t.model) IN (SELECT ma.raw_name FROM model_aliases ma") {
		t.Fatalf("half-2 must suppress baselines via the same alias chain; got:\n%s", sql)
	}
}

// TestRollupModelTaskIndex_SourceIsSessionFamily pins the **890 之后**的
// model_task rollup 读源契约。
//
// ★ 2026-10-20（迁移 890，v1 族退役）：原先的登记是「**不迁**」——auto 请求
// （sys:% 会话）按设计不镜像进 session_turns 家族（2026-10-13 实测：3 天窗
// v1 侧 3,420 条 is_auto_request 行、会话侧 **0** 条），所以这个读点没有会话族
// 数据源、留在 _request_logs_hot 上，并由注释登记下来，免得有人「把它迁完」
// 迁成一张静默空表。
//
// **那条登记的前提已经作废**：v1 族被 DROP 了 ⇒ 留在 v1 上不是「留在一张没
// 数据的表上」，而是**整条语句 42P01**。⇒ 本门从「留在 v1」**反转**为
// 「必须读会话族、不得再提 v1」。
//
// ⚠⚠ **但登记里的「事实」仍然成立，而且现在是活的风险**（生产语义问题，
// 父会话处置；本门只保证源不引用已 DROP 的关系）：
//
//	auto 请求不镜像进会话族 ⇒ 这个 rollup 在会话族上按 `is_auto_request =
//	TRUE` 过滤会**恒 0 行** ⇒ model_task_index 不再更新。
//
// ⇒ 所以下面那条「事实必须继续登记」的断言不是形式主义：它是「这张表恒空」
// 的唯一书面根据，删掉它就等于把恒 0 行伪装成正常。
func TestRollupModelTaskIndex_SourceIsSessionFamily(t *testing.T) {
	// 负向：不得再引用退役的 v1 族（提及即整条语句 42P01）。
	// 正向对照：下面那条会话族源断言。
	if strings.Contains(rollupModelTaskIndexSQL, "FROM _request_logs") {
		t.Fatalf("model_task rollup 不得再引用退役的 v1 族（迁移 890 已 DROP）；got:\n%s", rollupModelTaskIndexSQL)
	}
	// 正向：源必须是会话族热腿（窗口 5 分钟 ≪ hot 保留期 ⇒ 窗内的行必在热侧）。
	// ⚠ 这里断言的是**渲染后**的 SQL（rollupModelTaskIndexSQL 是 var，不是 const）
	// ⇒ 断的是「真的读会话族热腿」，不是「源码里提到了某个常量」。
	if !strings.Contains(rollupModelTaskIndexSQL, "public.session_turns_hot") {
		t.Fatalf("model_task rollup 必须读会话族热腿；got:\n%s", rollupModelTaskIndexSQL)
	}
	// 语义未变：仍然只统计 auto 请求（这条过滤谓词是上面那条恒 0 行的成因，
	// 删掉它就改变了 rollup 的口径 ⇒ 必须被钉住）。
	if !strings.Contains(rollupModelTaskIndexSQL, "rl.is_auto_request = TRUE") {
		t.Fatalf("model_task rollup 必须保留 is_auto_request 过滤（口径未变）；got:\n%s", rollupModelTaskIndexSQL)
	}
	src, err := os.ReadFile("auto_index_refresher.go")
	if err != nil {
		t.Fatalf("read auto_index_refresher.go: %v", err)
	}
	// 「auto 请求不镜像进 session_turns 家族」这条**事实**必须继续留在文件里。
	// ⚠ 与它同段的「不迁」**决定**已随 890 作废（源已经迁了）——本门只钉事实，
	// 不钉那个已作废的决定。
	if !strings.Contains(string(src), "不镜像进 session_turns 家族") {
		t.Fatal("auto 请求不镜像进 session_turns 家族这条事实必须继续登记在 auto_index_refresher.go 上 —— " +
			"它是「这个 rollup 在会话族上没有数据（恒 0 行）」的唯一书面根据")
	}
}
