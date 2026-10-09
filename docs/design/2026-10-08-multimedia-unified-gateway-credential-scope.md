# 多媒体 API 统一网关 + 凭据路由域隔离 + 凭据详情 UI —— 可行性与实施方案

> 状态：**方案定稿；Phase A–C 已落地；245 部署验证通过（seq=2510, migration 847/848）**  
> 日期：2026-10-08  
> 范围：llm-gateway-go 数据面 / 凭据模型 / Admin UI（`/providers/:id?tab=creds`）  
> 关联：`2026-10-03-audio-transcription-gateway-plan.md`、`2026-10-06-audio-multi-provider-and-transform.md`  
> 外部参考：  
> - MiniMax STT：https://platform.minimax.cn/docs/api-reference/speech-to-text  
> - 智谱 ASR：https://docs.bigmodel.cn/api-reference/模型-api/语音转文本  
> - 智谱 TTS：https://docs.bigmodel.cn/api-reference/模型-api/文本转语音  
> - 智谱 Voice：https://docs.bigmodel.cn/cn/guide/models/sound-and-video/glm-4-voice  

---

## 1. 目标与验收口径

| # | 目标 | 验收 |
|---|---|---|
| G1 | 语音/视频/图片类厂商 API 经网关统一为「类文本模型」调用与管理模式 | 统一入口（OpenAI 兼容或 `/v1/{modality}/*`）+ 目录/凭据/绑定/探针/审计齐全 |
| G2 | 特殊服务 key 进凭据表，带**显式标志**；标准 chat/text 调用**绝不**选到这些凭据 | 候选 SQL 硬过滤 + 单测/集成测覆盖负例 |
| G3 | 计费模式差异可建模（按时长 / 按次 / 按 token） | binding 侧计费单元可配置；usage 落库不丢 duration |
| G4 | `/providers/:id?tab=creds` 凭据详情 UI 整齐有序 | 分区折叠、信息密度可控、列表可扫读、i18n 补齐 |

**明确非目标（本轮不做）**：端到端实时双向语音（GLM-4-Voice WebSocket）、视频生成全链路产品化、替换现有 chat 路由内核。

---

## 2. 可行性结论（摘要）

**结论：可以加，且应分三层推进；L1 隔离标志与 UI 本轮必做，L2 多媒体面扩展复用已有音频栈，L3 计费按时长/按次为增强。**

| 维度 | 结论 | 依据 |
|---|---|---|
| 语音 ASR/TTS | **已部分落地，可扩展** | `/v1/audio/*`、MiniMax `speech_to_text`、智谱 multipart、小米 chat-audio 桥接均已存在 |
| 图片 vision | **chat 多模态已通；图生图/专用 API 待适配** | chat content parts 已支持 image；专用 image API 无统一出口 |
| 视频 | **目录 modality 有，数据面薄** | `models_canonical.modality=video` 已有；无独立 video 端点族 |
| 凭据隔离 | **必须新增字段，tags 不够** | `tags` 不参与候选 SQL；仅靠 modality 无法挡「同一 key 绑了 text 模型」误路由 |
| 计费 | **token 主路径成熟；时长/按次需补** | MiniMax ASR 按 `duration` 秒计费；MaaS 现按 multimodal tokens |
| UI | **可做，优先拆分 CredsTab** | 单文件 ~1673 行，抽屉字段堆叠，维护成本高 |

---

## 3. 现状盘点（证据）

### 3.1 已具备

1. **模态轴**：`models_canonical.modality ∈ {text,vision,audio,video,multimodal,embedding}`；`GetCandidatesByModality` 过滤。
2. **音频数据面**：`domains/streaming/audio_service.go` + `audio_minimax_stt.go`；传输形态 `transcriptions | chat-audio | speech-to-text`。
3. **ASR 目录种子**：`ensureAsrCatalogSeed`（`glm-asr` / `minimax-asr-1.0`）。
4. **后处理**：`/v1/audio/refine`、`/v1/audio/analyze`（环回 chat，成本归调用方）。
5. **凭据运营字段**：`plan_type`、`pool_group`、`manual_disabled`、`tags`、`billing_mode`（binding）。

