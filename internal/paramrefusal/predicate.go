package paramrefusal

// predicate.go — P0-B（2026-10-14）：白名单/黑名单谓词收敛为 reqprobe
// 的 SSOT。本包保留同名 API 作为薄转调，便于既有 executor / 测试过渡；
// 新代码请直接用 reqprobe.Strippable / StripCandidates。

import "github.com/kaixuan/llm-gateway-go/internal/reqprobe"

// Strippable 转调 reqprobe.Strippable（P0-B SSOT）。
func Strippable(field string) bool { return reqprobe.Strippable(field) }

// StripCandidates 转调 reqprobe.StripCandidates（P0-B SSOT）。
func StripCandidates(field string) []string { return reqprobe.StripCandidates(field) }
