package main

import (
	"strings"
	"testing"
)

func TestEnvOr(t *testing.T) {
	t.Setenv("MIGRATE_TEST_KEY", "from-env")
	if got := envOr("MIGRATE_TEST_KEY", "fallback"); got != "from-env" {
		t.Fatalf("envOr with set key=%q, want from-env", got)
	}
	if got := envOr("MIGRATE_TEST_KEY_UNSET", "fallback"); got != "fallback" {
		t.Fatalf("envOr with unset key=%q, want fallback", got)
	}
	t.Setenv("MIGRATE_TEST_KEY_EMPTY", "")
	if got := envOr("MIGRATE_TEST_KEY_EMPTY", "fallback"); got != "fallback" {
		t.Fatalf("envOr with empty key=%q, want fallback", got)
	}
}

func TestRedisURLFromEnv(t *testing.T) {
	t.Setenv("REDIS_URL", "")
	t.Setenv("LLM_GATEWAY_REDIS_ADDR", "redis.example:6380")
	password := "p" + "@ss/word"
	t.Setenv("LLM_GATEWAY_REDIS_PASSWORD", password)
	t.Setenv("LLM_GATEWAY_REDIS_DB", "7")
	got := redisURLFromEnv()
	if !strings.Contains(got, "redis.example:6380/7") || !strings.Contains(got, "p%40ss%2Fword") {
		t.Fatalf("redisURLFromEnv() = %q, want encoded password and db=7", got)
	}

	t.Setenv("REDIS_URL", "redis://explicit:6379/4")
	if got, want := redisURLFromEnv(), "redis://explicit:6379/4"; got != want {
		t.Fatalf("explicit REDIS_URL = %q, want %q", got, want)
	}

	t.Setenv("REDIS_URL", "")
	t.Setenv("LLM_GATEWAY_REDIS_ADDR", "")
	t.Setenv("LLM_GATEWAY_REDIS_PASSWORD", "")
	t.Setenv("LLM_GATEWAY_REDIS_DB", "")
	if got, want := redisURLFromEnv(), defaultRedisURL; got != want {
		t.Fatalf("default Redis URL = %q, want %q", got, want)
	}
}
