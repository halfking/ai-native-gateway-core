// Command migrate-ursm-v2 imports legacy probe state into the URSM v2 Redis
// namespace. The gateway invokes the same bootstrap implementation before an
// authoritative direct start, so manual and startup migrations cannot drift.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	ursmv2 "github.com/kaixuan/llm-gateway-go/domains/ursm/v2"
	"github.com/kaixuan/llm-gateway-go/domains/ursm/v2/bootstrap"
	"github.com/redis/go-redis/v9"
)

const (
	defaultRedisKeyPrefix = "ursm:v2:"
	defaultRedisURL       = "redis://localhost:6379/2"
)

func main() {
	var (
		apply          = flag.Bool("apply", false, "Write Redis state. Default is dry-run.")
		forceOverwrite = flag.Bool("force-overwrite", false, "Overwrite existing admin/live Redis state.")
		redisURL       = flag.String("redis", redisURLFromEnv(), "Redis URL")
		pgDSN          = flag.String("pg", envOr("LLM_GATEWAY_DATABASE_URL", envOr("DATABASE_URL", "")), "Postgres DSN")
		keyPrefix      = flag.String("key-prefix", defaultRedisKeyPrefix, "URSM v2 Redis key prefix")
		tenantID       = flag.String("tenant-id", "", "Restrict migration to one tenant (not valid for direct authoritative start)")
		coolSeconds    = flag.Int("cool-seconds", ursmv2.DefaultConfig().CoolSeconds, "Cooling duration used for migrated failed nodes")
	)
	flag.Parse()

	if *pgDSN == "" {
		log.Fatal("--pg or DATABASE_URL / LLM_GATEWAY_DATABASE_URL is required")
	}
	if *coolSeconds <= 0 {
		log.Fatal("--cool-seconds must be positive")
	}
	if !*apply {
		fmt.Println("[DRY-RUN] no Redis writes; pass --apply after verifying the target settings")
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	pool, err := pgxpool.New(ctx, *pgDSN)
	if err != nil {
		log.Fatalf("postgres connect: %v", err)
	}
	defer pool.Close()
	if err := pool.Ping(ctx); err != nil {
		log.Fatalf("postgres ping: %v", err)
	}

	redisOptions, err := redis.ParseURL(*redisURL)
	if err != nil {
		log.Fatalf("redis parse: %v", err)
	}
	rdb := redis.NewClient(redisOptions)
	defer rdb.Close()
	if err := rdb.Ping(ctx).Err(); err != nil {
		log.Fatalf("redis ping: %v", err)
	}

	result, err := bootstrap.Apply(ctx, bootstrap.Options{
		Pool:           pool,
		Redis:          rdb,
		KeyPrefix:      *keyPrefix,
		TenantID:       *tenantID,
		CoolSeconds:    *coolSeconds,
		ForceOverwrite: *forceOverwrite,
	})
	if err != nil {
		log.Fatalf("URSM v2 bootstrap: %v", err)
	}
	fmt.Printf("URSM v2 migration complete: total=%d written=%d skipped=%d\n", result.Total, result.Written, result.Skipped)
	if *tenantID == "" {
		fmt.Println("The global coverage manifest is ready for authoritative startup.")
	} else {
		fmt.Println("Tenant-only coverage was written; do not use it for authoritative startup.")
	}
}

func envOr(key, def string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return def
}

func redisURLFromEnv() string {
	if value := strings.TrimSpace(os.Getenv("REDIS_URL")); value != "" {
		return value
	}
	addr := envOr("LLM_GATEWAY_REDIS_ADDR", "localhost:6379")
	db := 2
	if raw := strings.TrimSpace(os.Getenv("LLM_GATEWAY_REDIS_DB")); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed >= 0 {
			db = parsed
		}
	}
	endpoint := &url.URL{Scheme: "redis", Host: addr, Path: "/" + strconv.Itoa(db)}
	if password := os.Getenv("LLM_GATEWAY_REDIS_PASSWORD"); password != "" {
		endpoint.User = url.UserPassword("", password)
	}
	return endpoint.String()
}
