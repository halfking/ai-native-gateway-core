package memory

import "net/http"

// StableHashKey 返回给定字符串的**稳定非负** 32 位哈希（FNV-1a）。
//
// ★ 为什么迁到本包（2026-11-11 上下文智能体审计轮）：
//
//	写侧（把会话事实写进 Memora）与读侧（上下文智能体召回长期记忆）
//	必须落到**同一个** api_key_id 上，否则召回恒空。
//	原实现是 executors 包里的**私有**函数 stableHashKey，读侧拿不到
//	⇒ 两侧各算各的 ⇒ 命名空间永不相等。
//	⇒ 单一真源放本包，写侧与读侧都调它（口径不变，只是搬家）。
//
// ★ 口径说明：FNV-1a 32 位；不同字符串撞车只会**放宽**命名空间，
//
//	不会造成隔离失效（写侧原注释即如此口径）。
func StableHashKey(s string) int {
	h := uint32(2166136261)
	for i := 0; i < len(s); i++ {
		h ^= uint32(s[i])
		h *= 16777619
	}
	return int(h & 0x7fffffff)
}

// APIKeyIDFromToken 从**凭据明文**派生 api_key_id。
//
// ★ 入参必须是**去掉 "Bearer " 前缀之后**的凭据明文 —— 必须与写侧
//
//	逐字节一致，否则同一把钥匙在两侧会得到两个不同的 api_key_id
//	⇒ 召回恒空。
//	（写侧口径：domains/streaming/executors.extractAPIKeyID 对
//	 `Authorization: Bearer <token>` 做 `auth[7:]` 之后哈希。）
func APIKeyIDFromToken(token string) int {
	return StableHashKey(token)
}

// NamespaceFor 派生「写侧同构」的记忆命名空间。
//
// ★ 这是**唯一的**命名空间真源：写侧（写入记忆）与读侧（召回记忆）
//
//	都必须经它派生，否则一边写一边读会对不上、召回恒空。
//
//	三个分量与写侧完全同构：
//
//	租户   → tenant_id（空或 "default" ⇒ 无租户段）
//	凭据   → api_key_id（凭据明文哈希，见 APIKeyIDFromToken）
//	任务   → TaskID 的词表（X-Task-Id / X-Session-Id / body 的
//	         task_id / session_id / 自动兜底 gateway:auto:<key>:<sha1>）
//
// ★ 返回空串表示「派生不出命名空间」（无任务标识）。
//
//	调用方**必须**据此跳过召回，而不是拿空串去查 —— 空串不是「查不到」，
//	是「压根不该查」。
func NamespaceFor(tenantID string, apiKeyID int, r *http.Request, body []byte) (userID string, taskID string) {
	taskID = TaskID(r, body, apiKeyID)
	if taskID == "" {
		return "", ""
	}
	return UserID(tenantID, apiKeyID, taskID), taskID
}
