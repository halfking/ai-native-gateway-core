package ursm

import "fmt"

// 缓存Key生成函数

// providerKey 生成Provider缓存key
func providerKey(providerID int) string {
	return fmt.Sprintf("ursm:provider:%d", providerID)
}

// credentialKey 生成Credential缓存key
func credentialKey(credentialID int) string {
	return fmt.Sprintf("ursm:credential:%d", credentialID)
}

// modelKey 生成Model缓存key
func modelKey(credentialID int, model string) string {
	return fmt.Sprintf("ursm:model:%d:%s", credentialID, model)
}

// nodeKey 生成Node缓存key
func nodeKey(credentialID int, model string) string {
	return fmt.Sprintf("ursm:node:%d:%s", credentialID, model)
}

// fpSlotKey 生成指纹槽key
func fpSlotKey(credentialID, slotIndex int) string {
	return fmt.Sprintf("llmgw:cred_fp_slot:%d:%d", credentialID, slotIndex)
}

// fpSlotPrefixKey 生成指纹槽前缀key（用于批量操作）
func fpSlotPrefixKey(credentialID int) string {
	return fmt.Sprintf("llmgw:cred_fp_slot:%d", credentialID)
}

// fpPinKey 生成指纹pin key
func fpPinKey(sessionID string, credentialID int) string {
	return fmt.Sprintf("llmgw:sess_cred_fp:%s:%d", sessionID, credentialID)
}

// modelIndexKey 生成模型级索引key (包含该模型下所有凭据的 rawModel 映射)
// 用于 DB 降级时替代 DB 查询发现候选凭据
func modelIndexKey(model string) string {
	return fmt.Sprintf("ursm:model:%s", model)
}

// concSlotKey 生成并发槽全局计数key
func concSlotKey(credentialID int) string {
	return fmt.Sprintf("llmgw:conc_slot:%d", credentialID)
}

// concSessionKey 生成并发槽会话计数key
func concSessionKey(credentialID int, sessionID string) string {
	return fmt.Sprintf("llmgw:conc_session:%d:%s", credentialID, sessionID)
}
