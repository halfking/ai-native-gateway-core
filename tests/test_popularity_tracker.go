//go:build ignore
// +build ignore

package main

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/kaixuan/llm-gateway-go/domains/credentialstate"
)

// ★ 2026-10-20：顺手修掉一处**既有编译错**（本文件是 //go:build ignore，
// 从来没被编译过，所以没人发现）：`"─"*70` 不是合法的 Go（字符串不能乘），
// 正确写法是 strings.Repeat("─", 70)。不修的话这个脚本**根本跑不起来**，
// 而「改指会话族」的目的正是让它还能跑。
func main() {
	// 从环境变量读取数据库连接
	dbURL := "postgres://llm_gateway:password@localhost:5432/llm_gateway?sslmode=disable"

	fmt.Println("=== Phase 2 热度追踪器功能测试 ===")
	fmt.Println()

	// 连接数据库
	fmt.Println("1. 连接数据库...")
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		log.Fatalf("连接失败: %v\n提示: 修改 dbURL 为实际数据库地址", err)
	}
	defer pool.Close()

	if err := pool.Ping(ctx); err != nil {
		log.Fatalf("Ping 失败: %v", err)
	}
	fmt.Println("   ✓ 数据库连接成功")
	fmt.Println()

	// ★ 2026-10-20（迁移 890，v1 族整体退役）：本脚本改查**会话族**
	// （session_turns_hot ∪ session_turns）。
	//
	// 口径对应关系（v1 → 会话族）：
	//   表          _request_logs        → session_turns_hot ∪ session_turns
	//   时间列      created_at            → ts（v1 侧其实是 ts；created_at 早就不存在）
	//   模型列      client_model          → model（会话族**没有** client_model：
	//                                          canonical 投影里它恒 NULL）
	//   两个存储面  母表 + hot             → 母表 + hot（两侧都要读：
	//                                          只读母表会漏掉最新的那一段）
	//
	// ⚠ 语义差别（写明）：v1 的 client_model 是**客户端请求的模型**，
	// 会话族的 model 是**实际选中的模型**（outbound_model）。热度追踪器
	// （domains/credentialstate）也在迁移中，所以它的口径以它为准。

	// 检查会话族表
	fmt.Println("2. 检查 session_turns 表结构...")
	var exists bool
	err = pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT FROM information_schema.tables
			WHERE table_name = 'session_turns'
		)
	`).Scan(&exists)
	if err != nil || !exists {
		log.Fatalf("session_turns 表不存在: %v", err)
	}
	fmt.Println("   ✓ session_turns 表存在")

	// 检查索引（口径对应 v1 侧的 created_at 索引：会话族的时间列是 ts）
	var indexCount int
	err = pool.QueryRow(ctx, `
		SELECT COUNT(*)
		FROM pg_indexes
		WHERE tablename IN ('session_turns', 'session_turns_hot')
		  AND indexdef ILIKE '%ts%'
		  AND indexdef ILIKE '%model%'
	`).Scan(&indexCount)
	if err != nil {
		log.Printf("   ⚠ 索引检查失败: %v", err)
	} else if indexCount == 0 {
		fmt.Println("   ⚠ 警告: 未找到 (ts, model) 相关索引，查询可能较慢")
		fmt.Println("   建议: CREATE INDEX idx_session_turns_ts_model ON session_turns (ts DESC, model);")
	} else {
		fmt.Printf("   ✓ 找到 %d 个 (ts, model) 相关索引\n", indexCount)
	}
	fmt.Println()

	// 检查数据量
	fmt.Println("3. 检查最近1小时数据...")
	var totalRows, distinctModels, withModel int
	err = pool.QueryRow(ctx, `
		SELECT
		  COUNT(*) as total_rows,
		  COUNT(DISTINCT t.model) as distinct_models,
		  COUNT(*) FILTER (WHERE t.model IS NOT NULL) as with_model
		FROM (
		  SELECT model, ts FROM session_turns_hot
		  UNION ALL
		  SELECT model, ts FROM session_turns
		) t
		WHERE t.ts > NOW() - INTERVAL '1 hour'
	`).Scan(&totalRows, &distinctModels, &withModel)
	if err != nil {
		log.Fatalf("查询失败: %v", err)
	}
	fmt.Printf("   总请求数: %d\n", totalRows)
	fmt.Printf("   不同模型数: %d\n", distinctModels)
	fmt.Printf("   有模型标识的: %d (%.1f%%)\n", withModel, float64(withModel)/float64(totalRows)*100)
	fmt.Println()

	// 创建 tracker
	fmt.Println("4. 创建 ModelPopularityTracker...")
	tracker := credentialstate.NewModelPopularityTracker(pool)
	fmt.Println("   ✓ Tracker 创建成功")
	fmt.Println()

	// 手动执行一次刷新
	fmt.Println("5. 执行热度统计查询...")
	start := time.Now()
	err = pool.QueryRow(ctx, `SELECT 1`).Scan(new(int)) // warm up

	start = time.Now()
	rows, err := pool.Query(ctx, `
		SELECT t.model, COUNT(*) AS request_count
		FROM (
		  SELECT model, ts FROM session_turns_hot
		  UNION ALL
		  SELECT model, ts FROM session_turns
		) t
		WHERE t.ts > NOW() - INTERVAL '1 hour'
		  AND t.model IS NOT NULL
		  AND t.model != ''
		GROUP BY t.model
		ORDER BY request_count DESC
		LIMIT 100
	`)
	if err != nil {
		log.Fatalf("查询失败: %v", err)
	}

	type modelStat struct {
		model string
		count int
	}
	var stats []modelStat

	for rows.Next() {
		var ms modelStat
		if err := rows.Scan(&ms.model, &ms.count); err != nil {
			log.Printf("   ⚠ 扫描行失败: %v", err)
			continue
		}
		stats = append(stats, ms)
	}
	rows.Close()
	duration := time.Since(start)

	fmt.Printf("   ✓ 查询完成，耗时: %v\n", duration)
	fmt.Printf("   ✓ 找到 %d 个活跃模型\n", len(stats))
	fmt.Println()

	// 显示 TOP 10
	fmt.Println("6. TOP 10 热门模型：")
	fmt.Println("   模型名称                                   请求数    推荐间隔")
	fmt.Println("   " + strings.Repeat("─", 70))
	for i, ms := range stats {
		if i >= 10 {
			break
		}
		interval := tracker.GetProbeInterval(ms.model)
		tier := "未知"
		switch {
		case ms.count >= 100:
			tier = "🔥热门"
		case ms.count >= 10:
			tier = "🌡️温热"
		default:
			tier = "❄️冷门"
		}
		fmt.Printf("   %-40s  %6d    %8v  %s\n", ms.model, ms.count, interval, tier)
	}
	fmt.Println()

	// 性能评估
	fmt.Println("7. 性能评估：")
	if duration > 1*time.Second {
		fmt.Printf("   ⚠ 查询耗时 %v 超过1秒，建议添加索引\n", duration)
	} else if duration > 500*time.Millisecond {
		fmt.Printf("   ⚠ 查询耗时 %v 略高，建议优化\n", duration)
	} else {
		fmt.Printf("   ✓ 查询耗时 %v，性能良好\n", duration)
	}

	if totalRows > 10000 {
		fmt.Printf("   ℹ 1小时数据量: %d 行，属于中高负载\n", totalRows)
	}

	fmt.Println()
	fmt.Println("=== 测试完成 ===")
	fmt.Println()
	fmt.Println("部署建议：")
	fmt.Println("1. 确保索引存在：CREATE INDEX idx_session_turns_ts_model ON session_turns (ts DESC, model);")
	fmt.Println("2. 启用热度追踪：export LLM_GATEWAY_ENABLE_POPULARITY_TRACKING=true")
	fmt.Println("3. 监控查询耗时：观察日志中的 'popularity tracker: refreshed' 消息")
}
