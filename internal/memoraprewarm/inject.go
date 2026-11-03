// inject.go — W2：读取旁路字段并渲染注入块（P1-2③，ADR §1 注入半边）。
//
// 读侧与写侧（prewarm.go）刻意同构：读不到/损坏/超预算 ⇒ 空串 =
// 无注入——所有失败形态对出站 prompt 完全不可见（G3）。
package memoraprewarm

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/redis/go-redis/v9"
)

// 注入块形态（ADR §5：有界 2KB + 来源标注）。
const (
	// MaxInjectionBytes 是注入块总预算（含标注头）。
	MaxInjectionBytes = 2048
	// MaxItemRunes 是单条 item 渲染上限（防单条吃满预算）。
	MaxItemRunes    = 480
	injectionHeader = "【长期记忆参考（来自 memora；可能与当前对话无关，仅供上下文）】"
)

// RedisSideReader 是读侧最小面（生产 *redis.Client HGet；测试 fake）。
type RedisSideReader interface {
	ReadMemoraContext(ctx context.Context, sessionID string) (string, error)
}

// prewarmPayload 与 ASM prewarm 响应同形。
type prewarmPayload struct {
	Items []struct {
		MemoryID string  `json:"memory_id"`
		Content  string  `json:"content"`
		Kind     string  `json:"kind"`
		Score    float64 `json:"score"`
	} `json:"items"`
}

// RenderInjection 把旁路字段原始值渲染为出站 prompt 尾部注入块。
// raw 为空/解析失败/无 items/超预算截断后为空 ⇒ 返回 ""（无注入）。
// 条目按序渲染 "- 内容"（来源标注在头），整体不超 MaxInjectionBytes。
func RenderInjection(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return ""
	}
	var p prewarmPayload
	if err := json.Unmarshal([]byte(trimmed), &p); err != nil {
		return ""
	}
	if len(p.Items) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString(injectionHeader)
	for _, it := range p.Items {
		content := truncateRunes(strings.TrimSpace(it.Content), MaxItemRunes)
		if content == "" {
			continue
		}
		line := "\n- " + content
		if b.Len()+len(line) > MaxInjectionBytes {
			break // 预算即止，不硬截断半行
		}
		b.WriteString(line)
	}
	if b.Len() == len(injectionHeader) {
		return "" // 全部条目为空 ⇒ 无注入
	}
	return b.String()
}

// InjectPrompt 把注入块拼到出站 prompt 尾部。injection 为空 ⇒ 原样返回
// （对照组/未命中/未装配完全同形——不受影响的保证就在这里）。
func InjectPrompt(prompt, injection string) string {
	if injection == "" {
		return prompt
	}
	return prompt + "\n\n" + injection
}

// ReadAndRender 是读侧一步：读旁路字段并渲染（读失败=无注入）。
// 调用方持有请求 ctx；读超时预算与写侧对称（300ms 由 reader 客户端配置）。
func ReadAndRender(ctx context.Context, reader RedisSideReader, sessionID string) string {
	if reader == nil || sessionID == "" {
		return ""
	}
	raw, err := reader.ReadMemoraContext(ctx, sessionID)
	if err != nil {
		return ""
	}
	return RenderInjection(raw)
}

// RedisReader 是生产读适配器（与 RedisWriter 同 key 空间）。
type RedisReader struct {
	client *redis.Client
}

// NewRedisReader 构造。
func NewRedisReader(client *redis.Client) *RedisReader { return &RedisReader{client: client} }

// ReadMemoraContext implements RedisSideReader.
func (r *RedisReader) ReadMemoraContext(ctx context.Context, sessionID string) (string, error) {
	if r == nil || r.client == nil {
		return "", fmt.Errorf("prewarm read: nil reader")
	}
	val, err := r.client.HGet(ctx, "session:"+sessionID, FieldName).Result()
	if err != nil {
		return "", err
	}
	return val, nil
}