### 3.2 缺口

| 缺口 | 风险 |
|---|---|
| 无凭据级「路由域」标志 | 媒体套餐 key 若绑定/误标 text 模型，会被标准 chat 选中 → **烧钱与流量** |
| 时长计费未进 credits | MiniMax/小米按秒；报表与 MaaS 对账失真 |
| 图片/视频专用 API 无统一出口 | 无法「像文本一样」管理 |
| CredsTab 信息架构扁平 | 运营扫读成本高；硬编码中文 tab；危险操作与日常字段混排 |

### 3.3 上游 API 差异矩阵（方案输入）

| 厂商/模型 | 端点形状 | 计费提示 | 网关适配策略 |
|---|---|---|---|
| MiniMax `asr-1.0` | `POST /v1/speech_to_text` multipart；流式私有 SSE | **按 duration 秒** | 已有 `speech-to-text` 传输；补时长计费 |
| 智谱 `glm-asr-2512` | `POST .../audio/transcriptions` OpenAI 兼容；≤25MB/≤30s | 资源包/按量（套餐敏感） | 透传；凭据需媒体资源包，应用 `media_only` |
| 智谱 `glm-tts` | `POST .../audio/speech` JSON | 按字符/套餐 | 映射 `/v1/audio/speech` |
| 智谱 `glm-4-voice` | chat + `input_audio` 端到端 | chat 用量 | modality=audio/multimodal；**不进标准 text 池** |
| 小米 mimo ASR/TTS | chat-audio 桥接 | usage.seconds | 已实现 |
| 通用 vision | chat image_url | image tokens | 已走 chat；凭据可用 `all` |
| 图生图/视频生成 | 各厂商私有 REST | 按张/秒/次 | Phase C adapter + 统一 `/v1/images/*` `/v1/videos/*` |

---

## 4. 推荐架构

### 4.1 「类文本统一调用」的含义

不是强行把 multipart 音频塞进 `/v1/chat/completions`，而是：

```
管理面统一：Provider → Credential → Model Binding → Probe → Usage → 计费档
数据面分层：
  /v1/chat/completions     → routing_scope ∈ {all, chat_only}
  /v1/audio/*              → routing_scope ∈ {all, media_only} + modality=audio
  /v1/images/*（后续）     → media_only + modality=vision|image
  /v1/videos/*（后续）     → media_only + modality=video
```

客户端体验对齐 OpenAI 音频/图像契约；上游差异收敛在 **Transport Adapter**（已有先例）。

### 4.2 凭据路由域标志（核心新增）

**字段**：`credentials.routing_scope text NOT NULL DEFAULT 'all'`

| 值 | 语义 | chat/text 候选 | audio/vision/video 候选 |
|---|---|---|---|
| `all` | 默认，兼容存量 | ✅ | ✅ |
| `chat_only` | 仅标准文本/工具调用 | ✅ | ❌ |
| `media_only` | 仅多媒体特殊服务 | ❌ | ✅ |

约束：

```sql
CHECK (routing_scope IN ('all', 'chat_only', 'media_only'))
```

**硬隔离规则（候选 SQL）**：

- 请求 modality ∈ `{text,''}`（标准 chat/completions/responses/messages）：  
  `c.routing_scope IN ('all','chat_only')`
- 请求 modality ∈ `{audio,vision,video}` 或音频专用 resolver：  
  `c.routing_scope IN ('all','media_only')`
- embedding 维持 `all|chat_only`（与 text 同域，避免媒体 key 被 embedding 扫到）

**不做**：仅用 `tags` 软标记——必须进 SQL，防止「忘过滤」。

**运营约定**：

