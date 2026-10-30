package provider

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// TestRealDBModalityFragmentVsGoOnLiveDB —— 【真库逐格对拍】：
// 把 candidateQuerySQL() 的模态接受臂 + verdict gate 逐格跑在真实 PG 上，
// 与 Go 判据 ModalityAccepts 对拍。此前这一验证只靠「静态解析 SQL 文本」，
// 从未在真库上执行过渲染后的 SQL（realdb 测试因无 DSN 静默 skip）。
func TestRealDBModalityFragmentVsGoOnLiveDB(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer pool.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// 登记侧：真库的实际值（不是想象全集）
	rows, err := pool.Query(ctx, "SELECT DISTINCT COALESCE(modality,'text') FROM models_canonical ORDER BY 1")
	if err != nil {
		t.Fatal(err)
	}
	var registered []string
	for rows.Next() {
		var m string
		if err := rows.Scan(&m); err != nil {
			t.Fatal(err)
		}
		registered = append(registered, m)
	}
	rows.Close()

	// 请求侧：判据词表 ∪ 一个未知值
	requested := append([]string{}, ModalityRequestModalities...)
	requested = append(requested, "document", "")

	// 把渲染后的接受臂整体作为布尔表达式求值（单行标量，不需要真表）。
	// COALESCE(mc.modality,'text') 替换成参数 _reg；占位符 $3 替换成 $1。
	exprTemplate := strings.ReplaceAll(ModalityAcceptArmsSQL("$3"),
		"COALESCE(mc.modality, 'text')", "_reg")
	exprTemplate = strings.ReplaceAll(exprTemplate, "$3", "$1")

	drift := 0
	for _, req := range requested {
		for _, reg := range registered {
			expr := strings.ReplaceAll(exprTemplate, "$1", "$1")
			q := "SELECT COALESCE($2,'text'), (" + strings.TrimSpace(expr) + ") FROM (SELECT $2 AS _reg) t"
			var regN string
			var sqlV bool
			if err := pool.QueryRow(ctx, q, req, reg).Scan(&regN, &sqlV); err != nil {
				t.Fatalf("SQL 求值失败 (req=%s reg=%s): %v\\nSQL: %s", req, reg, err, q)
			}
			goV := ModalityAccepts(reg, req)
			if sqlV != goV {
				drift++
				t.Errorf("★ 真库对拍漂移: registered=%q requested=%q SQL=%v Go=%v", reg, req, sqlV, goV)
			}
		}
	}
	fmt.Printf("✔ 真库逐格对拍: %d registered × %d requested = %d 格，漂移 %d\n",
		len(registered), len(requested), len(registered)*len(requested), drift)
}
