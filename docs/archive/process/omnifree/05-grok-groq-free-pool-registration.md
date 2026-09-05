# Grok/Groq Free-Pool 注册 — 探活失败分析与后续规划

## 任务概述

将 Grok (xAI) 或 Groq (Groq Inc) 的 API Key 注册到 free-pool（`https://llmgo.kxpms.cn/free-pool`）中，实现探活入库。当前探活失败，需进一步排查。

## 执行时间

**创建时间**: 2026-07-19 17:30
**执行条件**: 用户确认以下问题后继续

## 上下文信息

### 用户提供的信息
- **声称**: "申请了 Grok 的帐号"
- **Base URL**: `https://api.groq.com/openai/v1/`
- **API Key**: `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`
- **生成 URL**: `https://api.stytchb2b.groq.com/`

### 已确认的诊断结果

#### 1. 名称混淆：Grok (xAI) ≠ Groq (Groq Inc)

| 属性 | xAI Grok | Groq Inc |
|------|----------|----------|
| Base URL | `https://api.x.ai/v1` | `https://api.groq.com/openai/v1` |
| API Key 格式 | `xai-...` | `gsk_...` |
| Console | `console.x.ai` | `console.groq.com` |

用户提供的 Base URL 和 Key 格式均指向 **Groq**，而非 xAI Grok。

#### 2. API Key 无效（Groq 返回 403）

```bash
# GET /v1/models → 403 Forbidden
curl -s "https://api.groq.com/openai/v1/models" \
  -H "Authorization: Bearer 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"
# → {"error":{"message":"Forbidden"}}

# POST /v1/chat/completions → 403 Forbidden
curl -s -X POST "https://api.groq.com/openai/v1/chat/completions" \
  -H "Authorization: Bearer $KEY" \
  -H "Content-Type: application/json" \
  -d '{"model":"llama-3.3-70b-versatile","messages":[{"role":"user","content":"hi"}],"max_tokens":1}'
# → {"error":{"message":"Forbidden"}}
```

#### 3. stytchb2b 是认证门户，非 API 端点

`api.stytchb2b.groq.com` 返回 `route_not_found`（Stytch B2B 认证系统），所有 API 路径均 404，不是可用的 LLM API 端点。

#### 4. 国内代理中转也无效

测试了 `api.gptsapi.net`、`api.api2gpt.com`、`api.v3.cm` 等多个国内中转，均返回 401 Token invalid。

#### 5. xAI (api.x.ai) 当前网络不可达

从本机网络 `api.x.ai` 连接超时（DNS 可解析 31.13.94.37），可能是 GFW 或网络策略拦截。

### 探活入库代码路径

- **探活函数**: `admin/free_pool_extra.go:697` `probeOpenAICompatibleBase()`
- **快速录入**: `admin/free_pool_extra.go:858` `handleFreePoolQuickEntry()`
- **注册入库**: `admin/free_pool_extra.go:1362` `registerFreeProviderWithCtx()`
- **URL 构造**: `internal/upstreamurl/upstreamurl.go` `ModelsURLCandidates()`
- **前端 API**: `web/src/api/free-pool.ts` `quickEntryFreePool()`
- **前端页面**: `web/src/views/FreePoolView.vue`

## 剩余任务

### P0 — 解决 Key 有效性问题（阻塞项）

- [ ] 用户确认实际注册的是 **Groq** 还是 **xAI Grok**
  - Groq → 去 `https://console.groq.com/keys` 检查/重新生成 Key
  - xAI Grok → 需要提供正确的 `xai-...` 格式 Key 和 `https://api.x.ai/v1` 作为 Base URL
- [ ] 如果 Groq Key 在 console 显示正常但依然 403，需联系 Groq 支持

### P1 — 网络可达性验证（如果目标是 xAI Grok）

- [ ] 确认生产服务器（154/245）能否访问 `api.x.ai:443`
  - 命令: `ssh root@47.97.111.154 -p 25022 "curl -s -o /dev/null -w '%{http_code}' https://api.x.ai/v1/models"`
  - 如果不可达，需添加代理或国内中转
- [ ] 在 free-pool 模板中添加 xAI Grok 条目（如网络可达）
  - `admin/free_pool_extra.go:1518` `freeProviders` map 中添加 `"xai-grok-free"` 条目
  - `admin/free_pool_extra.go:51` `signupPlatforms` 列表中添加 xAI 的 signup 信息

