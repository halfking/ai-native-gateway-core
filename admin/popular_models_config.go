package admin

import (
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"
)

const popularModelsLookupHoursEnv = "LLM_GATEWAY_DB_POPULAR_MODELS_LOOKUP_HOURS"

func popularModelsLookupWindow() time.Duration {
	raw := strings.TrimSpace(os.Getenv(popularModelsLookupHoursEnv))
	if raw == "" {
		return defaultPopularModelsLookupWindow
	}
	hours, err := strconv.Atoi(raw)
	if err != nil || hours <= 0 {
		slog.Warn("popular models lookup window invalid; using default", "env", popularModelsLookupHoursEnv)
		return defaultPopularModelsLookupWindow
	}
	return time.Duration(hours) * time.Hour
}
