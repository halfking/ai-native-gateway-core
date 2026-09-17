# R36 补位轮 — 冗余清理 + 冻结表守卫 + 代理数据面设计（2026-09-17）

- 定位：R36 24h 审计轮（`2026-09-17-r36-24h-audit-round.md`，并行会话）推送后的**接力轮**。会话启动时 ff 对账至 67f78247c，发现原 R36 待办清单首位三项（01-schema 列型漂移 717 / survival ctxlen 压缩重试 / 附件归属校验）已被并行会话闭环——抽检属实（717 迁移+双守卫测试、survival_coordinator.go:491/748/1021、admin/attachments_routes.go fail-closed 联查）后**主动撤并不叠加**，按其 §六 指向重新圈定范围。
- 方法：主代理亲读逐点复核（死代码逐符号全仓 grep 计数、A-3 六点逐文件亲读），红绿验证，分批 staging；每次动手前 fetch 对账（本轮 origin 先后推进 6+1+1+3 提交，均零重叠 ff/merge 收敛）。

## 一、本批修复（4 提交，均已推 origin/main）

### 1. 冗余清理批（6f53b7bf9）——R35-gap §四 22 条低风险删除候选收口

逐条 git grep 复核零生产调用后物理删除 **19 组符号、25 文件 +89/−1111**。R35 只标注未删，本批逐条编译+测试后删：

- **bg**：TrimOnceDB（sqlmock 测试共删）、EnsureSystemAPIKeyFromEnv、InitTaxonomySync+DiscoverYamlPath（连带）、NewCostReconciliationWorker、balance_quota_probe.go 远端参考注释块 160 行（文件头 MERGE AUDIT NOTE 改写为指向 git 历史）
- **admin**：ValidateFilterColumn+SanitizeOrderBy（+孤儿 Filter 白名单数据）、WriteMissingRelationPayload+WriteMissingRelationOrError+MissingRelationPayload、ParseCursor+EncodeCursor+BuildPaginationResponse（**分页是完整接线的**——活链 buildOnlineSessionPaginationResponse(session_online.go:179)与私有编解码保留，死的只是三个导出薄包装，与 R35"三函数"吻合）、QueryChildRequests
- **streaming**：MaxBodySize、ReplaceModelIn{Request,Response}Body、ExtractFinishReason+InjectStreamOptions（+relayMustMarshal 孤儿连带）、IsDoubaoCatalog、WrapQualityProcessStreamLine、WithConnectionMonitorClock
- **ir/sanitize**：UsesToolCallID、CompareWithRawLog、injectPlaceholderProtection+InjectPlaceholderProtection+PlaceholderProtectionPrompt（保留 IsSanitizeSystemPromptEnabled 门控读取器供未来接线，文件头历史注记说明）

**R35 清单口径修正（以亲读复核为准）**：
- field_state"三函数"实为**两函数**：GetFieldState 被存活的 ValidateNonEmptyArray 引用；ValidateNonEmptyArray/HasUserMessage 被 handler.go:2400/2416 生产调用
- connection_monitor"四 option"仅 Clock 全死；Interval/IdleTimeout/Probe 是**测试钉桩 seam**（tests 同包但改直改未导出字段有与 run() goroutine 的数据竞争），保留并登记
- 测试同步改写：分页三包装的 15+ 处测试引用改测私有等价实现（HMAC 篡改/无签名拒绝/secret 校验覆盖不变）；field_state 删两测

### 2. 716 down 补体 + 双通道全文等价守卫（b756ea861）——R36 遗留#4（A-1/A-2）

- **A-1**：down 补 v_model_priority_details / v_model_availability_timeline / get_model_state_summary 三份旧体（取自 716 父提交 313d1ebc8^ 的 ensure 内嵌 SQL 原文）；v_node_probe_state_compat 保持 DROP（716 新增对象，down 恢复缺省形态）
- **A-2**：db.go 内嵌 SQL（338 行）提取为 `probeHealthDashboardViewsSQL()`（ensure 调用点行为不变）；新增 `TestProbeHealthDashboardViewsSQL_MigrationSync` 剥注释+折叠空白**全文逐字节比对**——**一次通过，实证两通道当前零漂移**；`TestProbeHealthDashboardViewsDown_CoversAllObjects` 钉住 down 必须 6 重建+1 DROP，防半回滚复发
- 双门禁（installer canonical + apply-db-revision-sequence）通过

### 3. 冻结表守卫按模式选源 + reviver 门控（86e09daa7）——R36 遗留#3 + 遗留#5（A-3）