### P2 — 探活逻辑优化（可选）

- [ ] 评估 `probeOpenAICompatibleBase` 对 403 的处理是否需要区分"Key 无效"和"端点/模型列表被保护"
- [ ] 考虑为 Groq 这类返回 403 但 chat/completions 可能正常工作的 provider 添加特殊处理

## 关键命令

### 验证 Groq Key
```bash
export KEY="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"

# 测试 Groq API
curl -s -w "\nHTTP: %{http_code}" "https://api.groq.com/openai/v1/models" \
  -H "Authorization: Bearer $KEY"

# 测试 chat completions
curl -s -w "\nHTTP: %{http_code}" -X POST "https://api.groq.com/openai/v1/chat/completions" \
  -H "Authorization: Bearer $KEY" \
  -H "Content-Type: application/json" \
  -d '{"model":"llama-3.3-70b-versatile","messages":[{"role":"user","content":"hi"}],"max_tokens":1}'
```

### 从生产服务器测试 xAI Grok
```bash
export SSH_KEY_154="$HOME/.ssh/184_id_rsa"
export SSH_HOST_154="root@47.97.111.154"
export SSH_PORT_154="25022"

ssh -i "$SSH_KEY_154" -p "$SSH_PORT_154" "$SSH_HOST_154" \
  "curl -s -o /dev/null -w '%{http_code}' --connect-timeout 10 https://api.x.ai/v1/models"
```

### Free-pool 探活测试
```bash
# 直接调用后端探活 API
curl -s -X POST "https://llmgo.kxpms.cn/api/free-pool/probe" \
  -H "Authorization: Bearer $ADMIN_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"base_url":"https://api.groq.com/openai/v1","api_key":"gsk_..."}'
```

## 快速启动（新会话恢复）

```
继续会话：Grok/Groq Free-Pool 注册 — 探活失败分析与后续

请执行 docs/omnifree/05-grok-groq-free-pool-registration.md 中的所有步骤。

关键上下文：
- Groq API Key 返回 403 Forbidden（无效）
- 用户声称在 api.stytchb2b.groq.com 生成 Key（此为 Stytch B2B 认证门户，非 API）
- 需要用户先确认实际注册的是 Groq 还是 xAI Grok
- Groq console: https://console.groq.com/keys
- xAI Grok console: https://console.x.ai
- 工作目录: /Users/xutaohuang/workspace/official-deploy/services/llm-gateway-go
```

## 项目文件结构

```
llm-gateway-go/
├── admin/
│   ├── free_pool_extra.go    # 探活函数 + 快速录入 + 注册入库 + freeProviders 模板
│   ├── provider_probe.go     # doProbeRequest / doChatProbe 底层探活
│   └── routing.go            # handleFreePoolRegister / handleFreePoolRegisterAll
├── internal/
│   └── upstreamurl/
│       └── upstreamurl.go    # ModelsURLCandidates / ChatCompletionsURL / Build
└── web/src/
    ├── api/free-pool.ts       # 前端 free-pool API 调用
    └── views/FreePoolView.vue # 前端 free-pool 页面
```

## 依赖关系

```
用户确认 Groq vs xAI Grok
    ↓
[Groq] 检查/重新生成 Key        [xAI Grok] 提供正确 Key + Base URL
    ↓                                    ↓
重新探活入库             验证生产服务器到 api.x.ai 网络可达
    ↓                                    ↓
成功 ✓                      在 freeProviders 模板添加 xAI Grok 条目
                                        ↓
                                  重新探活入库
                                        ↓
                                  成功 ✓
```

## 相关文档

- [Free-Pool 探活代码](../admin/free_pool_extra.go)
- [探活底层逻辑](../admin/provider_probe.go)
- [URL 构造 SSOT](../internal/upstreamurl/upstreamurl.go)
- xAI Grok API 文档: https://docs.x.ai/
- Groq API 文档: https://console.groq.com/docs

---

**创建时间**: 2026-07-19 17:30
**创建者**: AI Agent
**任务状态**: ⏸️ 阻塞（等待用户确认服务商和 Key 有效性）
**预计完成**: 待定