1. 智谱/MiniMax **媒体资源包专用 key** → 单独凭据行，`routing_scope=media_only`。  
2. 同一厂商 chat 套餐 key → `chat_only` 或 `all`。  
3. 禁止把 `media_only` 凭据的 binding 挂大量 text 模型后指望「自然不会被选」——SQL 仍会挡，但目录卫生靠 UI 提示。

### 4.3 计费扩展（Phase B）

在 `credential_model_bindings` 增加（或复用扩展）`billing_unit`：

| billing_unit | 含义 | 用量来源 |
|---|---|---|
| `token`（默认） | 现有 per_1M | usage tokens |
| `audio_second` | 按秒 | `duration` / `X-Gw-Audio-Seconds` |
| `request` | 按次 | 固定 1 |
| `image` | 按张 | 生成张数（后续） |

MaaS：`CalcCredits*` 增加 duration 分支；usage 落库保留 `audio_seconds`。  
路由档位：`audio_second`/`request` 归 PAYG 桶，不与 token_plan 混淆。

### 4.4 多媒体面扩展路线（Phase C）

| 阶段 | 内容 |
|---|---|
| C0（已有） | ASR/TTS OpenAI 兼容 + 三传输形态 |
| C1 | 凭据 `routing_scope` + chat 隔离 + Admin/UI |
| C2 | 时长计费 + 报表字段 |
| C3 | `/v1/images/generations` adapter 骨架（1～2 家试点） |
| C4 | `/v1/videos/*` 异步任务模型（job id + poll） |

---

## 5. 结构变更影响矩阵（rule 57）

### 5.1 主结构

| 对象 | 变更 |
|---|---|
| `credentials` | + `routing_scope text NOT NULL DEFAULT 'all'` + CHECK + COMMENT + INDEX |
| Admin DTO / PATCH | 读写 `routing_scope` |
| `candidateQuerySQL` / audio resolver | WHERE 过滤 |
| Web `ProviderCredential` / CredsTab | 展示与编辑 |
| 可选：`credential_model_bindings.billing_unit` | Phase B |

### 5.2 Writer / Reader

| 角色 | 文件（代表） | 动作 |
|---|---|---|
| Writer | `admin/provider_credential.go` add/update | 校验枚举；默认 `all` |
| Writer | migration `8xx_credential_routing_scope.sql` + ensure 镜像 | 加列 |
| Reader | `provider/*` 候选加载 | 按 modality 过滤 scope |
| Reader | `domains/streaming/audio_service.go` | 仅 media 域 |
| Reader | `web/.../CredsTab.vue` + `api/providers.ts` | UI |
| View | `model_offers` / `v_routable_*` | **不改列序**；过滤在 credentials 侧 JOIN 条件 |

### 5.3 兼容

- 存量行 DEFAULT `all` → **行为零变化**。  
- 新标志仅在运营显式设为 `media_only`/`chat_only` 后生效。  
- 回滚：列可保留；代码忽略即退回旧语义（DEFAULT 已是 `all`）。

### 5.4 测试矩阵（先测后改）

| 用例 | 期望 |
|---|---|
| media_only 凭据 + text 请求 | 0 candidates / 不出现在 chat 候选 |
| media_only + audio 请求 | 可被选中（其它条件满足时） |
| chat_only + audio 请求 | 不出现 |
| all + 任意 | 与今日一致 |
| PATCH 非法 scope | 400 |
| UI 保存/列表徽章 | media / chat / all 可辨 |

---

## 6. 凭据详情 UI 重组（G4）

现状问题（源码）：抽屉单列长表单；「信息/模型」硬编码中文；emoji/「手工」前缀；危险区与日常字段同视口；~1673 行难维护。

### 6.1 信息架构

抽屉 `info` 改为**可折叠分区**（默认展开前 3 区）：

