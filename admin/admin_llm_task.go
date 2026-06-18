package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

const (
	adminLLMTaskSessionTitle   = "session_title"
	adminLLMTaskSessionSummary = "session_summary"
	adminLLMModelAuto          = "auto"
)

// adminLLMTaskConfig drives internal gateway LLM calls (title/summary).
// Loaded from work_type_config when available; defaults are code fallbacks.
type adminLLMTaskConfig struct {
	Key            string
	DefaultProfile string
	TaskHint       string
	SystemPrompt   string
	MaxTokens      int
	Temperature    float64
	DeviceSeed     string
}

type adminLLMChatResult struct {
	Content       string
	ResolvedModel string
}

var defaultAdminLLMTasks = map[string]adminLLMTaskConfig{
	adminLLMTaskSessionTitle: {
		Key:            adminLLMTaskSessionTitle,
		DefaultProfile: "cost_first",
		TaskHint:       "creative",
		SystemPrompt: "你是会话标题生成助手。根据下方完整多轮会话日志，用中文生成一个简短准确的标题（不超过18字），概括用户目标与会话结果。" +
			"只输出标题纯文本：不要引号、编号、解释、XML/HTML 标签、thinking/redacted 标记或英文占位符。",
		MaxTokens:   48,
		Temperature: 0.2,
		DeviceSeed:  "admin-session-title",
	},
	adminLLMTaskSessionSummary: {
		Key:            adminLLMTaskSessionSummary,
		DefaultProfile: "cost_first",
		TaskHint:       "creative",
		SystemPrompt: `你是会话日志分析助手。请严格输出 JSON，格式如下：
{"summary":"一段连贯的中文摘要（80-200字），说明会话目标、关键步骤、最终结果","key_points":["要点1","要点2","要点3"]}
要求：
- summary 必须是完整句子，涵盖：做了什么、怎么做的、结果如何
- key_points 提取 3-5 个关键事实或决策点，每条 15-40 字
- 不要输出 JSON 以外的任何文本
- 如果语料中包含错误信息，务必在总结中提及`,
		MaxTokens:   0,
		Temperature: 0.2,
		DeviceSeed:  "admin-session-summary",
	},
}

func (h *Handler) loadAdminLLMTask(ctx context.Context, key string) adminLLMTaskConfig {
	cfg, ok := defaultAdminLLMTasks[key]
	if !ok {
		cfg = defaultAdminLLMTasks[adminLLMTaskSessionTitle]
		cfg.Key = key
	}
	if h == nil || h.db == nil {
		return cfg
	}

	var profile, l1Task, prompt *string
	err := h.db.QueryRow(ctx, `
		SELECT default_profile, l1_task_type, system_prompt
		FROM work_type_config
		WHERE key = $1 AND enabled = TRUE
	`, key).Scan(&profile, &l1Task, &prompt)
	if err != nil {
		return cfg
	}
	if profile != nil && strings.TrimSpace(*profile) != "" {
		cfg.DefaultProfile = strings.TrimSpace(*profile)
	}
	if l1Task != nil && strings.TrimSpace(*l1Task) != "" {
		cfg.TaskHint = strings.TrimSpace(*l1Task)
	}
	if prompt != nil && strings.TrimSpace(*prompt) != "" {
		cfg.SystemPrompt = strings.TrimSpace(*prompt)
	}
	return cfg
}

func (h *Handler) resolveAdminLLMFallbackModel(ctx context.Context, workTypeKey string) string {
	if v := strings.TrimSpace(os.Getenv("LLM_GATEWAY_ADMIN_LLM_FALLBACK_MODEL")); v != "" {
		return v
	}
	if h == nil || h.db == nil {
		return "minimax-m2.7"
	}
	rows, err := h.db.Query(ctx, `
		SELECT wtmr.canonical_name
		FROM work_type_model_route wtmr
		WHERE wtmr.work_type_key = $1 AND wtmr.enabled = TRUE
		ORDER BY wtmr.weight DESC, wtmr.canonical_name
		LIMIT 8
	`, workTypeKey)
	if err != nil {
		return "minimax-m2.7"
	}
	defer rows.Close()

	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err == nil && strings.TrimSpace(name) != "" {
			names = append(names, strings.TrimSpace(name))
		}
	}
	if len(names) == 0 {
		return "minimax-m2.7"
	}

	var picked string
	err = h.db.QueryRow(ctx, `
		SELECT mo.standardized_name
		FROM work_type_model_route wtmr
		JOIN model_offers mo ON lower(mo.standardized_name) = lower(wtmr.canonical_name)
		JOIN credentials c ON c.id = mo.credential_id
		JOIN providers p ON p.id = c.provider_id
		WHERE wtmr.work_type_key = $1
		  AND wtmr.enabled = TRUE
		  AND p.tenant_id = 'default'
		  AND mo.available IS TRUE
		  AND c.status IN ('active','cooling','degraded')
		  AND p.enabled IS TRUE
		ORDER BY wtmr.weight DESC, wtmr.canonical_name
		LIMIT 1
	`, workTypeKey).Scan(&picked)
	if err == nil && strings.TrimSpace(picked) != "" {
		return picked
	}
	return names[0]
}

