//go:build integration

package admin

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/kaixuan/llm-gateway-go/db"
)

// 审计报告缺陷 7（2026-10-01）：`queryRequestLogsFallback` 的 **turns 腿**
// 被 S4 读路径迁移批次（02c93d04e）从 v1 视图改接成 session 族原生源：
//
//	改前： FROM request_logs_with_current_month rl
//	改后： FROM ` + db.SessionFamilyTurnsForSessionSQL() + ` rl
//
// 而 `SessionFamilyTurnsForSessionSQL()` 展开正是
// `session_turns_hot UNION ALL session_turns` —— 与主路径
// `session_turns_with_current_month` 同源（该视图 = hot 去重 ∪ parent）。
//
// 后果不是"慢"，是**fallback 自毁**：这条路径存在的唯一理由，就是主路径在
// session_turns 里查不到轮次时才被调用；改接之后它去读同一批表，于是必然
// 同样返回 0 行，`generateSummary` 落到
// `return nil, fmt.Errorf("no turns found for session %s")` → HTTP 500。
//
// # 这道门第一版选错了人群，结论也跟着错了
//
// 第一版选的是「v1 视图里有轮次、session_turns 一行都没有」的会话，据此写下
// 「10.87% 的会话拿不到总结，返回 500」。**那个口径把 internal_loopback /
// non_terminal 也算成了 v1 的轮次** —— 而排除谓词正确地把它们丢掉之后，实测
// 这批会话**全部**是 100% 内部调用（抽样 `gs_gw_0a0c9d0b`：1,365 轮全是
// internal_loopback，即网关自己生成的标题/摘要 LLM 调用）。它们本来就不该被
// 当成用户对话去总结。
//
// 复核后的真实影响（近 3 天窗口，两种独立口径互相印证）：
//
//	v1 会话总数                                     15,640
//	其中有业务轮次（genuine_loss）的                 13,980
//	纯内部调用 / non_terminal 占位（不该总结）         1,660
//	有业务轮次但原生源完全没有的                          0   ← 缺陷 7 的实际暴露面
//
// 所以缺陷 7 是**潜在**缺陷：fallback 确实自毁，但今天没有用户可见损失。
// 这不改变「要修」——它在等一个还不存在的会话，而 2026-08-30 加这条路径的
// 原始理由正是那类会话。
//
// 因此门改成钉**不变量**：「fallback 对一个真有业务轮次的会话，必须返回与 v1
// 一致的轮次数」。
//
// # 但这道门抓不到缺陷 7 —— 正控实测过，结论必须写进注释
//
// 把 turns 腿退回缺陷 7 形态（原生源）后重跑这道门：**仍然绿**。原因是上面
// 那张表：今天没有任何一个有业务轮次的会话缺失于原生源，所以两个源对每一个
// 真实会话都给出相同结果，不变式自然成立。
//
// 也就是说：**缺陷 7 在当前数据下不可被任何依赖数据的门发现** —— 这正是它
// 在全绿状态下溜过去的原因。真正守住它的是那道**静态**门
// `TestSessionSummaryV2FallbackTurnsLegReadsV1`（无需 TEST_PG_URL，钉住
// FROM 的字面来源与谓词方向），以及本文件里 §既有等价性门 legacy 查询与
// 生产同源、探针又取自原生源的双重盲区。
//
// 本门保留的价值是**前瞻**的：一旦原生源开始漏镜像（或排除谓词口径变化），
// 让两个源不再对每个真实会话一致时，它会第一个报红。
//
// Run with a real database:
//
//	TEST_PG_URL='postgres://llm_gateway:...@127.0.0.1:5432/llm_gateway' \
//	  go test -tags=integration ./admin/ -run TestSessionSummaryV2FallbackServesRealV1Turns
func TestSessionSummaryV2FallbackServesRealV1Turns(t *testing.T) {
	dsn := os.Getenv("TEST_PG_URL")
	if dsn == "" {
		t.Skip("TEST_PG_URL unset — skipping does NOT constitute evidence that the fallback reads v1")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer pool.Close()

	// 选样：v1 视图里有**业务轮次**（genuine_loss）的会话。
	//
	// 谓词方向是关键：`= 'genuine_loss'` 是**保留**业务轮次。写反成
	// `<> 'genuine_loss'` 会把业务轮次全丢掉、只留下内部调用，于是这道门
	// 会对一个「正确地什么都不返回」的端点报红。
	probes := queryStrings(t, ctx, pool, `
		SELECT rl.gw_session_id || '|' || rl.tenant_id
		  FROM request_logs_with_current_month rl
		 WHERE rl.gw_session_id IS NOT NULL
		   AND rl.ts >= now() - `+probeWindow+`
		   AND (`+db.MirrorDriftClassSQL+`) = 'genuine_loss'
		 GROUP BY rl.gw_session_id, rl.tenant_id
		HAVING count(*) BETWEEN 2 AND 20
		 ORDER BY count(*) DESC
		 LIMIT 5`)
	if len(probes) == 0 {
		t.Skip("no v1 session with business turns in the " + probeWindow +
			" window — this run proves nothing either way")
	}

	api := NewSessionSummaryV2API(pool)
	served, turnsSeen := 0, 0
	var detail []string
	for _, raw := range probes {
		id, tenant, ok := strings.Cut(raw, "|")
		if !ok {
			t.Fatalf("unexpected probe row %q", raw)
		}
		// v1 侧的业务轮次数作为「本该服务多少」的基线，用同一个谓词。
		var v1Turns int
		if err := pool.QueryRow(ctx, `
			SELECT count(*)::int
			  FROM request_logs_with_current_month rl
			 WHERE rl.gw_session_id = $1
			   AND rl.tenant_id = $2
			   AND (`+db.MirrorDriftClassSQL+`) = 'genuine_loss'`, id, tenant).Scan(&v1Turns); err != nil {
			t.Fatalf("count v1 business turns for %s: %v", id, err)
		}

		turns, err := api.queryRequestLogsFallback(ctx, id, tenant, nil)
		if err != nil {
			t.Fatalf("fallback for %s: %v", id, err)
		}
		turnsSeen += v1Turns
		if len(turns) == v1Turns {
			served++
			continue
		}
		detail = append(detail, fmt.Sprintf("%s: v1 has %d business turns, fallback returned %d",
			id, v1Turns, len(turns)))
	}

	if served < len(probes) {
		t.Fatalf("the fallback served %d/%d v1 sessions with business turns:\n  %s\n\n"+
			"两个源对同一个会话给出了不同的轮次数。这不是缺陷 7 的形状（那个形态下\n"+
			"两个源今天仍然一致），而是 turns 腿的来源与排除谓词口径发生了漂移：\n"+
			"查 buildRequestLogsFallbackQuery 的 FROM 与谓词方向\n"+
			"（静态门 TestSessionSummaryV2FallbackTurnsLegReadsV1 钉的就是这两处）。",
			served, len(probes), strings.Join(detail, "\n  "))
	}
	t.Logf("fallback served %d/%d v1 sessions with business turns (%d turns in scope)",
		served, len(probes), turnsSeen)
}
