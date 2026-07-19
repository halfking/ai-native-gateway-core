package main

import (
	"database/sql"
	"log"
	"net/http"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/kaixuan/llm-gateway-go/internal/handlers"
	"github.com/kaixuan/llm-gateway-go/internal/quality"
)

func main() {
	// 连接数据库（trust 模式，不需要密码）
	db, err := sql.Open("pgx", "postgresql://kxuser@127.0.0.1:15432/llm_gateway?sslmode=disable")
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	// 测试连接
	if err := db.Ping(); err != nil {
		log.Fatal("数据库连接失败:", err)
	}
	log.Println("✅ 数据库连接成功")

	// 创建 ProfileUpdater（不启动后台任务）
	profileUpdater := quality.NewProfileUpdater(db)

	// 创建 handler
	qualityHandler := handlers.NewQualityHandler(db, profileUpdater)

	// 注册路由
	mux := http.NewServeMux()
	mux.Handle("/api/providers/", qualityHandler)

	// 启动服务器
	addr := ":8888"
	log.Printf("🚀 测试服务器启动在 http://localhost%s\n", addr)
	log.Println("API 端点:")
	log.Println("  GET  /api/providers/:id/quality")
	log.Println("  GET  /api/providers/quality/ranking")
	log.Println("  POST /api/providers/:id/quality/recalculate")
	
	if err := http.ListenAndServe(addr, mux); err != nil {
		log.Fatal(err)
	}
}
