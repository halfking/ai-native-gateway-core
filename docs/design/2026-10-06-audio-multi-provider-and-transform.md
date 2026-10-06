# ASR 多供应商接入 + 转写后处理二件套 —— 2026-10-06/07 轮

接续 `2026-10-03-audio-transcription-gateway-plan.md`（§8 后续候选的第
1、2 条落地）。目标（用户 2026-10-06 指令）：①测试更多 ASR 模型并评估
端上实时可行性 ②精细化转写 ③及时总结分析并给出提示/参考；在 API 层
完成，openpocket 做调用测试并完善调用方法。

代码：2fb2ec53f（多供应商 + refine/analyze + MCP）→ 9590dd663（anthropic
候选放开 + 837 自锁修复）。本地 8782 build 2479/2480 全量实证。

## 1. 上游直连实测矩阵（2026-10-06，cmd/probe-cred 解 key 直打）

| 上游 | 端点 | 形状 | 结论 |
|---|---|---|---|
| MiniMax asr-1.0 | `POST /v1/speech_to_text` | multipart file+model；非流式 `{text,duration,trace_id}`；流式 `data: {index,delta,finish}`（无 [DONE]，终帧带 duration；index 顺序分段，delta 为**新增**） | ✅ 可用；标准路径 `/audio/transcriptions` 404（go mux 裸文本） |
| 智谱 glm-asr | `POST {base}/audio/transcriptions` | OpenAI 兼容 multipart；`glm-asr`/`glm-asr-2512` 均被接受（1113 余额错 ≠ 1211 模型不存在），`glm-asr-flash*` 不存在；coding 路径同能力 | ✅ 端点兼容；❌ 在库 4 把 key 全是编码套餐 → 429 1113「余额不足或无可用资源包」 |

凭据侧事实：zhipu cred 22/163/164 与 minimax 21/42 均 active/healthy，
但 zhipu 全部无法调音频面（**阻塞在凭据充值，不在代码**；充值后零改动可用）。

## 2. 网关改动

### 2.1 MiniMax speech-to-text 传输形态（audio_minimax_stt.go）

- `preferSpeechToText(cand)`：catalog_code==minimax → 形态序钉死
  `[speech-to-text]`（标准路径实测 404，不空跑，与小米钉 chat-audio 同款）。
- `transcribeViaSpeechToText`：multipart 上行；`stream` 与
  response_format∈{srt,vtt,verbose_json} 互斥（沿 openpocket 调研口径）；
  SSE 逐帧 delta 下发、按到达顺序拼接（上游按 index 顺序发帧，实测无乱序）；
  duration 从非流式响应体 / 终帧取，映射 `DurationSeconds`。
- SSE 分帧复用包内 `sseLineReader`（chat-audio 桥接同款最小实现）。

### 2.2 anthropic 协议候选放开（9590dd663）

`audioCandidateSelection` 原本对 `anthropic-messages` 协议候选一刀切——
但转写的 multipart 透传（Bearer + /audio/transcriptions）与 chat 协议无关，
智谱 glm-asr 的唯一通路就在这里。改为：
- Transcribe 侧保留候选，形态序限 `[transcriptions]`（chat-audio 需要
  OpenAI chat 结构，不给）；
- Synthesize 侧维持剪除（无透传形态）。
本地实证：glm-asr 经网关把上游 429/1113 如实映射为 `429 upstream_rate_limited`。

### 2.3 目录数据面种子（ensureAsrCatalogSeed）

两家 ASR 模型都不在 discovery 的 /models 列表里（订阅套餐凭据只吐 chat
模型），纯靠 discovery 永远缺席。启动链幂等种子三层：
`models_canonical`（modality=audio + **manual** 豁免，820 不会翻回）→
`provider_models`（available）→ `credential_model_bindings`（该 provider
全部 active 凭据）。只升不降（text→audio 升级，绝不降级）；真库回归
（db_838_asr_catalog_seed_realdb_test）覆盖幂等 + 升级语义。
命名：canonical=`glm-asr` / `minimax-asr-1.0`（带厂商前缀避免裸名歧义），
outbound=上游真名（asr-1.0）。
**教训复现为代码**：层 3 SQL 曾把未引用的 $1 传给 pgx → 42P18 零 OID
推断（2026-09-14 external PG 轮同款），修复并注释钉死「每个参数必须被引用」。

### 2.4 转写后处理二件套（audio_transform.go）

- `POST /v1/audio/refine`：精细化转写。开关集 ops{punctuation,disfluency,
  itn}（缺省全开）+ hotwords + context；提示词核心约束「不改语义不增删
  事实，不确定保留原文」；`include_corrections` 回执逐条修改；
  `ignored_hotwords` 回执未生效热词（与 chat-audio 的 IgnoredParams 回执
  同一原则——静默吞掉会让调用方误以为已生效）。空 refined 如实 502，
  绝不拿空串覆盖调用方文本。
- `POST /v1/audio/analyze`：实时总结分析。`prior_summary` 非空即增量滚动
  （网关无状态，滚动状态由调用方持有，周期调用）；出参
  summary/key_points/decisions/action_items/open_questions/**hints**/topics。
  style∈{auto,meeting,interview,customer_service,lecture} 调整 hints 侧重。
- LLM 步骤**环回** `/v1/chat/completions`，转发调用方 Authorization：
  chat 面的路由/预算/限流/审计/故障转移全量复用，成本归属调用方（autoroute
  的 HTTP caller 同思路，但转发调用方 key 而非服务 key）。环回目标按
  请求 Host/X-Forwarded-Proto 推导，`LLM_GATEWAY_AUDIO_TRANSFORM_LLM_BASE`
  可覆盖；max_tokens 默认 8192（reasoning 系烧 thinking 预算，autoroute
  O4 同款教训），`LLM_GATEWAY_AUDIO_TRANSFORM_MAX_TOKENS` 可调。
