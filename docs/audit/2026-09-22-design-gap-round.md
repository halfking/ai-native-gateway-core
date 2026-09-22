# 设计差距审计修复轮·四波总台账（Wave 1-4 收官）

- 日期：2026-09-22（Wave 4 债务清理 + 四波总收口；Wave 1-3 于同日完成）
- 依据：《llm-gateway-项目审计与下一步优化方案.md》§2 A类 / §4 C类 / §3 B类 / §5 D类 / §6 四波推进；配套《llm-gateway-系统功能特性及流程.md》v1.1、《llm-gateway-全方面测试方案.md》
- 分支纪律：`audit/wave1-p0-20260921`，每逻辑单元精确路径提交，收口后统一推送，合并 main 等指令。
- 并行说明：Wave 3 首段（B3/B4/B5）与第二/三段（B1/B2/B5尾巴/B7/B8）由并行会话在同一分支推进，本台账合并归档。

---

## 一、A 类：设计红线违背（Wave 1，5/5 完成）

| # | 项 | 状态 | 提交 | 关键行为 |
|---|---|---|---|---|
| A1 | resolve 与真实选路不同源 | ✅ | 659324c93 | resolve 调 `Router.PlanCandidatesPinned` 产出真实 plan_order（persist_probe=1 同参），消灭"resolve 可用但请求无节点" |
| A2 | tool_use.input 非法 JSON 字符串直塞 | ✅ | 0fc829916 | 非对象 JSON 包裹 `{"raw":...}` + format-anomaly；四协议往返单测 |
| A3 | 非 default 租户 3 天窗口仅前端 | ✅ | 2de612429 | 后端 72h clamp（listLogs/stats 入口），直连 API 无法绕过 |
| A4 | 429 计入熔断 | ✅ | 83567d0ca | KindRateLimit 剔除断路器失败计数；保留 writer 3min 绑定排序降权；裁决落 docs |
| A5 | goal 入口契约与影子轮污染 | ✅ | 4f88f1861 | `X-Gw-Goal-Mode: managed` header 并存；client-signal gw-continue 默认开；legacy 影子轮不进拼装链 |

## 二、C 类：参数漂移裁决（Wave 2，12/12 裁决完成）

| # | 项 | 裁决 | 提交/证据 |
|---|---|---|---|
| C1 | MAX_PROMPT_TOKENS | 文档统一"2M=拒绝线；1M=压缩线" | 设计文档 v1.1 |
| C2 | 压缩利用率 | 双档 65%/50%，反向修订文档 | 设计文档 v1.1 |
| C3 | handoff 阈值 | 种子 180K→300K | 073722bfb |
| C4 | MNF 绑定冷却 | 三级化 30min/7d/30d | c4b503aeb |
| C5 | 失败阈值三套数 | settings 常量表集中化（streak=3/冷却 5min/sticky=2） | 3917dd747 |
| C6 | 探测节奏 | 现链更精细，反向修订文档+档位进 settings | 3917dd747 + v1.1 |
| C7 | 超时默认 | 运行调优值定版，反向修订文档 | 设计文档 v1.1 |
| C8 | fp slot 归还 | 常量表可配（默认 30min 维持） | 3917dd747 |
| C9 | hot 窗口 | settings 键，默认 8h | 3917dd747 |
| C10 | L0 成功率门 | 保留软降权（有意演进）；缓存 TTL 评估 | v1.1 + Wave2 记录 |
| C11 | 评分公式 | 加权惩罚和定版；MinStandardIQ/热门加权灰度评估（不翻开关） | beb8345ff |
| C12 | 恢复判据 | "直连成功即恢复"取舍文档化 | 设计文档 v1.1 |

设计文档 v1.1 反向修订（2026-09-22）完成：C1/C2/C4-C12 + D6 过时项（sessions.last_full_*、request_raw、promote 退役、泳道聚合、handoff 旋转语义）+ A4 裁决，§7 全局参数表与常量表数值一一对应。