- **新叶包 internal/probemode**：USE_NEW_PROBE_MODE 开关 + `GuardStateTable()`（活动探测系统的 (credential,model,state) 判定表）唯一事实源；bg 委托之
- **遗留#3**：BrokenProbeReviver 新模式下 no-op（冻结 broken_confirmed→recovering 会溶解 recovery 全断守卫，把新系统证死的模型放回池；新系统 paused 行由下次真实请求 Submit re-arm，node_probe.go:24 过时头注释一并修正）；credential_recovery availability 守卫源改模式感知（新=compat 投影，解除冻结行把凭据永久钉 suspended 的反向卡死）
- **遗留#5（A-3 六点逐文件亲读）**：
  - **修复**：provider/client.go:1586/1657 候选 SQL broken NOT EXISTS 过滤改 `brokenPairExcludeSQL`（模式感知；helper 用 strings.Builder 组装——反引号奇偶会干扰 sql_comment_syntax_test 的 raw-string 扫描器，首版被其拦截后重写）；:2368 suspicious-exit 死写加门控（保留指标）；credentialhealth/checker.go:642 RecoverExpired 同型守卫同步改
  - **核实健康不动**：domains/credentialstate/cache.go（node_probe_state 优先+legacy fallback 分层）、admin/routing.go force_enable（node_probe_state+model_probe_state 双清样板本体）

### 4. 代理数据面接入设计案（docs/03-design/proxy-dataplane-integration-design.md）——R35-gap §三#1 / R36 遗留#1

架构级 P0 的**设计稿**（行为大变更，按交接纪律待产品决策后实施）：分流 RoundTripper 方案（upstream 单 Transport 汇聚点前插一层，per-订阅连接池分片）、KindProxy 归因（闭环 R35-gap #7 proxyconnect 不计凭据健康）、ForceSwap keep-alive 尾巴语义（接受 90s IdleConnTimeout 渐变，强断留待风控证明必要）、阶段 0 观测/1 免费池/2 全量分期、4 个产品决策点。事实基线复核：GetProxyTransportForNode 唯一生产调用方=免费池探测（admin/free_pool_extra.go:938），egress_profile/RequiresProxy/proxy_subscription_id 零数据面读者——R35 登记属实。

## 二、测试与验证（真实实测）

```
go build ./...                                    # 每批清洁
双门禁：installer TestCanonicalStartupMigrations   # PASS
        apply-db-revision-sequence_test.sh        # passed
删除批：bg/sanitize/ir/cmd/admin/streaming 六包 -count=1 全绿（admin 66s/streaming 71s）
        删除符号 29 个全仓零残留（仅存历史注记注释）
716 批：go test ./db/ -count=1 绿；守卫一次通过=两通道零漂移实证
A-3 批：probemode/provider/credentialhealth/bg 四包 -count=1 全绿
        （TestEnabled_Parse/TestGuardStateTable_FollowsProbeMode/
         TestBrokenProbeReviver_SkipsUnderNewProbeMode|RevivesInLegacyMode/
         TestProbeGuardStateTable_FollowsProbeMode/TestBrokenPairExcludeSQL_FollowsProbeMode）
go vet（触碰包）清洁；gofmt（触碰文件）清零（admin 包 60 文件既有漂移不动）
```

## 三、遗留登记

1. **P2 | 717 部署前置**（承 R36-24h #2）：252 真库 information_schema 复核 → 跑 717 → dump-schema.sh 重导三 baseline 替换手工体。需运维窗口，本会话未触碰生产。
2. **P2 | 代理数据面实施**：设计案已落（§一.4），等 §五 4 个产品决策点；实施时按分期红绿基线。
3. **P3 | connection_monitor 三 option**（Interval/IdleTimeout/Probe）：保留为测试 seam，若要删除需先重构 NewConnectionMonitor 允许注入后启动。
4. **P3 | credential_recovery 与 checker.go 双守卫拷贝**：语义已对齐（同源表选择），两份 SQL 文本仍是手工同步，可后续提取共享。
5. 承继开放：credential_state_log 零读者（未决策）、R35-R1 X-Gw-* 信任头（前置拓扑核实）、MM-1 签名 URL 半边、R36-24h §四 #6 P3 批、R35-gap #3 节点地域优先级/#5 预压缩覆盖/#6 死指标/#8 其余。

## 四、下一轮提示词（建议）

> 以本文 §三 为起点：首位 **717 部署前置**（遗留#1，运维窗口：252 真库 psql 复核 information_schema.columns 十列现型 → 应用 717 → sql/scripts/dump-schema.sh 重导三 baseline，注意 252 docker 实为 podman、DDL 前先 df 查盘）；次位代理数据面阶段 0（KindProxy 归因，按设计案 §四 分期，动 upstream 前先读 upstream/client.go NewWithRetries 与 proxy/manager.go GetProxyTransportForNode）。产品决策点未决前勿实施阶段 1/2。冗余批：R35-gap §四 中风险 13 条待产品决策清单可开始逐条找 owner。动迁移前 git fetch 核对远端编号（当前已至 717）；触碰 installer 前跑双门禁。
