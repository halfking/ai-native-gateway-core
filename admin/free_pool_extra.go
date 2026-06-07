package admin

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"
)

// ── Static Catalog (mirrors Python free_pool_signup_hub.py) ────────────────

type signupPlatform struct {
	ID            string   `json:"id"`
	Name          string   `json:"name"`
	Category      string   `json:"category"`
	SignupURL     string   `json:"signup_url"`
	APIKeyURL     string   `json:"api_key_url"`
	BaseURL       string   `json:"base_url"`
	CatalogCode   string   `json:"catalog_code"`
	DisplayName   string   `json:"display_name"`
	ModelsHint    string   `json:"models_hint"`
	Notes         string   `json:"notes"`
	Difficulty    string   `json:"difficulty"`
	NeedsEmail    bool     `json:"needs_email"`
	EnvVars       []string `json:"env_vars"`
	Tags          []string `json:"tags"`
}

type signupTool struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	ToolType    string `json:"tool_type"`
	URL         string `json:"url"`
	Description string `json:"description"`
	Builtin     bool   `json:"builtin"`
}

var signupPlatforms = []signupPlatform{
	{ID: "groq", Name: "Groq Cloud", Category: "official",
		SignupURL:   "https://console.groq.com/login",
		APIKeyURL:   "https://console.groq.com/keys",
		BaseURL:     "https://api.groq.com/openai/v1",
		CatalogCode: "groq-free",
		DisplayName: "Groq (Free Tier)",
		ModelsHint:  "llama-3.3-70b-versatile",
		Notes:       "注册后控制台 Create API Key，速度快、免费额度明确",
		EnvVars:     []string{"GROQ_API_KEY"},
		Tags:        []string{"recommended", "fast"}},
	{ID: "gemini", Name: "Google AI Studio", Category: "official",
		SignupURL:   "https://aistudio.google.com/",
		APIKeyURL:   "https://aistudio.google.com/apikey",
		BaseURL:     "https://generativelanguage.googleapis.com/v1beta/openai",
		CatalogCode: "google-gemini-free",
		DisplayName: "Google Gemini (Free Tier)",
		ModelsHint:  "gemini-2.0-flash",
		Notes:       "Google 账号登录 → Get API Key",
		EnvVars:     []string{"GEMINI_API_KEY", "GOOGLE_API_KEY"},
		Tags:        []string{"recommended"}},
	{ID: "openrouter", Name: "OpenRouter", Category: "official",
		SignupURL:   "https://openrouter.ai/signup",
		APIKeyURL:   "https://openrouter.ai/settings/keys",
		BaseURL:     "https://openrouter.ai/api/v1",
		CatalogCode: "openrouter-free",
		DisplayName: "OpenRouter (Free Models)",
		ModelsHint:  "meta-llama/*:free, google/gemini-*:free",
		Notes:       "聚合多家 :free 模型，Key 以 sk-or- 开头",
		EnvVars:     []string{"OPENROUTER_API_KEY"},
		Tags:        []string{"recommended", "aggregator"}},
	{ID: "siliconflow", Name: "SiliconFlow", Category: "official",
		SignupURL:   "https://cloud.siliconflow.cn/account/authentication",
		APIKeyURL:   "https://cloud.siliconflow.cn/account/ak",
		BaseURL:     "https://api.siliconflow.cn/v1",
		CatalogCode: "siliconflow-free",
		DisplayName: "SiliconFlow (Free Tier)",
		ModelsHint:  "Qwen / DeepSeek 蒸馏",
		Notes:       "国内访问友好，手机注册",
		EnvVars:     []string{"SILICONFLOW_API_KEY"},
		Tags:        []string{"cn"}},
	{ID: "zhipu", Name: "智谱 AI", Category: "official",
		SignupURL:   "https://open.bigmodel.cn/login",
		APIKeyURL:   "https://open.bigmodel.cn/usercenter/apikeys",
		BaseURL:     "https://open.bigmodel.cn/api/paas/v4",
		CatalogCode: "zhipu-free",
		DisplayName: "Zhipu GLM (Free Tier)",
		ModelsHint:  "GLM-4-Flash",
		EnvVars:     []string{"ZHIPU_API_KEY", "BIGMODEL_API_KEY"},
		Tags:        []string{"cn"}},
	{ID: "nvidia-nim", Name: "NVIDIA Build", Category: "official",
		SignupURL:   "https://build.nvidia.com/",
		APIKeyURL:   "https://build.nvidia.com/settings/api-key",
		BaseURL:     "https://integrate.api.nvidia.com/v1",
		CatalogCode: "nvidia-nim-free",
		DisplayName: "NVIDIA NIM (Free Tier)",
		ModelsHint:  "llama-3.1-8b-instruct",
		EnvVars:     []string{"NVIDIA_API_KEY", "NVIDIA_NIM_API_KEY"}},
	{ID: "cerebras", Name: "Cerebras Cloud", Category: "official",
		SignupURL:   "https://cloud.cerebras.ai/",
		APIKeyURL:   "https://cloud.cerebras.ai/platform/organization/api-keys",
		BaseURL:     "https://api.cerebras.ai/v1",
		CatalogCode: "cerebras-free",
		DisplayName: "Cerebras (Free Tier)",
		ModelsHint:  "llama3.1-8b",
		EnvVars:     []string{"CEREBRAS_API_KEY"}},
	{ID: "huggingface", Name: "HuggingFace", Category: "official",
		SignupURL:   "https://huggingface.co/join",
		APIKeyURL:   "https://huggingface.co/settings/tokens",
		BaseURL:     "https://router.huggingface.co/v1",
		CatalogCode: "huggingface-free",
		DisplayName: "HuggingFace Inference",
		ModelsHint:  "Read token + Inference Providers",
		EnvVars:     []string{"HF_TOKEN", "HUGGINGFACE_API_KEY"}},
	{ID: "aigocode", Name: "AIGoCode", Category: "relay",
		SignupURL:   "https://docs.aigocode.com/docs/getting-started/base-url",
		APIKeyURL:   "https://docs.aigocode.com/docs/getting-started/base-url",
		BaseURL:     "https://api.aigocode.com/v1",
		CatalogCode: "aigocode-free",
		DisplayName: "AIGoCode (Proxy)",
		ModelsHint:  "gpt-4o-mini, claude, gemini",
		Notes:       "VS Code 插件或文档页获取 sk- Key；OpenAI 兼容",
		EnvVars:     []string{"AIGOCODE_API_KEY", "AIGOCODE_DEV_KEY"},
		Tags:        []string{"relay", "recommended"},
		Difficulty:  "medium"},
	{ID: "free-v36", Name: "Free ChatGPT API (v36)", Category: "relay",
		SignupURL:   "https://free.v36.cm/",
		APIKeyURL:   "https://free.v36.cm/",
		BaseURL:     "https://free.v36.cm/v1",
		CatalogCode: "free-chatgpt",
		DisplayName: "Free ChatGPT API (Community)",
		ModelsHint:  "gpt-4o-mini",
		Notes:       "社区中转，注册页获取 Key",
		EnvVars:     []string{"FREE_CHATGPT_API_KEY", "V36_API_KEY"},
		Tags:        []string{"relay"},
		Difficulty:  "medium"},
	{ID: "together", Name: "Together AI", Category: "relay",
		SignupURL:   "https://api.together.xyz/signup",
		APIKeyURL:   "https://api.together.xyz/settings/api-keys",
		BaseURL:     "https://api.together.xyz/v1",
		CatalogCode: "together-free",
		DisplayName: "Together AI (Free Credits)",
		ModelsHint:  "Llama-3.2-3B",
		Notes:       "新用户赠金，用完需充值",
		EnvVars:     []string{"TOGETHER_API_KEY"},
		Tags:        []string{"relay"}},
	{ID: "mistral", Name: "Mistral AI", Category: "official",
		SignupURL:   "https://console.mistral.ai/",
		APIKeyURL:   "https://console.mistral.ai/api-keys",
		BaseURL:     "https://api.mistral.ai/v1",
		CatalogCode: "mistral-free",
		DisplayName: "Mistral (Free Tier)",
		EnvVars:     []string{"MISTRAL_API_KEY"}},
	{ID: "sambanova", Name: "SambaNova Cloud", Category: "official",
		SignupURL:   "https://cloud.sambanova.ai/",
		APIKeyURL:   "https://cloud.sambanova.ai/apis",
		BaseURL:     "https://api.sambanova.ai/v1",
		CatalogCode: "sambanova-free",
		DisplayName: "SambaNova (Free Tier)",
		EnvVars:     []string{"SAMBANOVA_API_KEY"}},
	{ID: "llm7", Name: "LLM7.io", Category: "community",
		SignupURL:   "https://token.llm7.io/",
		BaseURL:     "https://api.llm7.io/v1",
		CatalogCode: "llm7-free",
		DisplayName: "LLM7 (No Registration)",
		ModelsHint:  "gpt-4o-mini, deepseek-chat",
		Notes:       "无需 Key，discovery 自动接入；可选注册提高限额",
		NeedsEmail:  false,
		Difficulty:  "easy",
		Tags:        []string{"no-key"}},
	{ID: "pollinations", Name: "Pollinations", Category: "community",
		SignupURL:   "https://pollinations.ai/",
		BaseURL:     "https://text.pollinations.ai/openai",
		CatalogCode: "pollinations-free",
		DisplayName: "Pollinations (No Key)",
		ModelsHint:  "openai",
		Notes:       "无需 Key",
		NeedsEmail:  false,
		Tags:        []string{"no-key"}},
}