## 三、B 类：功能补全（Wave 3，B1-B5/B7/B8 完成；B6/B9-B14 挂账）

| # | 项 | 状态 | 提交 | 备注 |
|---|---|---|---|---|
| B1 | 峰谷倍率体系 | ✅ | 39b8efbe0 | 迁移 736；时段表+Go/SQL 双侧同源取档+usage_ledger/request_logs 落 rate_multiplier |
| B2 | 节点状态老化+闪断双确认+探测并发 | ✅ | 6d3c8a28d | 2h 无证据转 suspect；2~5s 双确认；同凭据并发≤2 |
| B3 | 请求侧标准名自动补录 | ✅ | 0a2dfc3a4 | resolve miss 且同名 raw 存在时经 discovery 补录，5min 防抖；autoseed_test.go |
| B4 | 改密后自动注销 | ✅ | 7a2203223 | 吊销纪元（authRevocationProbe 三中间件）；前端登出；auth_revocation_test.go |
| B5 | 设置键组 | ✅ | c98b329a4 + f0a8d58ae | ①词典多语言键（zh/en/ja）②敏感词阈值键 ③"网关重试与超时"聚合组；error_probe 四键接线收尾 |
| B6 | 发行分发链三补 | ⏳ 挂账 | — | artifact 上传/catalog/设备上限默认 |
| B7 | 看板 IP 地域归类 | ✅ | c356ade12 | 内网直显/外网段表/优雅降级 |
| B8 | 内部对账 job | ✅（并行会话收口中，迁移 737） | 638b1d41f | usage_ledger↔credit_ledger balance_after 链校验 |
| B9 | 压缩 fallback 同厂优先档 | ✅ | b9d18e060 | 摘要 fallback 链同厂优先重排 |
| B10 | 泳道空闲 1 分钟档 | ✅ | a99274148 | no_traffic_1min |
| B11 | providers official 标记列 | ⏳ 挂账 | — | 需迁移 738 |
| B12 | goal AUDIT 三轮+独立 VERIFY | ✅ | 8e0b51bc4 | audit_multi_round_test.go ×3（三轮+verify 全流程/提前通过终态/legacy store 单轮不变） |
| B13 | Responses 上游 Parse+completed 回带 | ⏳ 挂账 | — | 撞 D2/D3 刚重构的 responses 桥，留 Wave 5 |
| B14 | 并发 slot 300s 硬顶 | ✅ | f82bb40da | lease deadline 看门狗；Released() 翻转钉桩（miniredis）+ 资格臂 ×2；指标 llmgw_fp_slot_lease_forced_release_total |

## 四、D 类：代码债清理（Wave 4，5/5 完成，本会话）

