// +build ignore

package main

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/kaixuan/llm-gateway-go/domains/credentialstate"
)

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
	
	// 检查 request_logs 表
	fmt.Println("2. 检查 request_logs 表结构...")
	var exists bool
	err = pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT FROM information_schema.tables 
			WHERE table_name = 'request_logs'
		)
	`).Scan(&exists)
	if err != nil || !exists {
		log.Fatalf("request_logs 表不存在: %v", err)
	}
	fmt.Println("   ✓ request_logs 表存在")
	
	// 检查索引
	var indexCount int
	err = pool.QueryRow(ctx, `
		SELECT COUNT(*) 
		FROM pg_indexes 
		WHERE tablename = 'request_logs' 
		  AND indexdef ILIKE '%created_at%'
	`).Scan(&indexCount)
	if err != nil {
		log.Printf("   ⚠ 索引检查失败: %v", err)
	} else if indexCount == 0 {
		fmt.Println("   ⚠ 警告: 未找到 created_at 索引，查询可能较慢")
		fmt.Println("   建议: CREATE INDEX idx_request_logs_created_at_model ON request_logs (created_at DESC, client_model);")
	} else {
		fmt.Printf("   ✓ 找到 %d 个 created_at 相关索引\n", indexCount)
	}
	fmt.Println()
	
	// 检查数据量
	fmt.Println("3. 检查最近1小时数据...")
	var totalRows, distinctModels, withModel int
	err = pool.QueryRow(ctx, `
		SELECT 
		  COUNT(*) as total_rows,
		  COUNT(DISTINCT client_model) as distinct_models,
		  COUNT(*) FILTER (WHERE client_model IS NOT NULL) as with_model
		FROM request_logs 
		WHERE created_at > NOW() - INTERVAL '1 hour'
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
		SELECT client_model, COUNT(*) AS request_count
		FROM request_logs
		WHERE created_at > NOW() - INTERVAL '1 hour'
		  AND client_model IS NOT NULL
		  AND client_model != ''
		GROUP BY client_model
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
	fmt.Println("   " + "─"*70)
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
	fmt.Println("1. 确保索引存在：CREATE INDEX idx_request_logs_created_at_model ON request_logs (created_at DESC, client_model);")
	fmt.Println("2. 启用热度追踪：export LLM_GATEWAY_ENABLE_POPULARITY_TRACKING=true")
	fmt.Println("3. 监控查询耗时：观察日志中的 'popularity tracker: refreshed' 消息")
}
