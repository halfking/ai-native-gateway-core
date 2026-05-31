package sessions

import (
	"context"
	"encoding/json"

	"github.com/kaixuan/llm-gateway-go/provider"
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