- 错误沿 `writeAudioError` 口径（上游 4xx 分流 / no_provider /
  audio_capacity 类型化）。
- 请求体上限 512KB（≈2 小时逐字稿）；总时长 120s。
- 实测延迟（minimax-text-01）：refine 10.6s / analyze 14.3s——客户端
  超时按 90s 配（openpocket 客户端已按此落地）。

### 2.5 MCP 面（audio_mcp.go）

新增 `refine_transcription` / `analyze_transcription` 两工具（transform
服务未注入时 tools/list 不声明——旧部署零行为变化）；structuredContent
与 content[0] 双形态同 transcribe_audio。四工具面本地实测全通。

## 3. 顺手修的启动自锁（837）

部署本轮构建时发现凡存量库（旧形状 routing_analytics_source）一律起不
来：837 的新定义相对旧视图是**减列**（origin_actor 摘除），而
`CREATE OR REPLACE VIEW` 只允许末尾加列 → 42P16 cannot drop columns
from view，db-open 失败先于迁移，启动自锁。修复：staleDefinition 分支
补 `DROP VIEW routing_analytics_source`（两条 MV 已先 CASCADE 摘依赖），
随后按新形状整组重建。837 作者的本地库形状不同所以没暴露。

## 4. 实证矩阵（本地 8782 build 2480 + openpocket verify 脚本）

| 用例 | 结果 |
|---|---|
| minimax-asr-1.0 zh/en（TTS 回环真值） | 200，**100%/100%**（EN 词级），1.5-2.9s |
| mimo-v2.5-asr zh/en（回归） | 200，100%/100%，1.4-2.8s |
| glm-asr | 429 upstream_rate_limited（1113 余额，**如实分流非 502**） |
| SSE 流式（chat-audio 与 speech-to-text 两形态） | delta 逐帧 + transport 事件 + done；首 delta 585ms |
| refine（messy 输入） | 语气词清理✓ ITN(百分之五十→50%)✓ 热词生效✓ corrections 回执✓ |
| analyze（两轮增量） | 滚动摘要合并新增内容✓ hints 4 条可执行✓ action_items/owner✓ |
| MCP tools/list | 4 工具；tools/call refine/analyze 往返 200 |
| seed 真库 | canonical audio+manual / offers / bindings 三层幂等 |
| streaming 全套 | 150s 绿（含 anthropic 放开后回归） |

复现：`GW_BASE=http://127.0.0.1:8782/v1 GW_KEY=sk-... node
scripts/verify-gateway-audio-multi.mjs`（openpocket 仓）。

## 5. 已知边界与后续候选

- **glm-asr live 绿卡在凭据充值**（4 把编码套餐 key 均无音频资源包）；
  数据面已就绪，充值后零改动。
- ~~refine/analyze 的 LLM 选型要避开被 chat 面 task_type 路由排除的
  模型~~ **2026-10-07 勘误**：上轮「glm-4.7 系被 creative 类排除」的
  归因经 curl 对照证伪——code 类提示同样 503，glm-4.7/glm-5 在本地环境
  是**整体不可路由**（zhipu 文本凭据未接），与任务分类无关。调用方只需
  选可路由模型；网关现已把 503 no_candidate 的 alternatives 建议清单
  透出到 MCP 面与错误语义（见 §6），选型成本进一步降低。
- 环回形态下 refine/analyze 一次调用计两次 key RPM（音频面 + chat 面）；
  客户端已按 12/min 口径节流（verify 脚本 `GW_RPM_GAP_MS`）。
- openpocket 会议流（server_meeting）接入周期 analyze：该文件当轮有并行
  工作在改，为避免撞车本轮只落 Go 客户端
  （backend/internal/llmgateway/audio_transform.go）+ 验收脚本 +
  端上可行性结论（docs/2026-10-06-ondevice-realtime-feasibility.md，
  结论：端上实时档可行且为实时字幕首选，短板由 refine 兜底），会议流
  接线留下一轮。
## 6. 完善轮（2026-10-07，限流单计 + 选型辅助）

- **authenticateTransform**：refine/analyze 的鉴权走 key 校验/键状态/
  预算同一口径但**不消耗 RPM**——LLM 步骤环回 chat 面带同一把调用方
  key 已计一次；双计会把 12/min 的 key 的周期 analyze（产品化轮询节奏
  分钟级）打成 429。跳过无绕过：直连打爆 /v1/audio/refine 最终仍被
  环回 chat 步的同一把 key 限流拦住（一次逻辑调用恰计一次）。MCP
  tools/call 的外层 authenticate 无法按工具名选择性跳过，维持双计，
  是次要路径。
- **503 no_candidate 透出 alternatives**：环回 chat 拿到 503 时解析
  错误体 alternatives[].model，转成 audioNoProviderError——HTTP 面映射
  503 no_provider（与音频面同一语义、同一错误码），MCP 面错误信息带
  完整建议清单（如 kimi-k3、deepseek-v4-pro），调用方换模型即可。
- 会议流产品化（openpocket main e39d63c6）：POST /api/meetings/{id}/
  analyze 实时分析 action——滚动摘要 + hints，录制中只读视图（不写
  会议状态/不覆盖正式纪要），滚动游标进程内存持有，无新增快返零
  LLM 花费；model 空按 preferred → auto 兜底。配套验证脚本补
  「ASR 原始输出 → refine → analyze」真实噪声链路段（[7]）。

- 837 自锁修复影响所有存量库的下次部署；252/245/154 部署前无需额外
  操作（ensure 已自愈），但**首个吃到该修复的构建**应留意启动日志里的
  `routing analytics materialized views ensured (rebuilt=true)`。
