package main

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/kaixuan/llm-gateway-go/internal/quality"
)

func main() {
	// 连接数据库
	db, err := sql.Open("pgx", "postgres://kxuser:kaixuan@localhost:15432/llm_gateway?sslmode=disable")
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	// 创建 ProfileUpdater
	updater := quality.NewProfileUpdater(db)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// 计算 3 个供应商的质量画像
	providers := []struct {
		id    int64
		model string
	}{
		{1, "claude-3-opus"},
		{2, "gpt-4"},
		{3, "test-model"},
	}

	for _, p := range providers {
		fmt.Printf("计算供应商 %d 模型 %s 的质量画像...\n", p.id, p.model)
		err := updater.UpdateOne(ctx, p.id, p.model)
		if err != nil {
			fmt.Printf("  ❌ 失败: %v\n", err)
		} else {
			fmt.Printf("  ✅ 成功\n")
		}
	}

	fmt.Println("\n质量画像计算完成！")
}
