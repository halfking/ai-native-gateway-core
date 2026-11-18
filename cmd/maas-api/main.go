package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kaixuan/llm-gateway-go/api"
)

func main() {
	// Get database URL from environment
	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		dbURL = "postgres://localhost:5432/maas?sslmode=disable"
	}

	// Create database connection pool
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		log.Fatalf("Failed to create connection pool: %v", err)
	}
	defer pool.Close()

	// Verify database connection
	if err := pool.Ping(ctx); err != nil {
		log.Fatalf("Failed to ping database: %v", err)
	}
	log.Println("✓ Database connection established")

	// Create API server
	server := api.NewServer(pool)
	router := server.SetupRouter()

	// Get port from environment
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	addr := fmt.Sprintf(":%s", port)

	log.Printf("Starting MaaS API server on %s", addr)
	log.Println("API endpoints:")
	log.Println("  GET  /health")
	log.Println("  POST /api/v1/inventory/sellable/query")
	log.Println("  POST /api/v1/inventory/holds")
	log.Println("  POST /api/v1/inventory/holds/:holdID/confirm")
	log.Println("  POST /api/v1/inventory/holds/:holdID/release")
	log.Println("  POST /api/v1/commercial/quotes/estimate")
	log.Println("  POST /api/v1/trading/entitlements")
	log.Println("  POST /api/v1/ledger/usage/hold")
	log.Println("  POST /api/v1/ledger/usage/:usageHoldID/settle")
	log.Println("  POST /api/v1/ledger/usage/correct")

	// Start server in a goroutine
	go func() {
		if err := router.Run(addr); err != nil {
			log.Fatalf("Failed to start server: %v", err)
		}
	}()

	// Wait for interrupt signal
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	log.Println("Shutting down server...")

	// Give ongoing operations 5 seconds to complete
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	<-ctx.Done()
	log.Println("Server shutdown complete")
}
