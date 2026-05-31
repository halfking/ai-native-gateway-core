package config

import (
	"os"
	"strconv"
	"strings"
)

type Config struct {
	DatabaseURL             string
	SecretKey               string
	CredentialEncryptionKey string
	RedisAddr               string
	RedisPassword            string
	RedisDB                  int
	SessionTTLHours          int
}

func Load() Config {
	redisDB := 0
	if dbStr := os.Getenv("LLM_GATEWAY_REDIS_DB"); dbStr != "" {
		if v, err := strconv.Atoi(dbStr); err == nil {
			redisDB = v
		}
	}

	sessionTTLHours := 168
	if ttlStr := os.Getenv("LLM_GATEWAY_SESSION_TTL_HOURS"); ttlStr != "" {
		if v, err := strconv.Atoi(ttlStr); err == nil {
			sessionTTLHours = v
		}
	}

	return Config{
		DatabaseURL:             firstNonEmpty(os.Getenv("LLM_GATEWAY_DATABASE_URL"), os.Getenv("DATABASE_URL")),
		SecretKey:               firstNonEmpty(os.Getenv("LLM_GATEWAY_SECRET_KEY"), os.Getenv("SECRET_KEY")),
		CredentialEncryptionKey: firstNonEmpty(os.Getenv("LLM_GATEWAY_CREDENTIAL_ENCRYPTION_KEY"), os.Getenv("CREDENTIAL_ENCRYPTION_KEY")),
		RedisAddr:               os.Getenv("LLM_GATEWAY_REDIS_ADDR"),
		RedisPassword:            os.Getenv("LLM_GATEWAY_REDIS_PASSWORD"),
		RedisDB:                  redisDB,
		SessionTTLHours:          sessionTTLHours,
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