var signupTools = []signupTool{
	{ID: "temp-email-mailtm", Name: "临时邮箱 (mail.tm)",
		ToolType: "temp_email", Description: "一键生成可收验证码的临时邮箱（内置）", Builtin: true},
	{ID: "temp-email-guerrilla", Name: "Guerrilla Mail",
		ToolType: "temp_email", URL: "https://www.guerrillamail.com/",
		Description: "在浏览器打开临时邮箱收验证码"},
	{ID: "temp-email-10min", Name: "10 Minute Mail",
		ToolType: "temp_email", URL: "https://10minutemail.com/", Description: "10 分钟有效临时邮箱"},
	{ID: "cliproxyapi-docs", Name: "CLIProxyAPI (OAuth)",
		ToolType: "oauth", URL: "https://github.com/router-for-me/CLIProxyAPI",
		Description: "Gemini/Codex OAuth token 桥接，配合「OAuth 桥接」按钮"},
	{ID: "freellmapi-list", Name: "FreeLLMAPI Provider List",
		ToolType: "docs", URL: "https://github.com/tashfeenahmed/freellmapi/blob/main/docs/providers.md",
		Description: "社区维护的免费 Provider 列表，discovery 已订阅"},
}

var signupWorkflow = []map[string]any{
	{"step": 1, "title": "选平台", "detail": "在「注册助手」按官方 / 中转 / 社区分类选择目标平台"},
	{"step": 2, "title": "准备邮箱", "detail": "需要邮箱的平台：点击「生成临时邮箱」，复制地址用于注册"},
	{"step": 3, "title": "打开注册页", "detail": "点击「打开注册」或「获取 Key 页」，完成 signup 并复制 API Key"},
	{"step": 4, "title": "快速录入", "detail": "粘贴 Base URL + API Key →「探活验证」→「探活并入库」"},
}

var signupCategories = []map[string]any{
	{"id": "official", "label": "官方免费层", "description": "厂商控制台注册，稳定可靠"},
	{"id": "relay", "label": "中转 / 代理平台", "description": "OpenAI 兼容中转，Key 通常来自插件或文档"},
	{"id": "community", "label": "社区免 Key", "description": "无需凭据，discovery 自动接入"},
	{"id": "tool", "label": "辅助工具", "description": "临时邮箱、OAuth 文档等"},
}

func platformByID(id string) *signupPlatform {
	for i, p := range signupPlatforms {
		if p.ID == id {
			return &signupPlatforms[i]
		}
	}
	return nil
}

// ── Acquisition Methods (mirrors Python free_pool_methods.py) ───────────────

var acquisitionMethods = []map[string]any{
	{
		"mode":      "signup",
		"title":     "官方注册 + 环境变量",
		"summary":   "在厂商控制台完成新手注册，将 API Key 写入 71 的 config/free-pool.env，由定时任务或「导入环境变量 Key」注入池子。",
		"steps":     []string{"在模板目录点击「注册」完成厂商 signup", "将 Key 写入 /opt/llm-gateway/config/free-pool.env（如 OPENROUTER_API_KEY=sk-...）", "重启 llm-gateway 或 POST /api/free-pool/import-env"},
		"risk":      "low",
		"automated": true,
	},
	{
		"mode":      "no_key",
		"title":     "无 Key 社区端点",
		"summary":   "LLM7、Pollinations 等公开端点，无需凭据，discovery 每小时自动 upsert。",
		"steps":     []string{"无需人工操作", "discovery 自动注册/更新 model_offers"},
		"risk":      "medium",
		"automated": true,
	},
	{
		"mode":      "oauth",
		"title":     "OAuth 桥接",
		"summary":   "从 CLIProxyAPI auth 目录、bridge JSON 或 OAUTH_*_ACCESS_TOKEN 环境变量同步 OAuth access token。",
		"steps":     []string{"配置 CLIPROXYAPI_AUTH_DIR 或 FREE_POOL_OAUTH_BRIDGE_FILE", "POST /api/free-pool/bridge-oauth"},
		"risk":      "medium",
		"automated": true,
	},
	{
		"mode":      "mirrored",
		"title":     "生产 Key 镜像",
		"summary":   "将已有付费池 active 凭据（如 nvidia-main-key）解密后镜像为 free tier 路由（routing_tier=9）。",
		"steps":     []string{"POST /api/free-pool/bootstrap 触发 mirror_from_existing"},
		"risk":      "low",
		"automated": true,
	},
	{
		"mode":      "discovered",
		"title":     "GitHub 列表学习",
		"summary":   "从 curated 开源列表解析 /v1 端点；仅通过 HTTP 探活且不在 blocklist 的端点才注册为 discovered-* provider。",
		"steps":     []string{"定时 discovery 拉取 cool-ai-stuff / freellmapi 列表", "探活 /models 或 /v1/models 返回 200/401 才入库"},
		"risk":      "high",
		"automated": true,
	},
	{
		"mode":      "manual",
		"title":     "管理台手动注册",
		"summary":   "在 free-pool UI 填写 catalog_code / base_url / api_key 手动注册。",
		"steps":     []string{"UI「添加 Provider」或 POST /api/free-pool/register"},
		"risk":      "low",
		"automated": false,
	},
	{
		"mode":      "signup_hub",
		"title":     "注册助手 + 快速录入",
		"summary":   "UI「注册助手」Tab：平台导航、临时邮箱、Base URL + Key 探活并加密入库。",
		"steps":     []string{"选择官方/中转平台 → 打开注册页", "可选：生成 mail.tm 临时邮箱收验证码", "复制 API Key → 快速录入 → 探活并入库"},
		"risk":      "low",
		"automated": false,
	},
}

