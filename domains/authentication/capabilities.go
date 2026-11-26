package authentication

import (
	"bytes"
	"encoding/json"
	"errors"
)

// ErrMalformedCapabilities 标记「这一行的 api_keys.capabilities 不是可解析的
// 能力清单」。
//
// ★ 2026-11-26 12h 审计轮新增（★ 修一个会造成**全站鉴权瘫痪**的读取路径 ★）：
//
//	原先 capabilities 直接扫进 `*[]string` ⇒ pgx 的 JSONCodec 走
//	`json.Unmarshal` ⇒ 一行脏值（`{"a":1}` / `["x",1]` / 非数组）就**返回错误**
//	⇒ 而调用方把扫描错误当作「整批装载失败」⇒ **一行坏数据 ⇒ 全量副本不更新
//	⇒ 全站 API Key 不可用**（不是 403，是认证面整体瘫痪）。
//
//	⇒ 现在：**坏行被隔离**（跳过这一把 Key + 告警），**不牵连整批**。
var ErrMalformedCapabilities = errors.New("api_keys.capabilities is not a valid capability list")

// parseCapabilityList 解析 api_keys.capabilities 的 JSONB 文本。
//
// 返回 (能力列表, 是否可解析)：
//   - 空/NULL ⇒ (nil, true) ⇒ 「无能力」（存量 Key 的常态）；
//   - 合法字符串数组 ⇒ (list, true)；
//   - JSONB `null` ⇒ (nil, true) ⇒ 同「无能力」；
//   - **其它一切**（非法 JSON / 非数组 / 元素非字符串）⇒ (nil, false)
//     ⇒ 调用方**不得**把它当成「无能力」直接放行，也**不得**让它打断整批装载。
//
// ★ 为什么单独解析、而不是让 pgx 直接扫进 `[]string`：★
//	pgx 的 JSONCodec 是**全有或全无**的 ⇒ 一行坏值就整批失败；★
//	能力清单是**逐 Key 独立**的语义 ⇒ 坏一行只该坏那一把 Key。
func parseCapabilityList(raw []byte) ([]string, bool) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return nil, true
	}
	var list []string
	if err := json.Unmarshal(trimmed, &list); err != nil {
		return nil, false
	}
	return list, true
}

// normalizeCapabilityList 把「未提供」与「空清单」统一成**空切片**。
//
// ★ 2026-11-26 12h 审计轮：更新路径若把 nil 切片交给 pgx ⇒ JSONCodec 走
// `json.Marshal([]string(nil))` ⇒ 落库的是 jsonb **`null`**（不是 SQL NULL，
// 所以 `COALESCE(capabilities, '[]'::jsonb)` **不生效**）⇒ 列值形态与创建
// 路径（SQL NULL ⇒ `'[]'`）**不一致** ⇒ `jsonb_array_length(capabilities)`
// 这类 SQL 消费会拿到 NULL 而不是 0。
func normalizeCapabilityList(list []string) []string {
	if list == nil {
		return []string{}
	}
	return list
}
