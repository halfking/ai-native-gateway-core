# D08 — 供应商错误链与凭据服务质量

> 领域编号: D08 ｜ 最近更新: 2026-09-17 (初版) ｜ 状态: v1

## 1. 领域边界

**管**：连接供应商端请求的错误处理逻辑全链：错误记录（supplier_errors 错误表族）、有备援时以 think 或类似不影响会话的模式回传客户端且不中断请求流程、错误按凭据聚合呈现（凭据详情页）、凭据服务质量评估；熔断（breaker）行为。
**不管**：overflow 族的压缩重试语义（D05）；错误分类对 survival 的路由影响（D04/D02 交界，本域管分类学本身）；节点探测（D09）。

## 2. 参考基线

设计文档：
- `docs/03-design/02-feature-design/design/vendor-credential-error-detail/`（00-code-context / 01-logic-points / 02-LP1~LP5 / 03-drift-check）
- `docs/design/provider-quality-failover/`（00/01）
- `docs/design/credential-monitor-heatmap-requirements.md` + `credential-monitor-heatmap-implementation.md`
- `docs/03-design/FEATURE-REQ-credential-heatmap-routing-log.md`
- `docs/03-design/02-feature-design/design/adaptive-timeout-strategy.md`、`timeout-retry-optimization/00-design-spec.md`
- `docs/error-handling-improvements.md`、`docs/error-analysis-and-fixes.md`

代码入口：
- `errorsx/`（ClassifyError/ClassifyResponseBody 分类学）
- `credentialhealth/`、`credentialfpslot/`、`domains/credential/`、`domains/credentialstate/`
- supplier_errors 表族：`sql/migrations/startup/703_supplier_errors_promote_timezone_pin.sql` 等
- web 凭据详情：ErrorDetailTab（error_summary/recent_failures/quality_scores_7d）
- 熔断：breaker（free/paid 双 profile）

## 3. 检查清单

1. **错误必落账**：所有供应商端错误（含窗口内新增供应商/协议路径）进入 supplier_errors_hot → 8h promote（V371 基准）→ 历史 TTL 90d；无"分类失败即丢弃"路径。
2. **think 模式回传**：有备援可切换时，供应商错误以 thinking/备注通道带给客户端、**不中断流**、错误文本不泄漏内部细节（脱敏双道基准）；无备援时以 kind 化信封终态。窗口内新增错误路径两种去向都有测试。
3. **凭据聚合呈现**：错误按凭据聚合（错误集合挂在凭据详情下，或会话内关联），质量分（7d）计算口径与展示一致；用于评估供应商服务质量的数据面完整。
4. **熔断曲线**：指数退避真指数（paid 30s→…→30min 封顶 / free 15s→…→5min 封顶）、RecordSuccess 重置、cycle≥5 告警；legacy 协议路径计入 free 画像；窗口内新增失败路径接入 RecordFailureWithBillingMode。
5. **分类守卫**：名词窄化（function 类只在有 status 门路径判 model_not_found）不回退；isGenericWebBody 守卫在位。
6. **免费/付费分叉**：billing mode 缺省语义有文档契约（COALESCE 活路径不可达即钉扎），新增 SQL 不引入空串静默回退。

## 4. 历史回归点（轮末回注区）

- [R30] errorsx `function` 名词无 status 门误判 model_not_found → survival 硬终+绑定 5min 冷却 — 修复 e784481af；modelNotFoundWrappedRe + 回归×2
- [R31] 熔断升级分支不递增 coolingCycle（free 恒 15s 平铺、日志"exponential"不实）— 修复 01350f089；曲线钉桩测试重写
- [R31] 免费档熔断画像漏 legacy 协议路径；CloseProbe/ProbeCheck 死接缝删除 — 修复 ceddf5438
- [R30] supplier_errors TTL 90d — 修复 f19ba5d5a；hot 8h promote（V371）+ ErrorDetailTab 双道脱敏为健康面基准

- **R44 | 分析面错误词表 SSOT = errorsx.ErrorKind**：SQL/看板手写 error_kind 字面量前先对 errorsx/classify.go——quota_exceeded/invalid_auth/service_unavailable 均为虚构值（R44 两个分析模板因此 auth/quota 告警全失效，大面积 401 也不判 critical）。凭据维度错误聚合读 supplier_errors_unified（V371，hot∪分区），模板见 credential_health_check.sql §2。

## 5. 子代理派发提示词

```text
你是 D08（供应商错误链与凭据服务质量）只读审计子代理。工作目录：本仓库根。
第一步：Read docs/audit/playbook/conventions.md 和 docs/audit/playbook/domains/D08-provider-errors.md 全文。
第二步：按域文档 §3 检查清单逐条核对，审计窗口：<窗口>；改动文件清单：<该域相关子集>。
重点：窗口内新增的供应商错误路径是否"落账+think 回传/信封终态"两去向齐全；熔断画像是否覆盖。
只读不改。输出按 conventions.md §4 结构，每条发现带 file:line 与触发路径。
```

### R42 回注（2026-09-18，721 manual 保护锁死 + 失败落账补齐）
- **共享谓词常量取代"keep in sync"注释**：manual-24h 保护谓词现为 `bg/balance_manual_guard.go manualBalanceGuardSQL`，floor guard（Pass A SELECT/成功 UPDATE/失败戳 UPDATE ×3）与 probe_v2（成功 UPDATE/失败戳 ×2）五处消费，`TestManualBalanceGuardPredicateLockstep` 锁消费计数并禁手抄字面量回潮。新增余额写点必须消费同一常量。
- **TOCTOU 定式**：候选 SELECT 带保护谓词 ≠ 写回受保护——写时 UPDATE 必须带同一谓词（探测飞行中操作员 PATCH 是真实场景）。
- **后台探测失败路径写 balance_error**（共享 balanceProbeFailStamp()，不含厂商响应体），不动 checked_at（保 #12a 退避）；manual 行由谓词豁免。
- **manual 戳只随值变化**：updateCredential 对 balance_usd 用 IS DISTINCT FROM 条件盖戳——表单恒携带该字段的环境里，无条件戳 = 任意编辑静默停摆自动探测 24h。

### R43 回注（2026-09-18，符号名更正 + refresh-balance 失败语义统一）
- 更正本域 R42 回注的符号名（merge 去重后 R42 子代理侧实现被淘汰）：manual 保护谓词 = `bg/balance_manual_protection.go ManualBalanceProtectionPredicate`（非 balance_manual_guard.go/manualBalanceGuardSQL）；守卫测试 = `TestBalanceManualProtectionContent/IsWired` + `TestManualBalancePredicateWriteTimeCoverage`（非 TestManualBalanceGuardPredicateLockstep）。消费计数 3+2 不变。
- **失败路径不动 checked_at 现为三处统一契约**（floor guard/probe_v2/⟳ refresh-balance）：`balance_source='manual' AND checked_at > NOW()-24h` 保护窗下，失败 bump = 每次点击续期 = 自动探测永久挂起。新失败路径一律只写 balance_error。
- 回注教训：**merge 后域文档回注要先 git ls-tree 验证符号存活**——回注指向已删除符号，下轮代理按文档找错文件（R43 实际发生）。

### R45 回注（2026-09-19）
- supplier_errors 聚合读端的 stage 列空串占比 90%+（真库 24h 1206/1333）：`dominant_stage` 类聚合必须 `COALESCE(NULLIF(stage,''),'unknown')`（vendor_credential_error_handlers.go 读端约定），裸 MODE 恒输出空串。分组聚合须带 other_count 使分项与总数的缺口可见（真库 158 错误只有 8 个落在四类分项）。
