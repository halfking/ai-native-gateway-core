# T2 IR/协议/mock-probe 子代理报告（窗口：2026-09-24T05:00..2026-09-25T05:00, HEAD 9635b9b17）

> R65 轮只读子代理原文留档。主代理复核结论见轮文档。

## 一、发现（候选，待主代理复核）

| # | 级别候选 | 发现 | 证据 file:line | 建议处置 |
|---|---|---|---|---|
| 1 | P2（待复核） | ollama 流式 content 语义**契约自相矛盾**：R63 收口给 ParseOllamaStreamChunk 立的 CONTRACT 断言 Ollama NDJSON `message.content` 是"累计值（迄今全文）"，故只写 `CumulativeContent`、Delta 恒 nil，要求消费方做差分；但仓库自有 wire 文档的流式示例是**增量分片**（首帧 `content:""` → 次帧 `"The"` → 末帧 `"..."`），与真实 Ollama 行为（增量 token 分片）一致。若增量属实，P4 接线后消费方按"累计差分"消费将丢正文/产乱码。当前**零生产调用方**（P4 未接线），不可达，属接线前必清的地雷 | internal/ir/parse_ollama_stream.go:35-45,103-123 vs docs/vendor-formats/ollama.md:49-56 | 对照活体 Ollama 实测定契约；接线 P4 前必须闭环【已修：CONTRACT 注释钉存疑门+交叉引用；语义留待活体抓包】 |
| 2 | P3 | SerializeOllama 对**多模态与多轮工具消息静默丢内容**：消息含任一非 text 块时 `content` 整体置 ""；assistant 历史里的 tool_calls 完全不序列化。注释自认"executor 必须在闸门拒绝多模态"，但该闸门属 P4 范畴**不存在**。当前零生产调用方，纯潜伏 | internal/ir/serialize_ollama.go:173-215、26 | P4 接线前补 schema 闸或序列化 tool_calls【登记】 |
| 3 | P3 | e1f4be463 新增的 `credentialSessionPingResponse.Probe` 是**零消费方死字段**：前端 TS 接口不含 `probe`；无任何 Go 读方。且 session-ping 实际由 admin 进程**直连供应商**，本就不进 request_logs/usage_facts | admin/node_operations.go:135-137,221-223,263-270；web/src/api/credential-monitor.ts:124-133 | 前端消费或注释降级【登记】 |
| 4 | P3（待复核） | DetectProtocol 的 ollama 独占键判定**单键即判 0.85 且先于 Responses 检测**：顶层出现任一 `options`（任意类型）/`keep_alive`/`raw`(bool)/`format`(string) 即返回 ollama-chat 0.85。现实影响有界：唯一生产消费点把它当 `clientProtocol` 元数据，今天只是遥测/日志口径噪声 | internal/ir/detect.go:112-135、146-156；domains/streaming/handler.go:3952-3959 | `options` 收紧为 object 判定或降 ≥2 键【登记】 |
| 5 | P3 | 停机窗口的在途探测会记**假失败**：main 停机先 cancel ctx 再 Stop，classifyTransportError 将 context.Canceled 也归为 `"timeout"`，该轮写进指标失败计数 + history 假行 + streak+1。量级极小 | cmd/gateway-v2/main.go:1163-1171；internal/mockprobe/client.go:131-135,233-239 | classifyTransportError 区分 Canceled【登记】 |
| 6 | P3/info | F8 host-precise 收窄有**识别面收窄副作用**：官方域合法子域（如 `gateway.anthropic.com`）现在走回退。唯一调用方是 admin/providers.go:869 的"推荐协议"展示，非数据面 | provider/catalog/protocol_normalize.go:138-209；admin/providers.go:869 | 如需覆盖官方子域加白名单后缀匹配【登记】 |

无 P1：mock 通道全生命周期未发现任何污染生产数据结构的路径。

## 二、核实为健康的面

1. **Mock 通道与真实凭据/计费全隔离**：探测生命周期 = runner（2×2 顺序）→ loopback HTTP（恒钳 127.0.0.1）→ auth.MockEndpoint 守卫 → 进程内 mock handler（纯内存、零 DB）。落地面仅 `mock_probe_*` 指标与 `mock_probe_history` 表。不触 providers/api_keys、不进凭据健康度/熔断、不进 usage_facts/request_logs、无 sticky。**五不**关闭态逐条证实 + e2e 钉桩（关闭 404、错 token 401、GET 405、非 mock 路径不旁路）。
2. **生产零装配**：cmd/gateway 全目录 grep `mockprobe|providers/mock` 零命中，与 README 声明一致；旁路需 enabled + `/mock/` 前缀 + 精确 Bearer token 三重条件。
3. **ollama-native"executor 框架"实为 IR 层框架**：FF_OLLAMA_NATIVE/P4 dispatch 接线与 P5 Passthrough 明确 deferred；SerializeOllama/Parse* 全仓零非测试调用方；错误侧铺垫就位（200+error 体返回 `*StreamError{Type:"upstream_error"}` 且有 Error() 方法可 errors.As）。
4. **empty-stream gate finish_reason 修复确已落地**：gate flush 循环 finish_reason 上提在位（stream.go:1091-1104），benign-EOF 判据含 sawSemanticOutput；钉桩测试在位；空流门全仓唯一调用点即 chat 路径。
5. **origin 打标端到端成立**：self_check_worker 请求头 → origin_mw trustedOriginOwners → request_logs.origin_stage → stats TrafficSelfCheck → usage_facts.traffic_class → reportrollup internal 三 scope 显式 `AND traffic_class='business'`。业务/内部计费口径剥离成立；daily_* 含全部流量类是显式声明的设计双口径。mockprobe 探测流量不进 usage_facts。
6. **.env.example 对账**：4/4 精确匹配，注释态默认安全（enabled=false、hide=true、interval 钳制）。
7. **IR 横向不变量**：窗口内 ir 改动 = ollama 新增面（隔离）+ anomaly dedup 清扫时间闸（性能修正，锁内读写，测试钉桩充分）。既有 OpenAI/Anthropic 转换器、多轮 message 序、tool_use.input 红线、usage 字段、流式/非流式双模式均未被触碰。

## 三、未覆盖项与原因

1. migrations/036 与 V800 的 SQL 实跑（禁连库）。
2. ollama 真机 wire 行为最终裁决需活体抓包（发现#1）。
3. gateway-v2 非窗口内既有面与 mock-probe v1 历史文档仅定性。
4. web 前端对账报表页内部归 T1。