| # | 项 | 状态 | 提交 | 测试证据 |
|---|---|---|---|---|
| D1 | last_system_session 死索引接线 | ✅ | 62738c7e6 | session_assignment_redis_test.go ×4（命中跳过 DB/DeviceSeed 不匹配回 DB/窗口复核/死会话回 DB，miniredis）。**注：设计 v1.1 §264 本就要求读该索引——本项是让代码补上设计要求的读路径，非行为变更**；DeviceSeed 门为身份对齐守卫，拒绝即落回原 DB finder（权威路径不变） |
| D2 | 空响应判定统一 | ✅ | c4a7c98a7 | 新包 `internal/emptyoutcome`：`IsEmptyOutcome`（usage-only 不计为空）+ 各形态入口（ChatChunkHasOutput/ChatSemanticDelta/ClassifyBody/ChatBodyIsEmpty/AnthropicMessagesBodyIsEmpty/ResponsesOutputIsEmpty）；四处调用点收口；streaming/stream.go、empty_response.go 转薄委托；executors 副本删除（裁决改用 streaming 权威：全 choices+分类信封，R16 方向）；executors/empty_response_test.go 钉桩 ×14；四路径既有空流钉桩：gate（stream_early_empty_test.go）、anthropic_stream（EmptyStreamIsResumableFailover）、responses_bridge（EmptyResponseFailOver）、executors（本次新增） |
| D3 | 厂商错误通道单表 | ✅ | c4a7c98a7 | `errorsx/vendor_channels.go`：两档表（FinishReasonVendorFailureKind=GLM 失败通道 / FinishReasonAbnormalKind=异常完成态）+ MiniMaxBaseRespStatusCodeKind（自 vendorstrip 移入，vendorstrip 委托）；**拼写容错**（exeated/exeeded/exceded/sensetive/network_eror 精确变体+词干扫描；设计文档 §177 自身即含 "exeated" 错拼，容错表正好覆盖）；三处改引用（ir/response.go、anthropic_stream.go、responses_bridge.go）；**顺带修复 responses_bridge 缺 model_context_window_exceeded**——GLM 上下文溢出流不再误渲染 completed；errorsx/vendor_channels_test.go ×33 |
| D4 | usage 解析器统一 | ✅ | 47850df1e | usage.go 单表 usagePath 变体（prompt/completion/cache_read/cache_write 四槽，首中即胜）；流式 ExtractUsageFromChunk 与非流式 extractTokensFromResponseBody 同源 lookupUsageInt；各自兜底保留（非流式 MiniMax 顶层 usage+双向 total 推断；流式 reasoning/miss/seed）；usage_variants_test.go 全量回归（MiniMax 顶层/GLM details/Doubao miss/双提取器 parity） |
| D5 | 请求方向 parity golden | ✅ | b9aa351e8 | chat_to_anthropic_parity_test.go：10 例矩阵（system 字串/数组/多轮/多 tools/tool_choice/多模态 URI+base64/tool_result/thinking/无 max_tokens/工具透传）双实现对拍，golden 双侧入库 testdata/parity-golden/（20 文件）防回归 |

### D5 差异裁决记录（详见测试文件头注释）

By-design（接受）：
1. **max_tokens**：手写默认 4096；IR 按 parse 结果直发（缺省即 0）——责任分层不同（converter shim vs 管道层）。Anthropic 上游必填该字段，IR 路径客户端须自带。
2. **未知顶层字段**：IR lossless 保留（记 ir_unknown_field anomaly）；手写丢弃。
3. **reasoning_effort→thinking**：IR 独有能力。

新实锤 IR 三缺口（挂账，Wave 5 候选，手写侧行为为准）：
- **GAP-1**：`ParseOpenAI` system 数组 content 只保留首块（数据丢失）；手写侧换行 join 全部块。
- **GAP-2**：`SerializeAnthropic` 命名 tool_choice 序列化为裸字符串 `"function"`——非合法 Anthropic 形态（应为 `{"type":"tool","name":X}`）。
- **GAP-3**：空 assistant text 块随 tool_use 一并保留（`content:""`→`{"type":"text","text":""}`）——Anthropic 拒收空 text 块；手写侧抑制。

收敛裁决：**暂缓**收敛为 IR 薄包装。bridge 路径（vendorstrip 介入/Q2 gate）与 executor IR 路径 fallback 语义不同，且 IR 携带 GAP-1/2/3，今日换包装会在 bridge 上回归这三类形态；待缺口闭合并评估 fallback 兼容后再议。

---

## 五、回归总表（《llm-gateway-全方面测试方案.md》映射）

| T 域 | 包 | 结果 |
|---|---|---|
| T01 协议转换 | internal/ir、internal/vendorstrip、internal/emptyoutcome、domains/transformation/... | ok |
| T02 路由/resolve | resolve、domains/streaming/executors | ok |
| T05 探测/节点健康 | bg/... | ok |
| T06 熔断/凭据 | domains/credential | ok |
| T10 压缩 | domains/hooks/compression/... | ok |
| T12 goal | domains/hooks/goal/...、domains/goalintegration | ok |
| T14 计费 | maas | ok |
| T15 admin/租户 | admin、pkg/identity、security/sensitive | ok |
| T16 存储/观测 | domains/hooks/observability/...、domains/streaming | ok |
| T20 分发 | licensing、autoupdate | ok |

