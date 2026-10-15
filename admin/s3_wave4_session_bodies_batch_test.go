//go:build !integration

package admin

import (
	"path/filepath"
	"strings"
	"testing"
)

// S3 波 4（bodies 逐点评估第三片）：`admin/session_bodies_batch.go` 的批量
// 正文取数改双源。
//
// # 这道门守的是什么
//
// `querySessionBodiesByRequestID` 是**全仓最贵的单条查询**（文件头实测
// 171 个 request_id 要 17~19 秒，雪崩从这里来），也是会话导出 / 对比 /
// 摘要三条路径共用的正文供给点。停写后它的 v1 腿对新会话**恒 0 行**，
// 而调用方把 map 里的缺项当成「没有存正文」⇒
// **导出/对比/摘要会拿到"有轮次、无正文"的结构自洽结果**。
//
// 真库实测（本地，2026-10-14，停写后 100 个真实 request_id）：
//
//	v1 腿命中	 0
//	会话族腿命中	100
//	并集		100
//
// # 三条判据各自钉的是什么
//
// 1. **两条腿都在场，且顺序是 v1 先、会话后**（覆盖方向即优先级）；
// 2. **谓词形态不变**：`IN (SELECT unnest($1::text[]))`，不是 `= ANY`。
//    这一条是本文件最贵的教训：`= ANY` 与 `IN (SELECT unnest)` 返回相同的行，
//    但在列存分区上前者让规划器放弃主键探针，代价差 8~9 倍（17.43s vs 2.20s）。
//    ⇒ 「加一条会话腿」这件事很容易顺手把谓词写成 `= ANY`，而那不会报错，
//    只会把 0.9ms 的新查询变成一次全分区列存扫描；
// 3. **会话腿带 jsonb-null 过滤**：会话族用 jsonb `null` 表示「这一列没有
//    正文」，不滤掉就会盖掉双生 request_id 在 v1 腿里的真实正文。

func TestSessionBodiesBatchServesBothFamilies(t *testing.T) {
	// 会话腿的 SQL 形状。
	sess := stripSQLComments(sessionBodiesByRequestIDSessionSQL)

	if !strings.Contains(sess, "session_bodies_hot") || !strings.Contains(sess, "public.session_bodies") {
		t.Errorf("会话腿没有走 SessionFamilyBodiesSourceSQL 的 hot ∪ parent 双腿形态：\n%s", sess)
	}
	for _, col := range []string{"request_body", "outbound_body", "response_body"} {
		if !strings.Contains(sess, "rb."+col+" IS NOT NULL") {
			t.Errorf("会话腿缺少 %s 的 jsonb-null 过滤 —— 会话族用 jsonb null 表示"+
				"「没有正文」，不滤掉就会盖掉双生 request_id 在 v1 腿里的真实正文：\n%s", col, sess)
		}
		if !strings.Contains(sess, "rb."+col+" <> 'null'::jsonb") {
			t.Errorf("会话腿缺少 %s 的 jsonb-null 排除：\n%s", col, sess)
		}
	}

	// ★ 谓词形态（承重）。`= ANY` 长得像半连接，返回行完全相同，
	// 但在列存分区上代价差 8~9 倍。任何改动这里的人都要先读文件头那张对照表。
	for _, leg := range []struct{ name, sql string }{
		{"v1 腿", sessionBodiesByRequestIDV1SQL},
		{"会话腿", sessionBodiesByRequestIDSessionSQL},
	} {
		if !strings.Contains(leg.sql, "IN (SELECT unnest($1::text[]))") {
			t.Errorf("%s 的谓词不是 IN (SELECT unnest(...)) 半连接形态：\n%s\n"+
				"= ANY($1::text[]) 返回相同的行，但让规划器放弃主键探针"+
				"（文件头实测 17.43s vs 2.20s）。", leg.name, leg.sql)
		}
		if strings.Contains(leg.sql, "= ANY(") {
			t.Errorf("%s 里出现了 = ANY( —— 正是文件头记的那个 8~9 倍陷阱：\n%s", leg.name, leg.sql)
		}
	}
}

// TestSessionBodiesBatchMergeOrderIsV1ThenSession 钉住合并顺序。
//
// 顺序即优先级：后跑的腿覆盖先跑的。会话族腿带内容过滤 ⇒
//   - 只有 v1 有（镜像启用前的历史）→ v1 条目保留；
//   - 只有会话族有（停写后的新流量）→ 会话族补上；
//   - 双生且两侧都有内容 → 会话族覆盖（停写后的权威源）。
//
// 反过来写（会话先、v1 后）会把 v1 的空正文盖掉会话族的真实正文，症状是
// "导出成功、每条消息正文为空"——与本族反复写下的那句同型。
func TestSessionBodiesBatchMergeOrderIsV1ThenSession(t *testing.T) {
	src, err := readFileForEvidence(filepath.Join(repoRootFromCaller(t), "admin", "session_bodies_batch.go"))
	if err != nil {
		t.Fatal(err)
	}
	body := src[strings.Index(src, "func querySessionBodiesByRequestID("):]
	body = body[:strings.Index(body, "\n}\n")]

	v1At := strings.Index(body, "sessionBodiesByRequestIDV1SQL")
	sessAt := strings.Index(body, "sessionBodiesByRequestIDSessionSQL")
	if v1At < 0 || sessAt < 0 {
		t.Fatalf("querySessionBodiesByRequestID 里找不到两条腿的调用（v1@%d 会话@%d）：\n%s", v1At, sessAt, body)
	}
	if v1At > sessAt {
		t.Errorf("会话族腿排在 v1 腿之前（v1@%d 会话@%d）—— v1 会覆盖会话族的真实正文：\n%s",
			v1At, sessAt, body)
	}
	// 两条腿必须都通过同一个扫描函数（复制粘贴的扫描函数是"只改一段"的温床）。
	if got := countOccurrences(body, "scanSessionBodies(ctx, q,"); got != 2 {
		t.Errorf("querySessionBodiesByRequestID 里只有 %d 处 scanSessionBodies 调用，期望 2：\n%s\n"+
			"两腿共用一个扫描函数才不会出现「只改一段」的分支。", got, body)
	}
}

// TestSessionBodiesTupleVariantStaysV1Only 是**登记不迁**的钉子。
//
// `sessionBodiesByRequestIDAndTSSQL` 配的是 `(request_id, ts)` 元组，而
// `ts` 是**正文写入时间**不是轮次时间——实测两侧 99.85%% 不等（差 8~16 秒），
// 所以这条查询长期近乎恒空。文件头明确记载：两种键对应两个端点各自的既有
// 行为，把其中一个悄悄改成另一个就是「在重构里夹带行为变更」。
//
// 本轮**不改它**（停写对它没有新影响：双写期它就已经 99.85% 落空）。
// 这条判据的作用是：防止后人看到"bodies 逐点评估"就顺手把它也迁走——
// 那正是文件头警告的那件事。
func TestSessionBodiesTupleVariantStaysV1Only(t *testing.T) {
	if strings.Contains(sessionBodiesByRequestIDAndTSSQL, "session_bodies") {
		t.Errorf("元组键那条被改指会话族了 —— 那是「在重构里夹带行为变更」：\n%s\n"+
			"它配 (request_id, ts)，而会话族 ts 是落库侧时刻（99.85%% 不等）。"+
			"要迁移请另立一轮并单独评估两个端点的行为变化。", sessionBodiesByRequestIDAndTSSQL)
	}
}
