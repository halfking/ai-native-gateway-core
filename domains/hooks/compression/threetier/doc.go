// Package threetier (2026-09-01, audit AUDIT_CONTEXT_COMPRESSION_AND_STREAMING_20260901 §五)
//
// 三层缓存（原始 / 压缩 / 脱敏）的统一访问入口，对接现有 SessionState 已有字段：
//
//   - Tier 1 (Raw):       SessionState.RawTokenEstimate / RawMsgCount
//   - Tier 2 (Compressed): SessionState.CompressedTokens / CompressedMsgs / CompressedPrefixHash / CompressionQuality
//   - Tier 3 (Sanitized):  SessionState.SanitizeMapRef / SanitizeStats
//
// 配合 SessionState.AlignmentMap (v7) 与 CutMarker (v5)，threetier 提供：
//
//   - Range/Layer/Tier 三个枚举常量
//   - Tier(t) 名称 → SessionState 字段名的映射
//   - BuildAlignments 把 v7 AlignmentInfo 归并为按 tier 排列的偏移区间
//   - 三层 offset 一致性校验（detectMisalignment）
//
// 不引入新持久化字段；不依赖 tests/session_cache/ 的 mock 实现（已删除）。
package threetier