var auditRules = []map[string]any{
	{"id": "auth-post", "title": "写操作需 Admin 鉴权", "status": "enforced",
		"detail": "所有 POST /api/free-pool/* 需 admin application Bearer token。"},
	{"id": "db-encrypted-keys", "title": "Key 加密入库", "status": "enforced",
		"detail": "免费池 API Key 存入 credentials.secret_ciphertext（AES）；acquisition_source/detail 记录来源；UI 仅展示掩码。"},
	{"id": "no-git-secrets", "title": "Key 不入库", "status": "enforced",
		"detail": "凭据仅通过 config/free-pool.env（gitignore）或运行时环境变量注入，encrypt_secret 写入 DB。"},
	{"id": "discovered-probe", "title": "发现端点必须探活", "status": "enforced",
		"detail": "discovered provider 注册前 GET /models；blocklist 拦截内网/回环/自家域名。"},
	{"id": "pool-cap", "title": "池子上限", "status": "enforced",
		"detail": "free pool_group 凭据总数不超过 FREE_POOL_MAX_PROVIDERS（默认 30）。"},
	{"id": "placeholder-oauth", "title": "占位 OAuth 自动禁用", "status": "enforced",
		"detail": "含 REPLACE_WITH 的 bridge token 跳过注入；bootstrap 清理 disabled 行。"},
	{"id": "tier-isolation", "title": "路由隔离", "status": "enforced",
		"detail": "免费 offer 固定 billing_mode=free、routing_tier=9，composite_score=0 优先于付费路由。"},
}

// ── Discovery State (in-memory singleton) ─────────────────────────────────

type freePoolDiscoveryState struct {
	IntervalSec int                    `json:"interval_sec"`
	LastResult  map[string]any         `json:"last_result"`
}

var discoveryState = &freePoolDiscoveryState{
	IntervalSec: 3600,
	LastResult:  map[string]any{},
}

// ── Handlers ─────────────────────────────────────────────────────────────

func (h *Handler) handleFreePoolMethods(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"methods":     acquisitionMethods,
		"audit_rules": auditRules,
		"scheduler": map[string]any{
			"enabled":      true,
			"interval_sec": discoveryState.IntervalSec,
			"last_result":  discoveryState.LastResult,
		},
	})
}

func (h *Handler) handleFreePoolDiscoveryStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"interval_sec": discoveryState.IntervalSec,
		"last_result":  discoveryState.LastResult,
	})
}

func (h *Handler) handleFreePoolSignupHub(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if h.db == nil {
		writeError(w, http.StatusServiceUnavailable, "database not configured")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()

	rows, err := h.db.Query(ctx, `
		SELECT DISTINCT catalog_code FROM providers WHERE enabled = TRUE AND tenant_id = 'default'
	`)
	registered := make(map[string]bool)
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var code string
			if rows.Scan(&code) == nil {
				registered[code] = true
			}
		}
	}

	platforms := make([]map[string]any, 0, len(signupPlatforms))
	for _, p := range signupPlatforms {
		row := map[string]any{
			"id":               p.ID,
			"name":             p.Name,
			"category":         p.Category,
			"signup_url":       p.SignupURL,
			"api_key_url":      p.APIKeyURL,
			"base_url":         p.BaseURL,
			"catalog_code":     p.CatalogCode,
			"display_name":     p.DisplayName,
			"models_hint":      p.ModelsHint,
			"notes":            p.Notes,
			"difficulty":       p.Difficulty,
			"needs_email":      p.NeedsEmail,
			"env_vars":         p.EnvVars,
			"tags":             p.Tags,
			"pool_registered":  p.CatalogCode != "" && registered[p.CatalogCode],
		}
		platforms = append(platforms, row)
	}

	tools := make([]map[string]any, 0, len(signupTools))
	for _, t := range signupTools {
		tools = append(tools, map[string]any{
			"id":          t.ID,
			"name":        t.Name,
			"tool_type":   t.ToolType,
			"url":         t.URL,
			"description": t.Description,
			"builtin":     t.Builtin,
		})
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"platforms":  platforms,
		"tools":      tools,
		"workflow":   signupWorkflow,
		"categories": signupCategories,
	})
}

// ── Mail.tm temp email (free_pool_temp_email.py) ──────────────────────────

const mailTMBase = "https://api.mail.tm"

func (h *Handler) handleFreePoolTempEmail(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	domainsReq, _ := http.NewRequestWithContext(ctx, http.MethodGet, mailTMBase+"/domains", nil)
	domainsResp, err := http.DefaultClient.Do(domainsReq)
	if err != nil {
		writeError(w, http.StatusBadGateway, "mail.tm domains request failed: "+err.Error())
		return
	}
	defer domainsResp.Body.Close()

	bodyBytes, _ := io.ReadAll(domainsResp.Body)
	var domainsData struct {
		Members []map[string]any `json:"hydra:member"`
	}
	if err := json.Unmarshal(bodyBytes, &domainsData); err != nil {
		writeError(w, http.StatusBadGateway, "invalid domains response")
		return
	}
	if len(domainsData.Members) == 0 {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": "no_domains_available"})
		return
	}
	domain, _ := domainsData.Members[0]["domain"].(string)
	if domain == "" {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": "invalid_domain_response"})
		return
	}

	address, password, err := randomMailAddress(domain)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "random address failed")
		return
	}

	// Create account
	createBody, _ := json.Marshal(map[string]any{
		"address":  address,
		"password": password,
	})
	createReq, _ := http.NewRequestWithContext(ctx, http.MethodPost, mailTMBase+"/accounts", bytes.NewReader(createBody))
	createReq.Header.Set("Content-Type", "application/json")
	createResp, err := http.DefaultClient.Do(createReq)
	if err != nil {
		writeError(w, http.StatusBadGateway, "account create failed: "+err.Error())
		return
	}
	defer createResp.Body.Close()
	if createResp.StatusCode != http.StatusOK && createResp.StatusCode != http.StatusCreated {
		bodyBytes, _ := io.ReadAll(createResp.Body)
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":     false,
			"error":  "account_create_failed",
			"detail": string(bodyBytes)[:min(200, len(bodyBytes))],
		})
		return
	}
	io.Copy(io.Discard, createResp.Body)

	// Get token
	tokenBody, _ := json.Marshal(map[string]any{
		"address":  address,
		"password": password,
	})
	tokenReq, _ := http.NewRequestWithContext(ctx, http.MethodPost, mailTMBase+"/token", bytes.NewReader(tokenBody))
	tokenReq.Header.Set("Content-Type", "application/json")
	tokenResp, err := http.DefaultClient.Do(tokenReq)
	if err != nil {
		writeError(w, http.StatusBadGateway, "token request failed: "+err.Error())
		return
	}
	defer tokenResp.Body.Close()
	if tokenResp.StatusCode != http.StatusOK && tokenResp.StatusCode != http.StatusCreated {
		bodyBytes, _ := io.ReadAll(tokenResp.Body)
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":     false,
			"error":  "token_failed",
			"detail": string(bodyBytes)[:min(200, len(bodyBytes))],
		})
		return
	}

	var tokenData struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(tokenResp.Body).Decode(&tokenData); err != nil {
		writeError(w, http.StatusBadGateway, "token decode failed")
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"ok":           true,
		"provider":     "mail.tm",
		"address":      address,
		"password":     password,
		"token":        tokenData.Token,
		"web_url":      "https://mail.tm/en/",
		"expires_hint": "临时邮箱，建议注册完成后尽快复制 Key 入库",
	})
}

