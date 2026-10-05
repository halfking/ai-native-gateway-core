package apihub

// upsertAssetSQL 的心跳门控契约（2026-10-04，runbook §10.2）
//
// 背景：生产实测 3 个实例 × 60 秒全量 upsert × ON CONFLICT DO UPDATE
// 带 last_seen_at=now() ⇒ 每天 924.9 万次**无变化**行重写 ≈ 1.73 GB/天，
// 而表里只有 2141 行、66 MB（98.39% 已是空洞）。而 last_seen_at 的唯一用途
// 是 listStaleSQL 的「超过 6 小时未见」⇒ 60 秒精度对 6 小时判据过剩 360 倍。
//
// ★ 本文件只做**文本层**的门，钉住三件事：
//   1. WHERE 条件存在，且由「业务字段真变了」+「心跳已过期」两半组成；
//   2. 用的是 IS DISTINCT FROM 而不是 `<>`（owner/team/cost_center 可为 NULL，
//      `NULL <> 'x'` 求值为 NULL 而不是 true ⇒ 用 `<>` 会把变更吞掉）；
//   3. 心跳窗口常量是 5 分钟 —— 这是**判定精度**不是性能旋钮，不该被随手改。
//
// 语义正确性由真库行为门验（scripts/.mutate-asset-heartbeat.py，走 252 的
// TEMP TABLE，生产零残留）—— 文本门验不了「条件到底会不会触发」。

import (
	"strings"
	"testing"
	"time"
)

func TestUpsertSQLHasHeartbeatGuard(t *testing.T) {
	q := normalizeSpace(upsertAssetSQL)

	if !strings.Contains(q, "DO UPDATE SET") {
		t.Fatalf("upsertAssetSQL 形状变了：找不到 DO UPDATE SET")
	}

	// WHERE 必须落在 DO UPDATE 之后（不是 upsert 顶层的 WHERE）。
	whereIdx := strings.Index(q, "DO UPDATE SET")
	if !strings.Contains(q[whereIdx:], "WHERE ") {
		t.Fatalf("ON CONFLICT DO UPDATE 后面没有 WHERE 条件 ⇒ " +
			"每次同步都会重写整行（每天 924.9 万次、1.73 GB/天的病源）")
	}

	// 第一半：业务字段真的变了 ⇒ 必须立刻落库
	need := []string{
		"IS DISTINCT FROM",       // 值比较必须是 NULL 安全的
		"EXCLUDED.metadata",      // 必须覆盖全部业务字段中的最后一个
		"public.assets.metadata", // 必须与目标表逐字段配对
		"public.assets.health_state",
		"public.assets.version",
		"public.assets.name",
	}
	for _, n := range need {
		if !strings.Contains(q, n) {
			t.Fatalf("WHERE 的「业务字段真变了」这一半不完整，缺少 %q\n实际 SQL：\n%s", n, q)
		}
	}

	// 绝不能出现 <> ：NULL 参与比较会得到 NULL，整行条件被吞。
	if strings.Contains(q, "<>") {
		t.Fatalf("upsertAssetSQL 出现 `<>`：owner/team/cost_center 可为 NULL，" +
			"`NULL <> 'x'` 求值为 NULL 而不是 true ⇒ 本该落库的变更会被静默吞掉。必须用 IS DISTINCT FROM")
	}
}

func TestUpsertSQLHeartbeatWindowGuard(t *testing.T) {
	q := normalizeSpace(upsertAssetSQL)

	// 第二半：心跳已过期 ⇒ 必须刷新，否则 last_seen_at 永远停在旧值，
	// stale 判定会在 6h+窗口 处误判为「消失」。
	//
	// ★ R44 起这一半**必须**是 COALESCE 形态，不能是裸比较。理由：
	// assets.last_seen_at 在 baseline 01-schema.sql:5673 是可空、无 DEFAULT、
	// 无 NOT NULL 的裸声明。裸写 `last_seen_at < now() - …` 时，对任何
	// last_seen_at IS NULL 的行：这一半是 `NULL < x` = NULL（不是 true），
	// 而「业务字段真变了」那一半在稳态下是 false ⇒ 两半都不成立 ⇒
	// **该行永远不会被更新**，last_seen_at 永远停在 NULL；读方
	// COALESCE(last_seen_at, registered_at) 判 stale ⇒ 活着的资产被判
	// degraded（bg/asset_health_probe）。省掉无效重写省过头了。
	//
	// 门从「含有裸比较」升格为「含有 COALESCE 到 -infinity 的比较」：
	// 判据仍锚在 last_seen_at 与 now() 的比较上（意图未变），
	// 同时把 NULL 这条逃逸路径钉死。
	if !strings.Contains(q, "COALESCE(public.assets.last_seen_at, '-infinity'::timestamptz)") {
		t.Fatalf("「心跳已过期」这一半没有用 COALESCE(public.assets.last_seen_at, '-infinity'::timestamptz)\n"+
			"⇒ 加上 WHERE 之后 last_seen_at 不会前进；且对 last_seen_at IS NULL 的行\n"+
			"  裸比较求值为 NULL，该行**永远写不进去**，读方 COALESCE 回落 registered_at\n"+
			"  会把活着的资产判成 stale/degraded。实际 SQL：\n%s", q)
	}
	if !strings.Contains(q, "< now() - ($12 * interval '1 second')") {
		t.Fatalf("心跳过期这一半没有与刷新窗口比较 ⇒ 加了 WHERE 之后 last_seen_at 再也不会前进，"+
			"6 小时后全部资产会被误判为已消失。实际 SQL：\n%s", q)
	}
	if !strings.Contains(q, "interval '1 second'") {
		t.Fatalf("心跳窗口没有用 $12 * interval '1 second' 表达 ⇒ " +
			"窗口值可能又被硬编码回去，Go 侧的 assetHeartbeatRefreshInterval 会变成死代码")
	}
	if !strings.Contains(q, "$12") {
		t.Fatalf("心跳窗口参数 $12 消失了")
	}
}

// ★ 钉住窗口取值本身。它决定 stale 判定的最大误差（窗口 / 6h），
// 改它等于改判定精度，属于契约变更而不是性能调优。
func TestAssetHeartbeatRefreshIntervalIsFiveMinutes(t *testing.T) {
	if assetHeartbeatRefreshInterval != 5*time.Minute {
		t.Fatalf("心跳窗口 = %v，契约值是 5 分钟。\n"+
			"  改它会同时改变 stale 判定的精度（当前最大误差 5min / 阈值 6h = 1.4%%）。\n"+
			"  理由：listStaleSQL 的 staleThreshold 默认 6h（bg.NewAssetHealthProbe），\n"+
			"  而本窗口只该比该阈值小一个数量级以上。\n"+
			"  确需放宽，先改 staleThreshold 的契约，不要动这里。",
			assetHeartbeatRefreshInterval)
	}
}

// 门自己也要被审：normalizeSpace 若退化成恒等，缩进差异就能让本文件恒过。
func TestNormalizeSpaceActuallyNormalizes(t *testing.T) {
	in := "SELECT  a,\n   b  FROM t"
	got := normalizeSpace(in)
	if strings.Contains(got, "\n") {
		t.Fatalf("normalizeSpace 没有去掉换行：%q", got)
	}
	if strings.Contains(got, "  ") {
		t.Fatalf("normalizeSpace 没有合并连续空格：%q", got)
	}
	if normalizeSpace("") != "" {
		t.Fatalf("normalizeSpace 对空串的行为变了")
	}
}

func normalizeSpace(s string) string {
	return strings.Join(strings.Fields(s), " ")
}
