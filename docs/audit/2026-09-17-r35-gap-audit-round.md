# R35 — 补面审计轮（2026-09-17，接 R34）

- 背景：R34 完成验证器裁定五个维度缺审计证据：三层缓存 provenance 链、超长会话压缩与供应商超长保连重试、海外代理（订阅/探测/香港规避/地域优先级）、上传文件解析与版本管理、冗余代码标注登记
- 方法：5 路子代理并行（sanitizer/compression provenance、压缩与重试、proxy 子系统、attachments、冗余登记），每条 P0/P1 经主代理亲读复核后处置

## 一、本批修复（提交见 git log，P0×2/P1×3/P2×4 + 标注）

### P0

| # | 发现 | 处置 |
|---|------|------|
| P0-1 | **provenance 写端断裂**：SanitizedMessageRefs/AlignmentMap 全链只有读端没有写端——压缩器产出只落 V1 Redis（"algn"/v8 字段）与 30min result memo，从不进 entry.CompressionMeta；sessionv2mirror 白名单分支是死分支；V2 模式（sessions_v2_compression_read 默认 true）下 skipV1Cache 连 V1 都不写 → sessions_v2 metadata 的 provenance 读路径（applyCompressionMeta/loadV2CompressionState/validatePersistedProvenance）永远读空 | handler scResult 块新增 buildOutboundProvenance 写透：alignment_map（256 条封顶+truncated 旗标）+ sanitize_message_refs（同帽）+ window_source 计数（retained/summary/dropped——即 provider-window source telemetry 的来源构成面）；mergeCompressionMetaV3 折入 compression_meta（镜像白名单键已就绪，接上即通）；累计 >128KB 时降级为仅计数器 |
| P0-2 | **sanitizer 跨进程 offset 非原子**：HGETALL→本地累加→HSET 绝对值覆盖，双实例读同基线发同号占位符映射不同明文，还原拦截器可能把 A 的敏感值还给 B 的占位符（现实存在的间接泄漏） | 新增 acquireOffsetsLock：session 级 Redis 锁（SET NX PX 5s + token 比对删除 Lua），串行化 load→assign→save 临界区；Redis 不可用/争抢超预算时退化为无锁（=旧行为）不阻塞热路径 |

### P1

| # | 发现 | 处置 |
|---|------|------|
| P1-1 | Zhipu `finish_reason=model_context_window_exceeded`（流中）Resumable 硬编码 false → 零输出也直接终态，不透明重试不触发压缩 | 改 `!attemptHasClientSemanticOutput(gate, chunkCount)`（与 anthropic_bridge 空响应/error 事件分支口径一致） |
| P1-2 | 附件跨租户：`/api/logs/{id}/attachments` 仅按 request_id 查询无租户谓词（对照 audit-data-closure-2 已给 admin 端点统一加 scope，此端点漏网），tenant_admin 可枚举任意请求附件元数据（含下载路径） | ListByRequest 加可变租户参数，admin 包装层对 tenant_admin 传 GetTenantID(r)；下载端点归属校验仍开放（见遗留#4） |
| P1-3 | proxy 节点级 banned_regions 在每次 1h 订阅刷新时被 DELETE+重插清空——地区规避最细粒度配置不可持久 | 刷新事务内先按节点名捕获既有 bans（loadNodeBansBySubscription），重插时解析产物为空则回放；另：订阅创建入口默认 `banned_regions=["HK"]`（海外供应商统一屏蔽香港，新订阅不得从"全地域"起步，运营者可改） |

### P2

| # | 发现 | 处置 |
|---|------|------|
| P2-1 | 超长错误识别缺口："prompt too long"（无 is）、"context length limit exceeded"、`{"code":"request_too_large"}` 400 形态落入 KindTransient → 同一超大 body 跨凭据重发到 attempt cap | contextLengthRe 补三变体（含 code 形态） |
| P2-2 | 订阅拉取不走代理（默认 client 无 Proxy）——机场域名被墙则子系统整体失效 | parser 默认 client 补 http.ProxyFromEnvironment |
| P2-3 | 附件存储失败默认整请求 503（LLM_GATEWAY_ATTACHMENT_STRICT 非"0"即 strict），与存储层声明契约"存储失败不应阻塞转发"冲突，磁盘满=全部带图请求不可用 | 默认翻转为宽松（仅记 store_failed 元数据继续转发），strict 改显式 "1" opt-in；钉桩测试按新契约改写 |
| P2-4 | 镜像白名单丢 alignment 的 occurrence/target_kind/target_space——即使 P0-1 接通，同 hash 消歧与目标语义在持久层丢失 | safeAlignmentRecords 补三可选字段（有界校验），新增 boundedCompressionWord |

## 二、核实为健康

- cut_marker/pre_sanitize_offset_range 三层 offset 链路设计完整且带恢复校验；脱敏引用 hash-only 无明文回链；DB sink AES-GCM+降级语义清晰；跨租户隔离与 WRONGTYPE/offset 过期恢复防御到位
- 超长处置主链路（首字节前 4xx）：候选内 tiered 压缩（limit discovery→smart window→strategy runner→机械裁剪→Memora→LLM 摘要）→ 同凭据同连接重试，预算豁免、60s 互斥、结果 memo、NeverWorse、无跨候选污染、keepalive+: thinking: 保活——实现完整优雅
- 代理子系统并发/生命周期（lifecycleMu+wg、探活持锁、ForceSwap 连接失效语义）、凭据脱敏、Grafana+Prometheus 告警配套
- 附件：内容寻址+原子写+SHA256 去重、路径穿越防护、SVG/nosniff 强制下载、629/632 审计台账