1. **身份与密钥** — label、API Key、routing_scope（新）、pool_group  
2. **路由与状态** — status、lifecycle、plan_type、manual_disabled、state_reason  
3. **健康与探针** — health、default_probe_model、立即检测  
4. **容量** — concurrency、fp_slot、生效/过期  
5. **余额与地板** — balance、floors  
6. **用量摘要** — requests/cost  
7. **标签与备注**  
8. **危险操作** — 默认折叠，红色分区置底  

列表列增强：增加 **路由域** 徽章列（`media`/`chat`/`all`），便于扫读防误配。

### 6.2 工程约束

- 拆分子组件：`CredDrawerIdentity.vue` / `CredDrawerRouting.vue` / …（单文件 ≤300 行）。  
- 全部文案走 i18n（去掉硬编码「信息」「模型」）。  
- 保持 `silentRefresh` 契约，禁止误用 `refresh` 卸抽屉。

---

## 7. 实施阶段与顺序

| Phase | 交付 | 预估 |
|---|---|---|
| **A（本轮）** | 方案文档 + `routing_scope` 迁移/ensure + 候选硬过滤 + Admin API + CredsTab 字段与列表徽章 + 测试 | 必做 |
| **A2（本轮）** | CredsTab 分区折叠 UI 重组 + i18n | 必做 |
| **B** | `billing_unit=audio_second` + usage/credits | 紧随 |
| **C** | images/videos 统一出口试点 | 后续独立 goal |

**Gate**：Phase A 合并前必须：候选负例测试绿；手工设 `media_only` 后 chat 探测不到该凭据。

---

## 8. 风险与缓解

| 风险 | 缓解 |
|---|---|
| 运营误标 `media_only` 导致音频 503 | UI 强提示 + 列表徽章；探针按 modality |
| 同一 key 既要 chat 又要 media | **拆成两行凭据**（推荐），或用 `all` |
| 智谱编码套餐无音频包 | 已文档化；`media_only` 避免误打 chat 烧包 |
| CredsTab 大重构回归 | 分区组件 + 浏览器实测凭据抽屉 |

---

## 9. 决策记录

1. **采用 `routing_scope` 枚举三值**，不采用布尔 `is_special`（表达力不足：chat_only 场景也需要）。  
2. **隔离落在候选 SQL**，不落在 handler 事后校验（防漏网路径）。  
3. **统一调用 = 管理面同构 + 数据面 OpenAI 契约出口**，不强制媒体走 chat completions。  
4. **UI 先分区再拆文件**，避免一次性重写 1600 行。

---

## 10. 实施进度

| 项 | 状态 | 证据 |
|---|---|---|
| 方案文档 | ✅ | 本文 |
| 847/848 migration + ensure | ✅ | `sql/migrations/startup/847_*` + `848_*`、`db.ensureCredentialRoutingScope` |
| 候选 SQL 硬隔离 | ✅ | `provider/client.go` CASE on `$3` + shape test |
| Admin CRUD | ✅ | `routing_scope` list/add/PATCH + `admin/routing_scope.go` |
| CredsTab 列表徽章 + 抽屉字段 | ✅ | `CredsTab.vue` + 8 语 i18n |
| 抽屉分区折叠 | ✅ | concurrency/balance/usage/tags 用 `<details>` |
| Phase B 时长计费 | ✅ 核心闭环 | `TokenUsage.AudioSeconds` + audio handler `SetMaas`；`848` cmb.billing_unit + request_logs.audio_seconds 列 |
| Phase C images 骨架 | ✅ | `POST /v1/images/generations` 透传 + modality=image 路由域 |
| Phase C videos 骨架 | ✅ | `POST /v1/videos/generations` 透传；无上游时 501；async poll 另开 |
| request_logs.audio_seconds 落库 | ✅ 轻量路径 | `NewPGAudioUsageLogger` → `request_logs_hot`；完整 telemetry 列对齐可后续合并 |

运营启用步骤：为 MiniMax/智谱等媒体专用 key 的凭据行设 `routing_scope=media_only`，标准 chat 即刻隔离。
