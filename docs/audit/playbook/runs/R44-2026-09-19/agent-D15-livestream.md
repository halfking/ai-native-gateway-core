# D15 可观测性-UX（实时流缓存 token/命中率/会话 ID）子代理报告（窗口：468a1ce82..HEAD，主改动面：5d70ce903）

> R44 轮原文落盘（子代理只读报告，主代理已逐条亲读复核，处置见轮文档）。

## 一、发现候选表

| # | 级别候选 | 发现 | 证据 file:line | 建议处置 |
|---|---|---|---|---|
| 1 | P3 | **命中率公式跨 provider 语义不可比（显示层缺陷，非回归）**。tooltip 用 `cache_read / (cache_read + prompt_tokens)`；OpenAI 系上游 `prompt_tokens` **已包含** cached_tokens（子集语义），Anthropic 系 `input_tokens` **不含** cache_read（互斥语义）。同一真实命中率两类供应商不可横向比较。与既有 UsageCost 缓存经济学面板同口径（web/src/views/admin/UsageCost.vue:443-446），故为**存量约定**而非本 commit 新错 | web/src/components/RequestTile.vue:244；domains/streaming/usage.go:29-56 | tooltip/文档标注口径，或后端统一归一化 prompt 口径 |
| 2 | P3 | **命中率分母把"未上报的 prompt"当 0**：`prompt_tokens ?? 0`，若 tile 只有 cache_read 而无 prompt，denom = r + 0 → 显示 `100%`，与"缺省不冒充"承诺有缝 | web/src/components/RequestTile.vue:244 | prompt 缺省时降级为 `—`，补单测 |
| 3 | P3 | **hub==nil 兜底分支补了 cache 字段却没补 GwSessionID**，两分支可空语义不一致。生产接线 hub 恒非 nil（注释自认 unreachable），无实际触发面 | cmd/gateway/main_livestream.go:186-201 | 顺手补齐或注释说明 |
| 4 | P3(备注) | 派发清单漂移：派发词称改动面含 main_types.go，实际 5d70ce903 未触碰该文件；commit message 称"3 个新单测"实为 4 处 | git show 5d70ce903 --stat | 无代码动作；登记防下次派发按记忆抄清单 |
| 5 | P3(备注) | 前端测试自带 i18n 存根不守护 8 语言 parity（parity 由 verify.sh --web 门禁兜底，本次 8 locale 实测齐全） | web/src/components/RequestTile.test.ts:136-155 | 可选改进，无阻塞 |

无 P0/P1/P2 候选。

## 二、核实为健康的面

- **数据闭环逐跳通过，实时与 replay 同源**：写入侧 handler.go:5687/5937 双路写 CacheRead/WriteTokens → telemetry entry（client.go:252）→ request_logs_hot INSERT（client.go:1150-1165 存量列）；实时路径 hub.LiveRequestFromTelemetry（live_stream_sse.go:2820-2826）+ 兜底分支；Redis payload marshal/unmarshal 对称（live_stream_redis_store.go:886-889/929-931），snake_case 与 TS 接口逐字一致；replay 双路同源（Redis detail hash 全量 payload + DB fallback SQL 已加列且 Scan 对齐，live_stream_sse.go:2405-2407/2434），视图 v2 体确认暴露两列（710:217-218；db/request_logs_view_schema.go:321-322）；overlay 只 patch Status 不丢字段；前端 Object.assign 整对象直通（liveStreamStore.ts:1246）。
- **命中率单一计算点**：后端只透传原始 token 数，比率仅前端一处；`denom > 0 ? pct : '—'`；"分母 0 不冒充"= tile 缺字段整行不渲染 + 分母 0 显示 `—` 两层落点。
- **nil/旧 payload 安全**：Go `*int` unmarshal nil → omitempty 省略 → 前端整行隐藏；滚动升级双向兼容；optional chaining 完整。
- **SSE 帧/内存**：slim tile 索引成员五字段未变；detail 每 tile +2 可选 int；无新增 slice/map，帧频率不变。
- **i18n parity 通过**：8 locale 均 +3 key，同层级、占位符 `{r}{w}{pct}` 齐 3 个；zh-TW 正确台/陆语义区分。
- **测试覆盖**：后端 roundtrip 测试断言保字段/trim/零值不冒充（反向 Contains 断言）+ 白名单测试同步补列并保留"payload 禁含请求体字段"的 D03 横切门；前端 3 个 it 覆盖 62% 精确值/无字段不渲染（not 0%）/分母 0 `—`。
- **UI 复用**：复用 tooltipLine helper 与 `dashboard.liveStream.tooltip` 命名空间，无第二套实现。
- **会话 ID 可见性与既有 admin 面一致**：gw_session_id 既有 UI 已大量展示；tile 走租户隔离 detail key；tooltip 截断 18 字符，无新增泄露面。

## 三、未覆盖项与原因

- go test/vitest/verify.sh 实跑（只读约束）——主代理收口实跑。
- 真机 Redis 存量旧 payload 升级回归（需 staging）；OpenAI 系命中率实测偏差（需真实流量）；RTL 长行视觉表现（静态不可达）。
