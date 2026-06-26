package main

import (
	"context"
	"fmt"
	"github.com/jackc/pgx/v5/pgxpool"
)

func main() {
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, "postgres://kxuser:CGpGfdG9De502%2FbdQYXD0Cr4akCVXaJ3@localhost:5434/llm_gateway?sslmode=disable")
	if err != nil {
		fmt.Printf("Pool error: %v\n", err)
		return
	}
	defer pool.Close()

	// Test 1: Simple column
	var n int
	err = pool.QueryRow(ctx, "SELECT 1").Scan(&n)
	if err != nil {
		fmt.Printf("Test1 error: %v\n", err)
	} else {
		fmt.Printf("Test1 OK: %d\n", n)
	}

	// Test 2: model_offers simple
	var mp *int
	err = pool.QueryRow(ctx, "SELECT manual_priority FROM model_offers LIMIT 1").Scan(&mp)
	if err != nil {
		fmt.Printf("Test2 error: %v\n", err)
	} else {
		fmt.Printf("Test2 OK: %v\n", mp)
	}

	// Test 3: mo.manual_priority
	err = pool.QueryRow(ctx, "SELECT mo.manual_priority FROM model_offers mo LIMIT 1").Scan(&mp)
	if err != nil {
		fmt.Printf("Test3 error: %v\n", err)
	} else {
		fmt.Printf("Test3 OK: %v\n", mp)
	}

	// Test 4: count
	var cnt int
	err = pool.QueryRow(ctx, "SELECT COUNT(*) FROM model_offers").Scan(&cnt)
	if err != nil {
		fmt.Printf("Test4 error: %v\n", err)
	} else {
		fmt.Printf("Test4 OK: %d rows in model_offers\n", cnt)
	}
}