func randomMailAddress(domain string) (string, string, error) {
	const alphabet = "abcdefghijklmnopqrstuvwxyz0123456789"
	local := make([]byte, 10)
	for i := range local {
		n, err := rand.Int(rand.Reader, big.NewInt(int64(len(alphabet))))
		if err != nil {
			return "", "", err
		}
		local[i] = alphabet[n.Int64()]
	}

	pwd := make([]byte, 16)
	if _, err := rand.Read(pwd); err != nil {
		return "", "", err
	}
	password := fmt.Sprintf("%x", pwd)

	return string(local) + "@" + domain, password, nil
}

func (h *Handler) handleFreePoolTempEmailPoll(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	var req struct {
		Token string `json:"token"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	if strings.TrimSpace(req.Token) == "" {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": "missing_token"})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	httpReq, _ := http.NewRequestWithContext(ctx, http.MethodGet, mailTMBase+"/messages", nil)
	httpReq.Header.Set("Authorization", "Bearer "+req.Token)
	resp, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		writeError(w, http.StatusBadGateway, "mail.tm request failed: "+err.Error())
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": "invalid_token"})
		return
	}
	if resp.StatusCode >= 400 {
		writeError(w, http.StatusBadGateway, fmt.Sprintf("mail.tm returned %d", resp.StatusCode))
		return
	}

	var data struct {
		Members []map[string]any `json:"hydra:member"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		writeError(w, http.StatusBadGateway, "decode failed")
		return
	}

	messages := make([]map[string]any, 0, len(data.Members))
	for _, row := range data.Members {
		from, _ := row["from"].(map[string]any)
		fromAddr, _ := from["address"].(string)
		messages = append(messages, map[string]any{
			"id":         row["id"],
			"from":       fromAddr,
			"subject":    row["subject"],
			"intro":      row["intro"],
			"created_at": row["createdAt"],
		})
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"ok":       true,
		"messages": messages,
		"total":    len(messages),
	})
}

// ── Probe (free_pool_probe.py) ────────────────────────────────────────────

var (
	probeBlockedHosts = map[string]bool{
		"localhost":      true,
		"127.0.0.1":      true,
		"0.0.0.0":        true,
		"llm.kxpms.cn":   true,
		"auth.kxpms.cn":  true,
		"acc.kxpms.cn":   true,
		"mcp.kxpms.cn":   true,
	}
	probePrivateNets = []*net.IPNet{
		mustCIDR("10.0.0.0/8"),
		mustCIDR("172.16.0.0/12"),
		mustCIDR("192.168.0.0/16"),
		mustCIDR("127.0.0.0/8"),
	}
)

func mustCIDR(s string) *net.IPNet {
	_, n, err := net.ParseCIDR(s)
	if err != nil {
		panic(err)
	}
	return n
}

func isAllowedPublicURL(rawURL string) bool {
	u, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return false
	}
	host := strings.ToLower(u.Hostname())
	if host == "" {
		return false
	}
	if probeBlockedHosts[host] {
		return false
	}
	if strings.HasSuffix(host, ".local") || strings.HasSuffix(host, ".internal") {
		return false
	}
	if ip := net.ParseIP(host); ip != nil {
		for _, n := range probePrivateNets {
			if n.Contains(ip) {
				return false
			}
		}
	}
	return true
}