## 三、遗留登记（P0/P1 级，均经亲读确认）

1. **P0（架构）| 代理订阅子系统与数据面脱钩**：真实出站由 upstream.ProxyResolver（HTTP(S)_PROXY env+国内白名单）决定；proxy.Manager 的订阅/探测/ForceSwap 只被 admin 面与免费池探测调用——`RequiresProxy`/`egress_profile`/`proxy_subscription_id`/`requires_proxy` 零读取方，"自动节点切换对真实流量无效"。需要产品级设计：按凭据/供应商把 GetProxyTransport 接入 upstream client Transport 池
2. **P1 | survival 层对 KindContextLength 一律 FailTerminal**（attempt_outcome.go:193、action_policy.go:243）：5h 连接保持预算对超长错误完全不工作；建议 ladder 中为 uncommitted attempt 加一次 compress-and-retry
3. **P1 | "节点地域优先级"未实现**：Subscription.Priority 仅展示；SelectNodeWithLocation/SelectNodeWithStrategy 零调用（location_affinity/5 种 LB 策略数据面不可达）
4. **P1 | 附件下载端点无归属校验**（domains/attachments/handler.go ServeHTTP，AuthContext.TenantID 只留 TODO）；MM-1 出站 URL 模式指向 admin JWT 路由=供应商拉取必 401（默认 OFF 埋雷）；FS 清理/水位 worker 不查引用会产生悬空引用且水位 worker 不写 632 台账
5. **P2 | 预压缩覆盖面**：Responses 协议整段跳过会话压缩；无 X-Gw-Session-Id 客户端无会话级预压缩；token 估算 bytes/3.5 对 CJK 低估 25-40%、多模态按原始字节计；base64 可进摘要器输入
6. **P2 | 死指标**：compression_triggered_total/latency/ratio 的 RecordOutcome 生产零调用——透明压缩重试在 Prometheus 不可见（window_source 计数落 compression_meta 后可 SQL 复盘，但 Prometheus 面仍缺）
7. **P2 | ProxyResolver 代理故障静默直连且失败计入凭据健康**（proxyconnect tcp 错误→KindNetwork→RecordFailure）
8. **P2 | 其余**：bans 缓存无 TTL 多副本不一致；刷新/探活间隔硬编码；sanitize 覆盖缺口（multimodal content 数组/顶层 system/tool 消息不脱敏）；injectPlaceholderProtection 死链未接线；状态门顺序（contentFilter 先于 contextLength 的双信号吞并）

## 四、冗余代码登记（R35-DEBT，37 条）

子代理逐条 grep 复核（区分零调用/仅测试/死链），完整清单见子代理报告存档；本批动作：
- **文件头标注 ×6**（UNUSED R35-DEBT）：admin/analytics_cache.go（254 行死文件）、bg/session_lifecycle_worker.go（576 行）、bg/model_config_validator.go（160 行）、domains/streaming/durable_contract.go（195 行 dormant 特性）、domains/streaming/action_bridge.go（450 行声明未赋值链）、domains/streaming/attempt_gate_context.go（28 行）
- **已核实的低风险删除候选（22 条，未删待批）**：injectPlaceholderProtection 双函数死链、CompareWithRawLog、UsesToolCallID、MaxBodySize、ReplaceModelIn{Request,Response}Body、ExtractFinishReason、InjectStreamOptions、IsDoubaoCatalog、WrapQualityProcessStreamLine、field_state 三函数、connection_monitor 四 option、dashboard_degrade 两函数、session_online_pagination 三函数、sql_validator 三函数、live_stream_sse QueryChildRequests、cost_reconciliation/taxonomy 旧构造器、EnsureSystemAPIKeyFromEnv、TrimOnceDB、balance_quota_probe.go:539-695 注释块
- **中风险待产品决策（13 条）**：Bandit 死链整体删除、session_lifecycle/autoheal 死链、临时调试端点 trigger-snapshot、cmd/gateway-v2 双维护、admin 四处 ensureFreeCredential 复制粘贴合并、handler.go(9087 行)/main.go(7409 行)/db.go(6226 行)/routing.go(5663 行) 超大文件拆分

## 五、测试与验证

```
go build ./... ; go vet（六个触碰包）          # clean
go test security/sanitize proxy domains/attachments internal/sessionv2mirror errorsx admin domains/streaming -count=1  # 全绿（含 strict 翻转钉桩改写）
```

## 六、下一轮提示词（建议）

> 以 docs/audit/2026-09-17-r35-gap-audit-round.md §三 遗留为起点：首位 #1 代理子系统接入数据面的产品设计（egress_profile→Transport 池，注意 KeepAlive 连接在 ForceSwap 时的失效语义与熔断统计归因），其次 #2 survival 层 context_length 一次压缩重试阶梯、#4 附件下载归属校验+MM-1 签名 URL；R34 遗留的 01-schema 列型漂移仍是全新安装首位阻断，两清单并行推进。冗余清理批可从 §四 22 条低风险删除候选起步（逐条编译+测试）。动 sanitizer/压缩链前先读 docs/audit/playbook/domains/D03-three-tier-cache.md 与 D05。
