# 56 · LLM 厂商并发与限流模式参考

> **创建日期**：2026-08-11
> **用途**：为凭据并发模式（`concurrency_mode`）与多层请求队列设计提供事实依据。所有数值均来自各厂商官方文档；文档未公开的数值已显式标注「未公开」，不做臆测。
> **关联**：[57-多层队列调度架构设计方案.md](./57-多层队列调度架构设计方案.md)

---

## 0. TL;DR（给网关运维的最短结论）

1. **所有主流厂商的限流都不是「按单 API Key」计的**，而是按 **账户 / 组织 / 项目 / 部署** 维度合并计算。在网关里**多开 Key（KeyRotator）不能抬高任何厂商的并发或速率上限**，反而可能违反 ToS。
2. 网关数据模型中「一个 credential ≈ 一个上游账户」（多 Key 轮换发生在单个 credential 内部），因此 **请求队列按 credential 建制是正确的**，与厂商「按账户限流」的语义一致。
3. 三种限流维度：
   - **并发数（in-flight cap）**：DeepSeek、智谱 BigModel、Kimi 明确以「同时在途请求数」为主控维度。
   - **RPM（每分钟请求数）**：OpenAI、Gemini、Groq、Cerebras、SambaNova 以速率为主。
   - **TPM/ITPM/OTPM（每分钟 token 数）**：OpenAI、Anthropic、Azure 以令牌速率为主。
4. **Azure / DashScope 即使平均用量低于配额也可能被 burst/短窗/ramp-up 限流**——客户端必须做平滑（削峰），而非仅靠硬上限。
5. **OpenRouter 流式中的限流以 SSE error 事件下发**（非 HTTP 429），网关必须解析流式 chunk，不能只看状态码。

---

## 1. 维度对照总表

| 厂商 | 限流作用域 | 是否有显式并发上限 | 主控维度 | Key 轮换是否抬高上限 |
|---|---|---|---|---|
| **OpenAI**（直连） | 按组织（org），跨 Key 合并 | 无官方硬并发数（实测存在） | RPM / TPM / RPD | 否 |
| **Anthropic Claude** | 按组织（org），跨 Key 合并 | 否（无显式并发连接数） | RPM / ITPM / OTPM | 否 |
| **Azure OpenAI** | 按**部署(deployment)**，配额来自订阅级池 | 否 | TPM（主）/ RPM | 否 |
| **Google Gemini**（AI Studio） | 按**项目(project)**，跨 Key 合并 | 否（仅 Batch API=100 并发） | RPM / TPM / RPD / 滚动 10min 消费额 | 否 |
| **Vertex AI** | 按 区域/项目（Cloud Quotas） | 否 | QPM（每分钟查询） | 否 |
| **DeepSeek** | 按账户（account），跨 Key 合并 | **是（v4-pro 500 / flash 2500）** | 并发数（唯一维度） | 否 |
| **智谱 BigModel**（GLM） | 按账户（账户维度） | **是（按模型，数值未公开）** | 并发数 | 否 |
| **阿里 DashScope**（百炼/通义） | 按**主账号**，跨 RAM 子账号/工作空间/Key 合并 | 隐式（ramp-up） | RPS / TPS / 并发爬坡 | 否 |
| **Moonshot Kimi** | 按账户（按累计充值分档） | **是（1→1000 按档）** | 并发数 / RPM / TPM / TPD | 否 |
| **OpenRouter** | 按账户（全局） | 否（受上游制约） | 免费模型 20 RPM / 50~1000 RPD；付费仅看上游 | 否 |
| **Groq** | 按组织（org） | 否 | RPM / RPD / TPM / TPD | 否 |
| **Cerebras** | 按组织（org） | 否 | RPM / TPM | 否 |
| **SambaNova** | 按用户/账户 | 否 | RPM / RPD / TPD | 否 |

---

## 2. 分厂商明细