func (h *Handler) handleFreePoolProbe(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	var req struct {
		BaseURL string `json:"base_url"`
		APIKey  string `json:"api_key"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}

	probe, _ := probeOpenAICompatibleBase(req.BaseURL, req.APIKey, 12*time.Second)
	writeJSON(w, http.StatusOK, map[string]any{"probe": probe})
}

func probeOpenAICompatibleBase(rawBase, apiKey string, timeout time.Duration) (map[string]any, error) {
	normalized := strings.TrimRight(rawBase, "/")
	if !isAllowedPublicURL(normalized) {
		return map[string]any{
			"ok":          false,
			"reason":      "blocked_url",
			"status_code": nil,
		}, nil
	}

	headers := map[string]string{}
	if k := strings.TrimSpace(apiKey); k != "" {
		headers["Authorization"] = "Bearer " + k
	}

	client := &http.Client{Timeout: timeout, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 5 {
			return fmt.Errorf("too many redirects")
		}
		return nil
	}}

	candidates := []string{
		normalized + "/models",
		normalized + "/v1/models",
	}
	if strings.HasSuffix(normalized, "/v1") {
		root := strings.TrimRight(normalized, "/v1")
		root = strings.TrimRight(root, "/")
		candidates = append([]string{root + "/v1/models"}, candidates...)
	}

	var lastStatus *int
	var lastError string

	for _, url := range candidates {
		httpReq, _ := http.NewRequest(http.MethodGet, url, nil)
		for k, v := range headers {
			httpReq.Header.Set(k, v)
		}
		resp, err := client.Do(httpReq)
		if err != nil {
			lastError = err.Error()[:min(200, len(err.Error()))]
			continue
		}
		status := resp.StatusCode
		lastStatus = &status
		if status == 200 || status == 401 || status == 403 {
			modelCount := 0
			models := []string{}
			if status == 200 {
				body, _ := io.ReadAll(resp.Body)
				var data map[string]any
				if err := json.Unmarshal(body, &data); err == nil {
					rows, _ := data["data"].([]any)
					if rows == nil {
						rows, _ = data["models"].([]any)
					}
					for _, row := range rows {
						if m, ok := row.(map[string]any); ok {
							id, _ := m["id"].(string)
							if id == "" {
								id, _ = m["name"].(string)
							}
							if id != "" {
								models = append(models, id)
								if len(models) >= 20 {
									break
								}
							}
						}
					}
					modelCount = len(rows)
				}
			}
			resp.Body.Close()

			authOK := status == 200 || (status == 401 && strings.TrimSpace(apiKey) == "")
			if strings.TrimSpace(apiKey) != "" && status == 200 {
				authOK = true
			}
			var authValid *bool
			if strings.TrimSpace(apiKey) != "" {
				b := status == 200
				authValid = &b
			}
			return map[string]any{
				"ok":          authOK,
				"status_code": status,
				"probe_url":   url,
				"model_count": modelCount,
				"models":      models,
				"auth_valid":  authValid,
			}, nil
		}
		bodyBytes, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		lastError = string(bodyBytes)[:min(200, len(bodyBytes))]
	}

	// Fallback: try chat/completions with a 1-token request
	if strings.TrimSpace(apiKey) != "" {
		chatURL := normalized + "/v1/chat/completions"
		if strings.HasSuffix(normalized, "/v1") {
			chatURL = normalized + "/chat/completions"
		}
		body, _ := json.Marshal(map[string]any{
			"model":      "gpt-4o-mini",
			"messages":   []map[string]string{{"role": "user", "content": "ping"}},
			"max_tokens": 1,
		})
		httpReq, _ := http.NewRequest(http.MethodPost, chatURL, bytes.NewReader(body))
		httpReq.Header.Set("Content-Type", "application/json")
		for k, v := range headers {
			httpReq.Header.Set(k, v)
		}
		resp, err := client.Do(httpReq)
		if err == nil {
			status := resp.StatusCode
			lastStatus = &status
			if status == 200 || status == 400 || status == 422 {
				resp.Body.Close()
				authValid := true
				return map[string]any{
					"ok":          true,
					"status_code": status,
					"probe_url":   chatURL,
					"model_count": 0,
					"models":      []string{},
					"auth_valid":  &authValid,
					"probe_mode":  "chat_completions",
				}, nil
			}
			bodyBytes, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			lastError = string(bodyBytes)[:min(200, len(bodyBytes))]
		} else {
			lastError = err.Error()[:min(200, len(err.Error()))]
		}
	}

	return map[string]any{
		"ok":          false,
		"reason":      "probe_failed",
		"status_code": lastStatus,
		"error":       lastError,
	}, nil
}

// ── Quick entry (free_pool.py quick_entry) ───────────────────────────────

func slugFromURL(rawURL string) string {
	re := regexp.MustCompile(`https?://([^/]+)`)
	m := re.FindStringSubmatch(rawURL)
	if len(m) < 2 {
		return ""
	}
	domain := m[1]
	slug := domain
	for _, suffix := range []string{"api.", ".com", ".cn", ".ai", ".io"} {
		slug = strings.ReplaceAll(slug, suffix, "")
	}
	slug = strings.ToLower(slug)
	re2 := regexp.MustCompile(`[^a-z0-9-]`)
	slug = re2.ReplaceAllString(slug, "-")
	return strings.Trim(slug, "-")
}

func (h *Handler) handleFreePoolQuickEntry(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if h.db == nil {
		writeError(w, http.StatusServiceUnavailable, "database not configured")
		return
	}

	var req struct {
		SignupURL          string   `json:"signup_url"`
		BaseURL            string   `json:"base_url"`
		APIKey             string   `json:"api_key"`
		DisplayName        string   `json:"display_name"`
		CatalogCode        string   `json:"catalog_code"`
		Models             []string `json:"models"`
		Protocol           string   `json:"protocol"`
		Source             string   `json:"source"`
		SourceDetail       string   `json:"source_detail"`
		Label              string   `json:"label"`
		PlatformID         string   `json:"platform_id"`
		ProbeFirst         bool     `json:"probe_first"`
		Save               bool     `json:"save"`
		NoAPIKeyRequired   bool     `json:"no_api_key_required"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}

	platform := platformByID(req.PlatformID)

	if platform != nil && req.BaseURL == "" {
		req.BaseURL = platform.BaseURL
	}
	if platform != nil && req.CatalogCode == "" {
		req.CatalogCode = platform.CatalogCode
	}
	if platform != nil && req.DisplayName == "" {
		req.DisplayName = platform.DisplayName
	}
	if platform != nil && req.SignupURL == "" {
		req.SignupURL = platform.SignupURL
	}

	catalogCode := strings.TrimSpace(req.CatalogCode)
	if catalogCode == "" {
		slug := slugFromURL(req.BaseURL)
		if slug == "" {
			slug = "custom"
		}
		if !strings.HasSuffix(slug, "-free") {
			slug = slug + "-free"
		}
		catalogCode = slug
	}

	var probeResult map[string]any
	if req.ProbeFirst && strings.TrimSpace(req.BaseURL) != "" {
		p, _ := probeOpenAICompatibleBase(req.BaseURL, req.APIKey, 10*time.Second)
		probeResult = p
		if strings.TrimSpace(req.APIKey) != "" {
			if authValid, ok := probeResult["auth_valid"].(bool); ok && !authValid {
				writeJSON(w, http.StatusOK, map[string]any{
					"status": "probe_failed",
					"probe":  probeResult,
					"error":  "API Key 探活未通过，请检查 base_url 与 Key",
				})
				return
			}
		}
		if !probeOK(probeResult) && strings.TrimSpace(req.APIKey) != "" {
			writeJSON(w, http.StatusOK, map[string]any{
				"status": "probe_failed",
				"probe":  probeResult,
				"error":  "端点不可达或 Key 无效",
			})
			return
		}
	}

	models := req.Models
	if len(models) == 0 && probeResult != nil {
		if pm, ok := probeResult["models"].([]string); ok {
			models = pm
		}
	}
	if len(models) == 0 && platform != nil && platform.ModelsHint != "" {
		for _, m := range strings.Split(platform.ModelsHint, ",") {
			if t := strings.TrimSpace(m); t != "" {
				models = append(models, t)
			}
		}
	}

	sourceDetail := req.SourceDetail
	if sourceDetail == "" {
		sourceDetail = req.SignupURL
	}
	if sourceDetail == "" {
		sourceDetail = req.Label
	}
	if sourceDetail == "" {
		sourceDetail = catalogCode
	}
	if req.PlatformID != "" {
		sourceDetail = fmt.Sprintf("platform:%s:%s", req.PlatformID, sourceDetail)
	}

	if !req.Save {
		writeJSON(w, http.StatusOK, map[string]any{
			"status":       "probed_only",
			"probe":        probeResult,
			"catalog_code": catalogCode,
			"models":       models,
		})
		return
	}

	if req.BaseURL == "" {
		writeJSON(w, http.StatusOK, map[string]any{
			"status": "error",
			"error":  "base_url required",
		})
		return
	}

	displayName := req.DisplayName
	if displayName == "" {
		displayName = catalogCode
	}
	protocol := req.Protocol
	if protocol == "" {
		protocol = "openai-completions"
	}
	credLabel := req.Label
	if credLabel == "" {
		credLabel = fmt.Sprintf("%s-%s-key", catalogCode, req.Source)
	}
	acquisitionMode := req.Source
	if acquisitionMode == "" {
		acquisitionMode = "signup"
	}

	cfg := freeProviderConfig{
		catalogCode:       catalogCode,
		displayName:       displayName,
		baseURL:           req.BaseURL,
		protocol:          protocol,
		apiKey:            req.APIKey,
		models:            models,
		acquisitionMode:   acquisitionMode,
		acquisitionDetail: sourceDetail,
		credentialLabel:   credLabel,
	}

	h.logAudit(r, "free_pool_quick_entry", map[string]any{
		"catalog_code": catalogCode,
		"platform_id":  req.PlatformID,
		"probed":       req.ProbeFirst,
	})

	writeJSON(w, http.StatusOK, map[string]any{
		"status":       "ok",
		"probe":        probeResult,
		"catalog_code": catalogCode,
		"message":      "use POST /api/free-pool/keys to persist credential",
		"config":       cfg,
	})
}

func probeOK(p map[string]any) bool {
	if p == nil {
		return false
	}
	if v, ok := p["ok"].(bool); ok {
		return v
	}
	return false
}

// ── Pool keys CRUD (free_pool.py keys) ────────────────────────────────────

func (h *Handler) handleFreePoolListKeys(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if h.db == nil {
		writeError(w, http.StatusServiceUnavailable, "database not configured")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	rows, err := h.db.Query(ctx, `
		SELECT
			c.id, c.label, COALESCE(c.status,'unknown'),
			COALESCE(c.availability_state,'ready'),
			COALESCE(c.quota_state,'ok'),
			COALESCE(c.acquisition_source,''),
			COALESCE(c.acquisition_detail,''),
			COALESCE(c.tags::text,'[]'),
			c.secret_ciphertext,
			c.created_at, c.updated_at,
			p.id, p.catalog_code, p.display_name, p.base_url
		FROM credentials c
		JOIN providers p ON p.id = c.provider_id
		WHERE c.pool_group = 'free'
		ORDER BY c.updated_at DESC
	`)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query failed: "+err.Error())
		return
	}
	defer rows.Close()

	keys := make([]map[string]any, 0)
	for rows.Next() {
		var (
			credID, providerID                       int
			credLabel, credStatus, availState        string
			quotaState, acqSource, acqDetail, tags   string
			ciphertext                               []byte
			createdAt, updatedAt                     time.Time
			catalogCode, providerName, baseURL       string
		)
		if err := rows.Scan(&credID, &credLabel, &credStatus, &availState,
			&quotaState, &acqSource, &acqDetail, &tags, &ciphertext,
			&createdAt, &updatedAt, &providerID, &catalogCode, &providerName, &baseURL); err != nil {
			slog.Warn("free-pool: scan row failed", "error", err.Error(), "label", credLabel)
			continue
		}

		hasSecret := len(ciphertext) > 0
		masked := ""
		if hasSecret {
			if pt, derr := h.decryptCredStr(string(ciphertext)); derr == nil {
				masked = maskAPIKey(pt)
			} else {
				masked = "***"
			}
		}

		keys = append(keys, map[string]any{
			"credential_id":       credID,
			"credential_label":    credLabel,
			"credential_status":   credStatus,
			"availability_state":  availState,
			"quota_state":         quotaState,
			"acquisition_source":  acqSource,
			"acquisition_detail":  acqDetail,
			"tags":                tags,
			"has_secret":          hasSecret,
			"key_masked":          masked,
			"created_at":          createdAt,
			"updated_at":          updatedAt,
			"provider_id":         providerID,
			"catalog_code":        catalogCode,
			"provider_name":       providerName,
			"base_url":            baseURL,
		})
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"keys":  keys,
		"total": len(keys),
	})
}

func (h *Handler) handleFreePoolAddKey(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if h.db == nil {
		writeError(w, http.StatusServiceUnavailable, "database not configured")
		return
	}

	var req struct {
		CatalogCode      string   `json:"catalog_code"`
		APIKey           string   `json:"api_key"`
		Source           string   `json:"source"`
		SourceDetail     string   `json:"source_detail"`
		Label            string   `json:"label"`
		DisplayName      string   `json:"display_name"`
		BaseURL          string   `json:"base_url"`
		Models           []string `json:"models"`
		Protocol         string   `json:"protocol"`
		NoAPIKeyRequired bool     `json:"no_api_key_required"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	if strings.TrimSpace(req.CatalogCode) == "" {
		writeError(w, http.StatusBadRequest, "catalog_code required")
		return
	}

	platform := platformByID(req.CatalogCode)
	if platform == nil {
		// Try the static catalog templates already defined (FREE_PROVIDERS list)
		platform = lookupFreeTemplate(req.CatalogCode)
	}

	baseURL := req.BaseURL
	if baseURL == "" && platform != nil {
		baseURL = platform.BaseURL
	}
	if baseURL == "" {
		writeError(w, http.StatusBadRequest, "base_url required for unknown catalog_code")
		return
	}
	displayName := req.DisplayName
	if displayName == "" && platform != nil {
		displayName = platform.DisplayName
	}
	if displayName == "" {
		displayName = req.CatalogCode
	}
	protocol := req.Protocol
	if protocol == "" && platform != nil {
		protocol = "openai-completions"
	}
	if protocol == "" {
		protocol = "openai-completions"
	}
	models := req.Models
	if len(models) == 0 && platform != nil && platform.ModelsHint != "" {
		for _, m := range strings.Split(platform.ModelsHint, ",") {
			if t := strings.TrimSpace(m); t != "" {
				models = append(models, t)
			}
		}
	}
	label := req.Label
	if label == "" {
		source := req.Source
		if source == "" {
			source = "manual"
		}
		label = fmt.Sprintf("%s-%s-key", req.CatalogCode, source)
	}
	source := req.Source
	if source == "" {
		source = "manual"
	}
	acqDetail := req.SourceDetail
	if acqDetail == "" {
		acqDetail = req.Label
	}
	if acqDetail == "" {
		acqDetail = req.CatalogCode
	}

	cfg := freeProviderConfig{
		catalogCode:       req.CatalogCode,
		displayName:       displayName,
		baseURL:           baseURL,
		protocol:          protocol,
		apiKey:            req.APIKey,
		models:            models,
		acquisitionMode:   source,
		acquisitionDetail: acqDetail,
		credentialLabel:   label,
	}

	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()

	result := h.registerFreeProviderWithCtx(ctx, cfg)
	if status, ok := result["status"].(string); !ok || status != "registered" {
		h.logAudit(r, "free_pool_add_key_failed", result)
		writeJSON(w, http.StatusOK, map[string]any{
			"status": "error",
			"error":  result,
		})
		return
	}

	h.logAudit(r, "free_pool_add_key", result)
	resp := map[string]any{"status": "ok"}
	for k, v := range result {
		resp[k] = v
	}
	writeJSON(w, http.StatusOK, resp)
}

func (h *Handler) handleFreePoolAddKeysBulk(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if h.db == nil {
		writeError(w, http.StatusServiceUnavailable, "database not configured")
		return
	}

	var req struct {
		Keys []struct {
			CatalogCode  string `json:"catalog_code"`
			APIKey       string `json:"api_key"`
			Source       string `json:"source"`
			SourceDetail string `json:"source_detail"`
			Label        string `json:"label"`
		} `json:"keys"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	results := make([]map[string]any, 0, len(req.Keys))
	registered := 0
	for _, item := range req.Keys {
		if strings.TrimSpace(item.APIKey) == "" && !strings.HasSuffix(item.CatalogCode, "-free") {
			continue
		}

		platform := platformByID(item.CatalogCode)
		if platform == nil {
			platform = lookupFreeTemplate(item.CatalogCode)
		}
		baseURL := ""
		displayName := item.CatalogCode
		var models []string
		if platform != nil {
			baseURL = platform.BaseURL
			displayName = platform.DisplayName
			if platform.ModelsHint != "" {
				for _, m := range strings.Split(platform.ModelsHint, ",") {
					if t := strings.TrimSpace(m); t != "" {
						models = append(models, t)
					}
				}
			}
		}

		source := item.Source
		if source == "" {
			source = "manual"
		}
		label := item.Label
		if label == "" {
			label = fmt.Sprintf("%s-%s-key", item.CatalogCode, source)
		}
		acqDetail := item.SourceDetail
		if acqDetail == "" {
			acqDetail = item.Label
		}
		if acqDetail == "" {
			acqDetail = item.CatalogCode
		}

		cfg := freeProviderConfig{
			catalogCode:       item.CatalogCode,
			displayName:       displayName,
			baseURL:           baseURL,
			protocol:          "openai-completions",
			apiKey:            strings.TrimSpace(item.APIKey),
			models:            models,
			acquisitionMode:   source,
			acquisitionDetail: acqDetail,
			credentialLabel:   label,
		}

		result := h.registerFreeProviderWithCtx(ctx, cfg)
		row := map[string]any{
			"catalog_code": item.CatalogCode,
		}
		for k, v := range result {
			row[k] = v
		}
		results = append(results, row)
		if status, ok := result["status"].(string); ok && status == "registered" {
			registered++
		}
	}

	h.logAudit(r, "free_pool_bulk_add_keys", map[string]any{
		"total":     len(req.Keys),
		"registered": registered,
	})

	writeJSON(w, http.StatusOK, map[string]any{
		"registered": registered,
		"total":      len(req.Keys),
		"results":    results,
	})
}

func (h *Handler) registerFreeProviderWithCtx(ctx context.Context, cfg freeProviderConfig) map[string]any {
	if h.db == nil {
		return map[string]any{"status": "error", "message": "db not configured"}
	}

	// 1. Ensure provider_catalog row
	if _, err := h.db.Exec(ctx, `
		INSERT INTO provider_catalog (code, tier, display_name, category, kind, protocol,
			base_url_template, discovery_strategy, domestic, hidden, notes)
		VALUES ($1, 'restricted', $2, 'aggregator', 'cloud', $3, $4, 'manifest', true, false, $5)
		ON CONFLICT (code) DO UPDATE SET
			display_name = EXCLUDED.display_name,
			base_url_template = EXCLUDED.base_url_template
	`, cfg.catalogCode, cfg.displayName, cfg.protocol, cfg.baseURL, "free_pool:keys"); err != nil {
		return map[string]any{"status": "error", "message": "catalog upsert failed: " + err.Error()}
	}

	// 2. Ensure providers row
	if _, err := h.db.Exec(ctx, `
		INSERT INTO providers (tenant_id, code, display_name, catalog_code, is_custom,
			kind, category, protocol, base_url, egress_profile, domestic, enabled)
		VALUES ('default', $1, $2, $1, false, 'cloud', 'aggregator', $3, $4, 'direct', true, true)
		ON CONFLICT (tenant_id, code) DO UPDATE SET
			display_name = EXCLUDED.display_name,
			base_url = EXCLUDED.base_url,
			enabled = true
	`, cfg.catalogCode, cfg.displayName, cfg.protocol, cfg.baseURL); err != nil {
		return map[string]any{"status": "error", "message": "provider upsert failed: " + err.Error()}
	}

	var providerID int
	if err := h.db.QueryRow(ctx, `SELECT id FROM providers WHERE tenant_id = 'default' AND catalog_code = $1`, cfg.catalogCode).Scan(&providerID); err != nil {
		return map[string]any{"status": "error", "message": "provider lookup failed: " + err.Error()}
	}

	// 3. Encrypt key
	var ciphertext string
	if strings.TrimSpace(cfg.apiKey) != "" {
		encrypted, err := h.encryptCred([]byte(cfg.apiKey))
		if err != nil {
			return map[string]any{"status": "error", "message": "encrypt failed: " + err.Error()}
		}
		ciphertext = encrypted
	}

	// 4. Upsert credential
	credLabel := cfg.credentialLabel
	if credLabel == "" {
		credLabel = fmt.Sprintf("%s-free-key", cfg.catalogCode)
	}

	var credID int
	tagsJSON := fmt.Sprintf(`["free-pool","source:%s","catalog:%s"]`, cfg.acquisitionMode, cfg.catalogCode)

	var cipherParam any
	if ciphertext != "" {
		cipherParam = []byte(ciphertext)
	} else {
		cipherParam = nil
	}

	err := h.db.QueryRow(ctx, `SELECT id FROM credentials WHERE provider_id = $1 AND label = $2`, providerID, credLabel).Scan(&credID)
	if err != nil {
		// Insert
		insertErr := h.db.QueryRow(ctx, `
			INSERT INTO credentials (provider_id, tenant_id, label, secret_ciphertext,
				trust_level, status, lifecycle_status, availability_state, quota_state,
				pool_group, acquisition_source, acquisition_detail, tags)
			VALUES ($1, 'default', $2, $3, 'degraded', 'active',
				'active', 'ready', 'ok', 'free', $4, $5, CAST($6 AS jsonb))
			RETURNING id
		`, providerID, credLabel, cipherParam, cfg.acquisitionMode, cfg.acquisitionDetail, tagsJSON).Scan(&credID)
		if insertErr != nil {
			return map[string]any{"status": "error", "message": "credential insert failed: " + insertErr.Error()}
		}
	} else {
		if _, uerr := h.db.Exec(ctx, `
			UPDATE credentials SET
				secret_ciphertext = $1,
				status = 'active', pool_group = 'free',
				acquisition_source = $2, acquisition_detail = $3,
				tags = CAST($4 AS jsonb), updated_at = NOW()
			WHERE id = $5
		`, cipherParam, cfg.acquisitionMode, cfg.acquisitionDetail, tagsJSON, credID); uerr != nil {
			return map[string]any{"status": "error", "message": "credential update failed: " + uerr.Error()}
		}
	}

	// 5. Insert model offers
	offerCount := 0
	for _, model := range cfg.models {
		var canonID *int
		var cid int
		if err := h.db.QueryRow(ctx, `SELECT id FROM models_canonical WHERE canonical_name ILIKE $1 LIMIT 1`, model).Scan(&cid); err == nil {
			canonID = &cid
		}
		if _, ierr := h.db.Exec(ctx, `
			INSERT INTO model_offers (credential_id, canonical_id, raw_model_name,
				available, routing_tier, billing_mode, currency,
				unit_price_in_per_1m, unit_price_out_per_1m, pricing_source, pricing_updated_at)
			VALUES ($1, $2, $3, true, 9, 'free', 'CNY', 0, 0, 'pool_manager', NOW())
			ON CONFLICT (credential_id, raw_model_name) DO UPDATE SET
				billing_mode = 'free', unit_price_in_per_1m = 0, unit_price_out_per_1m = 0,
				pricing_source = 'pool_manager', pricing_updated_at = NOW(), available = true
		`, credID, canonID, model); ierr == nil {
			offerCount++
		}
	}

	return map[string]any{
		"catalog_code":  cfg.catalogCode,
		"status":        "registered",
		"provider_id":   providerID,
		"credential_id": credID,
		"models":        offerCount,
	}
}

func lookupFreeTemplate(code string) *signupPlatform {
	// Reuse the static catalog list (FREE_PROVIDERS) defined elsewhere
	if tpl, ok := freeProviders[code]; ok {
		return &signupPlatform{
			ID:          code,
			CatalogCode: code,
			BaseURL:     tpl.baseURL,
			DisplayName: tpl.displayName,
		}
	}
	return nil
}

type freeProviderTemplate struct {
	catalogCode     string
	displayName     string
	baseURL         string
	protocol        string
	models          []string
	needsKey        bool
	signupURL       string
	rpmLimit        int
	envVars         []string
	acquisitionMode string
}

// Static catalog mirroring Python FREE_PROVIDERS for free-pool template lookup.
var freeProviders = map[string]freeProviderTemplate{
	"zhipu-free":         {catalogCode: "zhipu-free", displayName: "Zhipu GLM (Free Tier)", baseURL: "https://open.bigmodel.cn/api/paas/v4", protocol: "openai-completions", models: []string{"GLM-4-Flash", "GLM-4.7-Flash", "GLM-4V-Flash"}, rpmLimit: 5, signupURL: "https://open.bigmodel.cn/usercenter/apikeys", envVars: []string{"ZHIPU_API_KEY", "BIGMODEL_API_KEY"}, acquisitionMode: "signup", needsKey: true},
	"siliconflow-free":   {catalogCode: "siliconflow-free", displayName: "SiliconFlow (Free Tier)", baseURL: "https://api.siliconflow.cn/v1", protocol: "openai-completions", models: []string{"Qwen/Qwen2.5-7B-Instruct", "deepseek-ai/DeepSeek-R1-Distill-Qwen-7B"}, rpmLimit: 10, signupURL: "https://cloud.siliconflow.cn/account/ak", envVars: []string{"SILICONFLOW_API_KEY"}, acquisitionMode: "signup", needsKey: true},
	"groq-free":          {catalogCode: "groq-free", displayName: "Groq (Free Tier)", baseURL: "https://api.groq.com/openai/v1", protocol: "openai-completions", models: []string{"llama-3.3-70b-versatile", "llama-3.1-8b-instant", "mixtral-8x7b-32768"}, rpmLimit: 30, signupURL: "https://console.groq.com/keys", envVars: []string{"GROQ_API_KEY"}, acquisitionMode: "signup", needsKey: true},
	"cerebras-free":      {catalogCode: "cerebras-free", displayName: "Cerebras (Free Tier)", baseURL: "https://api.cerebras.ai/v1", protocol: "openai-completions", models: []string{"llama-3.3-70b", "llama-3.1-8b"}, rpmLimit: 30, signupURL: "https://cloud.cerebras.ai", envVars: []string{"CEREBRAS_API_KEY"}, acquisitionMode: "signup", needsKey: true},
	"sambanova-free":     {catalogCode: "sambanova-free", displayName: "SambaNova (Free Tier)", baseURL: "https://api.sambanova.ai/v1", protocol: "openai-completions", models: []string{"Meta-Llama-3.3-70B-Instruct", "Meta-Llama-3.1-8B-Instruct"}, rpmLimit: 10, signupURL: "https://cloud.sambanova.ai/apis", envVars: []string{"SAMBANOVA_API_KEY"}, acquisitionMode: "signup", needsKey: true},
	"google-gemini-free": {catalogCode: "google-gemini-free", displayName: "Google Gemini (Free Tier)", baseURL: "https://generativelanguage.googleapis.com/v1beta/openai", protocol: "openai-completions", models: []string{"gemini-2.0-flash", "gemini-1.5-flash", "gemini-1.5-pro"}, rpmLimit: 15, signupURL: "https://aistudio.google.com/apikey", envVars: []string{"GEMINI_API_KEY", "GOOGLE_API_KEY"}, acquisitionMode: "signup", needsKey: true},
	"mistral-free":       {catalogCode: "mistral-free", displayName: "Mistral (Free Tier)", baseURL: "https://api.mistral.ai/v1", protocol: "openai-completions", models: []string{"mistral-small-latest", "open-mistral-7b"}, rpmLimit: 5, signupURL: "https://console.mistral.ai", envVars: []string{"MISTRAL_API_KEY"}, acquisitionMode: "signup", needsKey: true},
	"openrouter-free":    {catalogCode: "openrouter-free", displayName: "OpenRouter (Free Models)", baseURL: "https://openrouter.ai/api/v1", protocol: "openai-completions", models: []string{"meta-llama/llama-3.3-70b-instruct:free", "meta-llama/llama-3.1-8b-instruct:free"}, rpmLimit: 20, signupURL: "https://openrouter.ai/keys", envVars: []string{"OPENROUTER_API_KEY"}, acquisitionMode: "signup", needsKey: true},
	"together-free":      {catalogCode: "together-free", displayName: "Together (Free Tier)", baseURL: "https://api.together.xyz/v1", protocol: "openai-completions", models: []string{"meta-llama/Llama-3.3-70B-Instruct-Turbo", "meta-llama/Meta-Llama-3.1-8B-Instruct-Turbo"}, rpmLimit: 10, signupURL: "https://api.together.xyz", envVars: []string{"TOGETHER_API_KEY"}, acquisitionMode: "signup", needsKey: true},
	"nvidia-nim-free":    {catalogCode: "nvidia-nim-free", displayName: "NVIDIA NIM (Free Tier)", baseURL: "https://integrate.api.nvidia.com/v1", protocol: "openai-completions", models: []string{"meta/llama-3.3-70b-instruct", "meta/llama-3.1-8b-instruct"}, rpmLimit: 10, signupURL: "https://build.nvidia.com", envVars: []string{"NVIDIA_API_KEY", "NVIDIA_NIM_API_KEY"}, acquisitionMode: "signup", needsKey: true},
	"llm7-free":          {catalogCode: "llm7-free", displayName: "LLM7 (No Registration)", baseURL: "https://api.llm7.io/v1", protocol: "openai-completions", models: []string{"gpt-4o-mini", "deepseek-chat"}, rpmLimit: 60, signupURL: "https://token.llm7.io/", acquisitionMode: "no_key", needsKey: false},
	"pollinations-free":  {catalogCode: "pollinations-free", displayName: "Pollinations (No Key)", baseURL: "https://text.pollinations.ai/openai", protocol: "openai-completions", models: []string{"openai-fast", "openai"}, rpmLimit: 100, acquisitionMode: "no_key", needsKey: false},
	"free-chatgpt":       {catalogCode: "free-chatgpt", displayName: "Free ChatGPT API (Community)", baseURL: "https://free.v36.cm/v1", protocol: "openai-completions", models: []string{"gpt-4o-mini", "gpt-3.5-turbo"}, rpmLimit: 96, signupURL: "https://free.v36.cm/", envVars: []string{"FREE_CHATGPT_API_KEY", "V36_API_KEY"}, acquisitionMode: "signup", needsKey: true},
	"aigocode-free":      {catalogCode: "aigocode-free", displayName: "AIGoCode (Proxy)", baseURL: "https://api.aigocode.com/v1", protocol: "openai-completions", models: []string{"gpt-4o-mini", "claude-sonnet-4-6", "gemini-2.0-flash"}, rpmLimit: 30, signupURL: "https://docs.aigocode.com/docs/getting-started/base-url", envVars: []string{"AIGOCODE_API_KEY", "AIGOCODE_DEV_KEY"}, acquisitionMode: "signup", needsKey: true},
	"huggingface-free":   {catalogCode: "huggingface-free", displayName: "HuggingFace Inference", baseURL: "https://router.huggingface.co/v1", protocol: "openai-completions", models: []string{"meta-llama/Llama-3.2-3B-Instruct", "Qwen/Qwen2.5-7B-Instruct"}, rpmLimit: 10, signupURL: "https://huggingface.co/settings/tokens", envVars: []string{"HF_TOKEN", "HUGGINGFACE_API_KEY"}, acquisitionMode: "signup", needsKey: true},
	"cloudflare-ai-free": {catalogCode: "cloudflare-ai-free", displayName: "Cloudflare Workers AI", baseURL: "https://api.cloudflare.com/client/v4/accounts", protocol: "openai-completions", models: []string{"@cf/meta/llama-3.2-3b-instruct"}, rpmLimit: 10, signupURL: "https://developers.cloudflare.com/workers-ai/", envVars: []string{"CLOUDFLARE_API_TOKEN", "CF_API_TOKEN"}, acquisitionMode: "signup", needsKey: true},
}

// ── Helper to find env var (used by quick_entry validation) ─────────────

func envOrEmpty(name string) string {
	return os.Getenv(name)
}

// ── Keys router (dispatches /keys and /keys/{sub}) ──────────────────────

func (h *Handler) handleFreePoolKeysRouter(w http.ResponseWriter, r *http.Request) {
	if h.db == nil {
		writeError(w, http.StatusServiceUnavailable, "database not configured")
		return
	}
	switch r.Method {
	case http.MethodGet:
		h.handleFreePoolListKeys(w, r)
	case http.MethodPost:
		h.handleFreePoolAddKey(w, r)
	default:
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (h *Handler) handleFreePoolKeysSubRouter(w http.ResponseWriter, r *http.Request) {
	if h.db == nil {
		writeError(w, http.StatusServiceUnavailable, "database not configured")
		return
	}
	remaining := strings.TrimPrefix(r.URL.Path, "/api/free-pool/keys/")
	switch remaining {
	case "bulk":
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		h.handleFreePoolAddKeysBulk(w, r)
	default:
		http.NotFound(w, r)
	}
}
