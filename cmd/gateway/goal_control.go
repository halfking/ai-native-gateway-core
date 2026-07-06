package main

import (
	"database/sql"
	"log/slog"

	streaming "github.com/kaixuan/llm-gateway-go/domains/streaming"
)

// initGoalControl wires the goal/audit response interceptors.
// TODO: recover the full implementation from feat/session-panorama-analytics
// branch (commit 247d5ced). The full version requires goal.PGHistoryStore,
// loop detection, model-switch fields etc. that are not yet on main.
func initGoalControl(db *sql.DB, chatHandler *streaming.ChatHandler) {
	_ = db
	_ = chatHandler
	slog.Info("goal-control: stub (disabled) — full implementation pending")
}