func (h *Handler) callAdminLLMChat(
	ctx context.Context,
	r *http.Request,
	apiKey string,
	taskKey string,
	taskID string,
	userContent string,
) (adminLLMChatResult, error) {
	task := h.loadAdminLLMTask(ctx, taskKey)

	content, resolvedModel, err := h.postAdminLLMChat(ctx, r, apiKey, task, taskID, userContent, adminLLMModelAuto)
	if err == nil {
		return adminLLMChatResult{Content: content, ResolvedModel: resolvedModel}, nil
	}
	if !adminLLMShouldRetryExplicit(err) {
		return adminLLMChatResult{}, err
	}

	fallback := h.resolveAdminLLMFallbackModel(ctx, taskKey)
	content, resolvedModel, retryErr := h.postAdminLLMChat(ctx, r, apiKey, task, taskID, userContent, fallback)
	if retryErr != nil {
		return adminLLMChatResult{}, fmt.Errorf("auto failed (%v); fallback %s failed (%v)", err, fallback, retryErr)
	}
	if resolvedModel == "" {
		resolvedModel = fallback
	}
	return adminLLMChatResult{Content: content, ResolvedModel: resolvedModel}, nil
}

func adminLLMShouldRetryExplicit(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "no_candidate") ||
		strings.Contains(msg, "no available provider") ||
		strings.Contains(msg, "auto_route_unavailable")
}

func (h *Handler) postAdminLLMChat(
	ctx context.Context,
	r *http.Request,
	apiKey string,
	task adminLLMTaskConfig,
	taskID string,
	userContent string,
	model string,
) (content string, resolvedModel string, err error) {
	payload := map[string]any{
		"model": model,
		"messages": []map[string]string{
			{"role": "system", "content": task.SystemPrompt},
			{"role": "user", "content": userContent},
		},
		"temperature": task.Temperature,
	}
	if task.MaxTokens > 0 {
		payload["max_tokens"] = task.MaxTokens
	}

	body, _ := json.Marshal(payload)
	endpoint := gatewayEndpointFromRequest(r) + "/v1/chat/completions"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return "", "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("X-Gw-Auto-Profile", task.DefaultProfile)
	req.Header.Set("X-Gw-Task-Hint", task.TaskHint)
	req.Header.Set("X-Gw-Work-Type", task.Key)
	if taskID != "" {
		req.Header.Set("X-Gw-Task-Id", taskID)
	}
	if task.DeviceSeed != "" {
		req.Header.Set("X-Device-Seed", task.DeviceSeed)
	}

	client := &http.Client{Timeout: 20 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()

	resolvedModel = model
	if hdr := strings.TrimSpace(resp.Header.Get("X-Gw-Auto-Decision")); hdr != "" {
		var wire struct {
			ChosenModel string `json:"chosen_model"`
		}
		if json.Unmarshal([]byte(hdr), &wire) == nil && wire.ChosenModel != "" {
			resolvedModel = wire.ChosenModel
		}
	}

	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		msg := strings.TrimSpace(string(raw))
		if msg == "" {
			msg = resp.Status
		}
		return "", resolvedModel, fmt.Errorf("%s", msg)
	}

	var out struct {
		Model   string `json:"model"`
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", resolvedModel, err
	}
	if out.Model != "" && out.Model != adminLLMModelAuto {
		resolvedModel = out.Model
	}
	if len(out.Choices) == 0 {
		return "", resolvedModel, fmt.Errorf("empty completion")
	}
	content = strings.TrimSpace(out.Choices[0].Message.Content)
	if content == "" {
		return "", resolvedModel, fmt.Errorf("empty completion content")
	}
	return content, resolvedModel, nil
}
