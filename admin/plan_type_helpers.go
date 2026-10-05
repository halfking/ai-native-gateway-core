package admin

// Steps 3-5 minimal implementation for plan_type standardization.
// This file contains the helper functions and will be integrated into
// provider_credential.go, pricing.go, and routing.go via edits.

// validPlanTypes mirrors migrations/136 CHECK constraint.
var validPlanTypes = map[string]bool{
	"token": true, "token_plan": true, "code_plan": true,
	"agent_plan": true, "monthly": true, "free": true,
}

func isValidPlanType(s string) bool { return validPlanTypes[s] }

// validConcurrencyModes mirrors the credentials_concurrency_mode_check
// constraint (migration 479). See docs/会话优化v2/57.
var validConcurrencyModes = map[string]bool{
	"concurrency": true, "rpm": true, "tpm": true, "disabled": true,
}

func isValidConcurrencyMode(s string) bool { return validConcurrencyModes[s] }

// 本文件原来还有一个 deriveBillingModeSQL 常量（"CASE WHEN $1 = 'token'
// THEN 'per_token' ELSE $1 END"），注释写着「used in multiple UPDATEs」——
// 2026-10-06 核过：它**零使用者**，而同一段字面量在
// admin/provider_credential.go:704 内联了一份。死代码 + 假陈述，删掉。
//
// ⚠ 但「plan_type='token' ⇒ per_token」这条**语义**在仓里有四处、四种写法，
// 删常量不等于收口：
//
//	provider_credential.go:704  CASE WHEN $1='token' THEN 'per_token' ELSE $1 END
//	pricing.go:1045            CASE WHEN c.plan_type='token' THEN 'per_token' ELSE c.plan_type END
//	modelcatalog/upsert.go:114 CASE WHEN cred.plan_type='token' THEN 'per_token' ELSE COALESCE(cred.plan_type,'per_token') END
//	modelcatalog/upsert.go:240 （同上）
//
// 前两处 NULL 的 plan_type 会**保持 NULL**，第三、四处会**变成 'per_token'**。
// 同一个 NULL 走到不同的 billing_mode，这是承重的差异（`per_token` 决定
// 第 13 条检查报不报、token_plan 走不走预付口径）。
// **哪一处是意图不归本文件决定**，所以这里只留下事实，不做统一。