执行：26 包合并扫描无失败 + streaming(72s)/executors(34s)/admin(66s) 三大包独立复跑全绿；三门 `go build ./... && go vet ./... && go test <受影响域>` 每单元提交前全绿。deploy-local 冒烟见 §七。

## 六、设计文档核对

- v1.1（2026-09-22 反向修订版）版本头确认；§264 会话识别（D1 接线即该条要求的读路径）、§242 空响应 fail-over、§177 厂商错误通道（含 "exeated" 错拼，D3 容错表覆盖）与 Wave 4 代码无新漂移。
- D5 的 IR 三缺口建议随 v1.2 修订在"多原厂兼容"章节补记已知边界（暂不影响 v1.1 结论）。

## 七、部署冒烟

- Wave 3 会话同日多轮 deploy-local 冒烟 PASS（见其台账记录）；Wave 4 会话当时因并行部署竞争推迟的收口冒烟，已由收官会话于 2026-09-22 20:1x 补验：
  - 运行二进制 `2.5.6-f7f8f66d-20260922-2185`（8782 active-version 与字面量一致，f7f8f66d 之后仅 docs 提交，**覆盖 Wave1-4 全部代码提交**）；
  - `/healthz`：`status=ok ready=true`；
  - admin 登录 `POST /api/auth/token`：200 + access_token(289 字节)；错密码负例 401 拒绝（对照有效）。
- 顺带收口：`dl_load_project_env` 消费死路径修复（38814ddde）——c48651e58 的 `{ parser >/dev/null; env -0; }` 形态丢弃解析输出致 .env.local 零加载；红绿实证（HEAD 版 pw/DSN 双 UNSET）+ envload/bootretry/preflight 三锁定测试全绿；envload 锁定测试只盯 `dl_load_env_file` 未覆盖本函数系漏网根因。
- 顺带收口（main 合并轮，d0569d202）：deploy_local_contract_test duplicate-release 用例预存在红（期望 rc=1 实得 64）定性+修复——b071bb120（P1.1，09-10）自出生即红，非 R54/R55 回归（b071bb120 worktree 复跑实锤同红）：fixture 把 `AIAN_DEPLOY_LIB` 指向 `$ROOT/../../deploy-lib`（official-deploy 下不存在，真 SSOT 在 workspace/ai-native-tools，与仓库 `scripts/deploy-lib` 软链差一个层级），`_shared-lib.sh` 对"已设置但缺失"fail-closed exit 64，撞车分支根本未执行。修法：fixture 改经仓库自身软链 `pwd -P` 解析，与生产消费路径同源、与 checkout 层级无关；全文件复跑 26 PASS / 0 FAIL / RC=0。

## 八、挂账清单（下一轮入口）

1. **B6/B11/B13**（Wave 3 择机项余量；B9/B10/B12/B14 已于本日落库 b9d18e060/a99274148/8e0b51bc4/f82bb40da）：artifact 上传+catalog+设备上限默认 2、providers official 标记列（需迁移 738）、Responses 上游方向 Parse+completed 回带。
2. **IR 请求方向三缺口**（D5 实锤，Wave 5 优先）：GAP-1 system 数组丢块 / GAP-2 命名 tool_choice 非法序列化 / GAP-3 空 text 块；闭合后重估 chat_to_anthropic 收敛为 IR 薄包装。
3. **IR SerializeAnthropic max_tokens=0 直发**：管道层默认值责任归属待裁决（by-design 记录在案）。
4. **下一轮**：挂账清单 + 源文档《llm-gateway修正》未覆盖项的常规 48h 审计轮（docs/audit/playbook/orchestrator-prompt.md）。
