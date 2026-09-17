# D08 供应商错误处理/凭据服务质量 子代理报告(窗口:0a015af51^..b75c91900)

窗口内该域 commit:c66dbd6c9(errorsx MiniMax thinking.type 分类)、507d78cff(721 凭据余额元数据/刷新/手工保护)。

## 一、发现(候选,待主代理复核)

| # | 级别候选 | 发现 | 证据 file:line | 建议处置 |
|---|---|---|---|---|
| 1 | P2 | **manual 保护谓词双写点未锁死**(交接遗留#3):两处谓词语义当前一致,但为两个独立 SQL 字符串字面量,靠注释人工维护,无共享常量、无守卫测试。且两处防护强度不对等:floor guard 只在 Pass A SELECT 处防护(refreshBalance 的 UPDATE 无 manual 谓词),probe_v2 在 UPDATE WHERE 处防护——TOCTOU | bg/balance_floor_guard.go:857-860(SELECT)、:1001-1008(UPDATE 无谓词)、bg/credential_probe_v2.go:586-590(UPDATE 谓词)、同步注释 :575-577 | 抽共享常量+守卫测试;floor guard 的 UPDATE 补同款 WHERE |
| 2 | P2 | **floor guard / probe_v2 余额探测失败路径可观测性黑洞**(交接遗留#3 承认项,核实属实):refreshBalance 的 !ok 分支裸 return false 连 slog 都没有;probe_v2 侧仅 slog.Debug。均不写 balance_error、不写 checked_at——厂商余额端点持续故障时 UI 只能看到 checked_at 变陈旧 | bg/balance_floor_guard.go:997-999、bg/credential_probe_v2.go:1871-1875;对照写错误的一侧 admin/provider_credential_balance.go:92-97 | 失败路径写 balance_error(不动 checked_at)+ floor guard 侧升 Warn |
| 3 | P3 | refresh-balance 失败路径刷新 balance_last_checked_at=NOW(),对 manual 行反复点击失败会无限顺延 24h 保护窗 | admin/provider_credential_balance.go:92-97 + bg/balance_floor_guard.go:859;契约注释 :27-30 | 失败路径不刷 manual 行的 checked_at |
| 4 | P3 | balance_error 内容信息量极低:FetchBalanceUSD 返回 (0,bool) 丢弃 HTTP 状态码与响应体——401/402/5xx 在 UI 不可区分 | internal/providercap/capability.go:182-209、admin/provider_credential_balance.go:91 | FetchBalanceUSD 增加错误详情返回 |
| 5 | P3 | failover_policy 新增 KindClientBug 分支是纯契约表修正、无生产调用方(DecideFailover 零外部引用);新分支与 L112-116 既有 case 逐字重复(可合并) | errorsx/failover_policy.go:117-128、:45、:112-116;真实消费方 domains/credentialstate/manager.go:282 等 | 按契约一致性修正登记;可顺手合并重复 case |
| 6 | P3 | ⟳ 刷新按钮对所有凭据渲染、无余额能力门控:zhipu/minimax/openrouter/Anthropic 等点击必得 400 | web/src/views/ProvidersView.vue:1379-1386、:543-545;internal/providercap/capability.go:56-70 | 按能力隐藏/禁用按钮或 tooltip 声明 |
| 7 | P3 | docs/balance-query-optimization.md §2.4 伪代码与实际实现漂移(30s ctx vs 12s;列名 balance_checked_at vs balance_last_checked_at;写 balance_currency 未实现;落点文件不符;缺 400 分支) | docs/balance-query-optimization.md:419-500 vs admin/provider_credential_balance.go:42,70-74,108-129 | §2.4 加设计稿标注或更新为实码 |
| 8 | P3 | 分类标签形态分叉:MiniMax base_resp 信封 2013 会先被 toolCallIdMismatchRe 截为 KindToolCallIdMismatch(同 IsClientBug 家族,仅 error_type 标签不同) | errorsx/classify.go:678 先于 :1033;:1452-1460 | 可接受;补钉桩测试 |
| 9 | P3(流程) | 迁移 721 仅本地 PG 事务内验证后回滚,未按迁移三纪律#1 在存量真库实跑定稿(风险低,纪律点留给主代理) | sql/migrations/startup/721_...sql:22-35 | 主代理在存量真库核验(R40 已实跑,本轮确认 marker 齐) |

## 二、核实为健康的面

- **MiniMax invalid thinking.type → KindClientBug 分类正确且不影响其它厂商**:两个新 pattern 窄匹配+反向断言测试;400/422 status 门在位;逐一体检前置分类器均不拦截生产实测 body。
- **降温/冷却语义全链一致**:manager.UpdateOnFailure 首行跳过、breaker.RecordFailure 跳过、terminalActionKind 终态、nodehealth 不污染、failover 表行 EnqueueProbe=false+测试锁定。
- **错误必落账未被分类修正破坏**:persistSupplierError 无 kind 过滤,client bug 仍进 supplier_errors_hot → unified,凭据详情按 credential_id 聚合。
- **721 迁移本体健康**:纯 ADD COLUMN IF NOT EXISTS、CHECK 幂等守卫、installer embeddata 副本逐字节一致。
- **balance 写入点清点完备**:全仓恰好 4 个写者(PATCH manual / refresh-balance / floor guard / probe_v2),无第 5 个未防护写者。
- **refresh-balance 三分支**:400 配置事实不写 balance_error;失败 fail-open 保留旧值+写错误戳;成功清 error;超时双保险;truncateBalanceError rune 边界有钉桩。
- **listCredentials 四个新字段端到端对齐**:SQL/scan/struct → TS → UI。

## 三、未覆盖项与原因

721 存量真库实跑(留给主代理,R40 已实跑);前端 vue-tsc/vite build 未重跑(交接文档声明绿);D05 交界流式行为未重验;§2.6-2.8 未逐行比对(文档自标未实施);721 缺 .down 是否违约(混合惯例)。
