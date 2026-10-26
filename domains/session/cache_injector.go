package session

import (
	"context"
	"encoding/json"

	"github.com/kaixuan/llm-gateway-go/provider"
	providercatalog "github.com/kaixuan/llm-gateway-go/provider/catalog"
)

type CacheInjector struct {
	sessionMgr *Manager
}

func NewCacheInjector(sessionMgr *Manager) *CacheInjector {
	return &CacheInjector{sessionMgr: sessionMgr}
}

func (ci *CacheInjector) InjectCacheParams(ctx context.Context, sessionID string, bodyBytes []byte, candidate *provider.Candidate) ([]byte, error) {
	if sessionID == "" {
		return bodyBytes, nil
	}

	if candidate == nil || !candidate.SupportsPromptCache {
		return bodyBytes, nil
	}

	session, err := ci.sessionMgr.Get(ctx, sessionID)
	if err != nil {
		return bodyBytes, nil
	}

	var obj map[string]any
	if err := json.Unmarshal(bodyBytes, &obj); err != nil {
		return bodyBytes, nil
	}

	switch candidate.CacheMode {
	case "checkpoint":
		obj["cache_checkpoint"] = session.SessionKey
	case "tokens":
		// 2026-10-27 缓存标准对齐轮：Anthropic 的「自动缓存」厂商形态是
		// 请求体**顶层** cache_control（IR 已支持全链往返）。此前写进
		// metadata 的 cache_control 不是厂方字段——Anthropic metadata 只认
		// user_id，真 Anthropic 上游会 400；对 openai-completions 中转同理
		// 不是合法形态。仅对 anthropic-messages 候选改写厂商形态；其余
		// 协议维持历史约定不动（其目标多为自定义中转，语义未盘点）。
		if candidate.Protocol == providercatalog.ProtocolAnthropicMessages {
			if cc, ok := obj["cache_control"].(map[string]any); ok {
				cc["type"] = "ephemeral"
				obj["cache_control"] = cc
			} else {
				obj["cache_control"] = map[string]any{"type": "ephemeral"}
			}
			break
		}
		if meta, ok := obj["metadata"].(map[string]any); ok {
			if existing, ok := meta["cache_control"].(map[string]any); ok {
				existing["type"] = "ephemeral"
				meta["cache_control"] = existing
			} else {
				meta["cache_control"] = map[string]any{"type": "ephemeral"}
			}
			obj["metadata"] = meta
		} else {
			obj["metadata"] = map[string]any{
				"cache_control": map[string]any{"type": "ephemeral"},
			}
		}
	case "header":
	}

	return json.Marshal(obj)
}