### 2.1 OpenAI（直连）
- **作用域**：组织级，账户下所有 Key 共享一个桶。
- **维度**：RPM、TPM、RPD（部分模型），按 usage tier（Tier 1–5，随累计消费/账期升级）。
- **并发**：无官方文档化硬并发上限；社区反馈高 RPM（如 10,000）下存在实际并发约束。
- **行为**：超限返回 HTTP 429，附带 `Retry-After` / `X-RateLimit-Reset`。
- **网关建议**：以 TPM 为主控（RPM 通常 = TPM/1000 量级），并发上限由 TPM 与平均请求时长反推，并始终遵守 `Retry-After`。
- 来源：[Rate limits | OpenAI API](https://developers.openai.com/api/docs/guides/rate-limits)

### 2.2 Anthropic Claude
- **作用域**：组织级，非单 Key。
- **维度**：RPM、ITPM（输入 token/分钟）、OTPM（输出 token/分钟），按 usage tier。
- **并发**：无显式并发连接上限，主要通过 RPM + token/分钟约束吞吐。
- **网关建议**：ITPM/OTPM 是主控；输出 token 速率往往先触顶（长回复场景）。
- 来源：[Rate limits – Claude Platform Docs](https://platform.claude.com/docs/en/api/rate-limits)、[Our approach to rate limits – Anthropic](https://support.claude.com/en/articles/8243635-our-approach-to-rate-limits-for-the-claude-api)

### 2.3 Azure OpenAI（与直连 OpenAI 不同）
- **作用域**：按**部署(deployment)**；配额来自订阅级池。自 2026-05-07 起 Foundry 在**订阅级**追踪：Global Standard 同模型/版本跨区域共享一个池；Data Zone Standard 按数据区共享。
- **维度**：RPM + TPM，按部署分配；7 个 Quota Tier（Free + Tier 1–6）。
- **并发**：无显式并发数；以 TPM 令牌桶为主控（PTU 部署给保证吞吐）。
- **关键坑**：
  - token 用量低于配额时**仍可能 429**（额外 burst/短窗/区域负载节流）。
  - `Retry-After` 可能**非常大**（社区报告最高 86,400s = 24h）。
- **网关建议**：按**部署**维度设上限（TPM 为主），始终遵守 `Retry-After`；客户端做平滑避免 burst。
- 来源：[Azure OpenAI quotas-limits](https://learn.microsoft.com/en-us/azure/foundry/openai/quotas-limits)、[how-to/quota](https://learn.microsoft.com/en-us/azure/foundry/openai/how-to/quota)

### 2.4 Google Gemini / Vertex AI
- **作用域（AI Studio）**：按**项目(project)**，明确「per project, not per API key」。
- **维度**：RPM、TPM、RPD、滚动 10 分钟消费额（如 Tier 1 = $10/10min），按 tier 与模型不同。
- **并发**：`generateContent` / `streamGenerateContent` 无显式并发上限；仅 Batch API = 100 并发。
- **行为**：返回 `429 RESOURCE_EXHAUSTED`；文档未明确 `Retry-After`。
- **Vertex AI**：Cloud Quotas，按区域/项目的 QPM。
- **网关建议**：按项目维度设限（Key 轮换无效），以 RPM/TPM 为主。
- 来源：[Gemini API rate limits](https://ai.google.dev/gemini-api/docs/rate-limits)

### 2.5 DeepSeek（并发数主导）
- **作用域**：按账户，跨 Key 合并（文档原文：「Concurrency limits are calculated at the account level, regardless of which API Key is used」）。
- **维度**：**仅并发数**（不公布 RPM/TPM）。公布值：`deepseek-v4-pro` = 500 并发，`deepseek-v4-flash` = 2500 并发。
- **行为**：超并发返回 HTTP 429；支持 per-`user_id` 隔离与子配额。
- **网关建议**：**最典型的并发数模式**——网关按账户设 in-flight 上限（500 / 2500），多 Key 无效，提额走工单。
- 来源：[DeepSeek rate_limit](https://api-docs.deepseek.com/quick_start/rate_limit/)

### 2.6 智谱 BigModel / GLM
- **作用域**：按账户（「基于账户维度对请求进行限流」）。
- **维度**：主控为**并发数（同时在途请求数）**，按模型独立；未公布数值（仅控制台可见）。
- **错误码**：`1302`（账户级速率/并发超限）、`1305`（平台侧过载）。
- **网关建议**：与 DeepSeek 同类——按账户设 in-flight 上限，数值需从控制台获取。
- 来源：[智谱 rate-limit](https://docs.bigmodel.cn/cn/api/rate-limit)

### 2.7 阿里 DashScope / 通义千问（百炼 / Model Studio）
- **作用域**：按**主账号**，跨 RAM 子账号、业务空间、API Key 合并。
- **维度**：RPS/RPM、TPS/TPM、并发/爬坡(ramp-up)。例：`qwen3-max` = 500 RPS、500,000 tokens/6s。
- **并发**：隐式/动态（平台按整体负载施加并发 + 爬坡）。
- **行为**：返回 429，通常 ~1 分钟自动恢复；DashScope SDK 默认连接池 `limit=100`。
- **网关建议**：令牌桶(RPS) + TPM 桶组合；Key 轮换无效；**客户端必须削峰**（爬坡限制会在平均 RPM 合规时仍 429）。
- 来源：[百炼 rate-limit](https://help.aliyun.com/zh/model-studio/rate-limit)、[限流最佳实践](https://help.aliyun.com/zh/model-studio/rate-limiting-best-practices)

### 2.8 Moonshot Kimi（最干净的并发分档）
- **作用域**：按账户（按累计充值美元分档）。
- **维度**：**并发数 / RPM / TPM / TPD** 四者同时生效，先触顶者节流。
- **并发分档（已公布）**：

  | Tier | 充值 | 并发 | RPM | TPM | TPD |
  |---|---|---|---|---|---|
  | Tier 0 | $1 | **1** | 3 | 500,000 | 1,500,000 |
  | Tier 1 | $10 | **50** | 200 | 2,000,000 | 不限 |
  | Tier 2 | $20 | **100** | 500 | 3,000,000 | 不限 |
  | Tier 3 | $100 | **200** | 5,000 | 3,000,000 | 不限 |
  | Tier 4 | $1,000 | **400** | 5,000 | 4,000,000 | 不限 |
  | Tier 5 | $3,000 | **1,000** | 10,000 | 5,000,000 | 不限 |

- **网关建议**：并发维度可直接作为 in-flight 上限（数值已公开，最适合网关语义）。注意 Tier 0 极紧（并发=1）。
- 来源：[Kimi limits](https://platform.kimi.ai/docs/pricing/limits)

### 2.9 OpenRouter（聚合器）
- **作用域**：按账户，全局治理（文档原文：「Making additional accounts or API keys will not affect your rate limits」）。
- **两层**：
  - **免费 `:free` 模型**：全员 20 RPM；< $10 充值 = 50 RPD，≥ $10 = 1,000 RPD。
  - **付费模型**：平台不限请求，受 **上游厂商** 的 RPM/TPM/并发制约。
- **并发**：平台侧无显式并发数，受上游制约。
- **关键坑**：流式已开始后，限流以 **SSE 事件** `"Rate limit exceeded"` + `"finish_reason":"error"` 下发，**不是干净的 HTTP 429**。
- **网关建议**：免费模型 20 RPM 是硬上限；付费模型的真实上限 = 模型别名背后的上游；必须解析 SSE error，不能只看 HTTP 状态。
- 来源：[OpenRouter limits](https://openrouter.ai/docs/api_reference/limits)

### 2.10 高速推理厂商（Groq / Cerebras / SambaNova）
- **Groq**：按组织（per organization, not per API key）。维度 RPM/RPD/TPM/TPD（按模型×tier）。免费例：`llama-3.1-8b-instant` = 30 RPM / 14,400 RPD / 6,000 TPM / 500,000 TPD。无显式并发数。
- **Cerebras**：按组织。RPM + TPM（先触顶），经验：`requests/s ≈ 10% × RPM`。无显式并发数。
- **SambaNova**：按用户/账户。RPM/RPD/TPD（免费含 TPD）。Developer 全模型合计 20M tokens/day。无显式并发数。
- **网关建议**：三者均为速率型，用 RPM/TPM 令牌桶；无并发维度。
- 来源：[Groq rate-limits](https://console.groq.com/docs/rate-limits)、[Cerebras rate-limits](https://inference-docs.cerebras.ai/support/rate-limits)、[SambaNova rate-limits](https://docs.sambanova.ai/docs/en/models/rate-limits)

---

## 3. 对网关并发模式（`concurrency_mode`）的映射

| `concurrency_mode` | 适用厂商 | 上限来源字段 | 网关行为 |
|---|---|---|---|
| `concurrency` | DeepSeek / 智谱 / Kimi | `concurrency_limit` | in-flight 信号量；超限入凭据队列，按上限平滑释放 |
| `rpm` | OpenAI / Gemini / Groq / Cerebras / SambaNova | `rpm_limit` | 令牌桶（每 `60/rpm` 秒释放 1 个），队列削峰 |
| `tpm` | OpenAI / Anthropic / Azure / DashScope | `tpm_limit`（新增） | 发送前预估 token，令牌桶按 token 预算放行 |
| `disabled` | 内部/不限流 | — | 直通，不走队列调速 |

> 历史凭据默认值：有 `rpm_limit` 且无并发数 → `rpm`；否则 `concurrency`（沿用 `concurrency_limit`）。

---

## 4. 跨凭据 vs 依托凭据（需求点澄清）

- **结论**：请求队列**按凭据建**。原因：厂商限流按账户；网关中 credential ≈ 账户。
- **边界情况**：若同一上游账户挂了多个 credential（少见），其厂商上限是这些 credential **共享**的。v1 暂按 credential 独立队列；「账户组（account-group）共享队列」列为后续增强，需要给 credential 增加 `account_group` 标识并在 dispatch 层聚合 in-flight 计数。
