// Package outputcompliance — owner.go
//
// owner==caller 授权规则：决定敏感数据能否以明文返回给数据面调用方。
//
// 背景（见 docs/2026-07-09-session-tagging-redaction-architecture.md §1.1）：
// 数据面调用方（/v1/chat/*）只有 sk- key，无真实 user_id。唯一的"主人"信号是
// api_keys.owner_user（创建 key 时填的自由文本），与数据行的 owner_user
// （session_dim.owner_user / request_logs.api_key_owner_user）按约定一致。
// 复用 admin/session_tenant.go 的 ownerScopeClause 既有约定，不新建授权表。
package outputcompliance

// RedactionMode 控制输出合规脱敏的力度。
type RedactionMode string

const (
	// RedactOff 不脱敏（仅观察/记录）。
	RedactOff RedactionMode = "off"
	// RedactAlways 无条件脱敏所有命中项（最严）。
	RedactAlways RedactionMode = "always"
	// RedactOwnerMismatch 仅当敏感数据的 owner 与调用方 key owner 不一致时脱敏（默认）。
	RedactOwnerMismatch RedactionMode = "owner_mismatch"
)

// OwnerAllowsSensitive 判断"数据拥有者与调用者同一身份"是否成立。
//
//   - callerOwner：调用方 sk- key 的 owner_user（来自 KeyInfo.OwnerUser）。
//   - dataOwner：敏感数据所在会话/请求的 owner_user（来自 session_dim.owner_user 等）。
//
// 规则（保守，与 admin/session_tenant.go requireSessionOwnerAccess 的 deny 语义一致）：
//   - 调用方无 owner 身份（空）→ false（一律脱敏）
//   - 数据无主人（空）→ false（视为"非自有"，脱敏）
//   - 两者文本相等 → true（允许明文）
//
// 注：这是约定匹配（非外键），依赖 key 创建时 owner_user 与 JWT username 一致。
// 即便约定破裂，脱敏规则独立于可见性规则兜底，不会泄露。
func OwnerAllowsSensitive(callerOwner, dataOwner string) bool {
	if callerOwner == "" {
		return false
	}
	if dataOwner == "" {
		return false
	}
	return callerOwner == dataOwner
}

// ShouldRedact 综合脱敏模式与 owner 判定，决定是否对命中项脱敏。
//
//	mode=off            → 永不脱敏
//	mode=always         → 永远脱敏
//	mode=owner_mismatch → owner 不允许时脱敏
func ShouldRedact(mode RedactionMode, callerOwner, dataOwner string) bool {
	switch mode {
	case RedactOff:
		return false
	case RedactAlways:
		return true
	case RedactOwnerMismatch:
		return !OwnerAllowsSensitive(callerOwner, dataOwner)
	default:
		// 未知模式保守脱敏
		return true
	}
}
