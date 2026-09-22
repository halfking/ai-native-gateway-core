# Wave 3：B 类功能补全 —— 轮次台账（2026-09-22）

分支 `audit/wave1-p0-20260921`（本会话推进至 a99274148 + 后续提交）；依据
《llm-gateway-项目审计与下一步优化方案.md》§3 B 类 + §6 Wave 3。
本会话与一并行会话共享工作树，任务分工以下表"执行方"标注。

## 逐项台账（改动 / 测试 / 风险）

### B3 请求侧标准名自动补录 —— 本会话，0a2dfc3a4
- 改动：resolve 全 miss 且 `provider_models` 存在同名 raw（lower/剥前缀比较）
  时经 `discovery.EnsureCanonicalAndAliases`（source=resolve_autoseed）补录
  标准名+别名后重查正缓存；同 cacheKey 5min 防抖；无 raw/防抖/失败走原
  负缓存。resolve→discovery 无 import 环（已核）。
- 测试：resolve 14 用例全绿（三类形态参数化、防抖、seed 失败回退、无 DB
  路径不变守卫）；discovery/modelname/modelcatalog 包回归绿。
- 风险：junk 名补录被 EnsureCanonicalAndAliases 内建置信匹配守卫兜住
  （disabled 行不复活）；真库对拍靠 T02 回归（本机 8782 冒烟通过）。

### B4 改密后自动注销 —— 本会话，7a2203223
- 改动：settings_kv 键 `admin.auth_revocations`（jsonb_set 原子推进纪元，
  零迁移）；AdminMiddleware（identity legacy+VerifyToken 双分支）、
  SuperAdmin、ProviderConsole 三处验证点比对 iat<纪元即 401；5s 进程内
  TTL 缓存、fail-open（settings_kv 故障不锁管理面）、>7 天纪元解析时丢弃；
  identity.LegacyClaims/Principal 补 IssuedAt 通道；前端改密成功即 logout。
- 测试：admin 全包绿（中间件旧 token 401/新 token 200、纪元解析过滤、
  fail-open、VerifyLegacy iat 传播）；测试钉住 LLM_GATEWAY_JWT_SECRET
  规避包内既有 env 污染（该债务为预存在，未扩大修复）。
- 风险：multi_issuer 跨项目主体（user_id 语义异库）不做吊销检查（保守）；
  5s 缓存滞后窗口内旧 token 仍可通过（可接受）。

### B5 设置键组 —— 本会话，c98b329a4
- 改动：① `llmgw_continue_keywords`/`llmgw_retry_keywords` 补 spec
  （TypeString=JSON 数组字符串形态，对齐 hotconfig.GetString 通道），默认
  词典 zh/en 逐字保留 + ja 骨架，settings/executors 两侧镜像锚点测试；
  ② `security.sensitive_block_score`/`warn_score`（默认 0.6/0.3=原硬编码，
  CachedPlatformFloat ≤5s 热更 + warn<block 防御性 clamp）；
  ③ CategoryRetry "网关重试与超时"聚合组：goal.retry_*(tenant)/
  stream_retry_threshold/error_probe.timeout_ms 只归组展示不迁移存储；
  前端 category bar + 8 语种 i18n。
- 测试：settings 全包绿（注册链、JSON 形态、多语种骨架、聚合组防退化、
  clamp）；executors 镜像锚点测试绿。
- 风险：词典热更走 hotconfig 30s 轮询（非即时）；aggregation 仅 UI 层。

### B1 峰谷倍率体系 —— 本会话，39b8efbe0（迁移 736）
- 改动：配置同源 `maas.rate_periods`（spec 注册，TypeString JSON 形态，
  默认 enabled=false 零漂移，种子=双高峰 3x+深夜 0.7x，显式 Asia/Shanghai）；
  Go `ResolveRateMultiplier` 与 SQL `maas_resolve_rate_multiplier()`（736）
  同规则（半开区间/跨午夜/退化钳制/fail-open）；计费链
  ChargeRequestMultimodalWithMultiplier 总额乘倍率后单次 CEIL，SQL 估算
  表达式 COALESCE(credits_rate_multiplier,1.0) 折入率项（数学等价）；
  usage_ledger(_hot).rate_multiplier、request_logs(_hot).credits_rate_multiplier
  四列落账（分区族级联；102 参数 UPSERT 尾部 $103 追加零移位）；handler
  以 eventAt 取档一次同时盖章计费与审计痕迹。
- 测试：maas 全包绿（取档矩阵/精度 2.1→ceil 3/0.9999→ceil 1/折价不清零/
  退化钳制/1.0x 等价/SQL 防退化钉桩）；telemetry mock 计数全量对齐绿；
  本机真库 SQL 函数 6 点对拍（3x/0.7x/1x/12:00 边界/11:59/07:00 跨午夜
  端）与 Go 矩阵逐点一致，配置清理后回 1.0。
- 风险：**迁移登记六点同步**（apply-db-revision-sequence.sh 序列清单是
  第 6 点，本轮实测漏登记时存量库静默跳过 736，已补并冒烟验证）；
  估算表达式每率各自 CEIL 的既有估算-精确差保持原样（非本波引入）。

### B7 看板 IP 地域归类 —— 本会话，提交于 B9 前（ip_region）
- 改动：classifyVirtualIP 三态（内网/保留段直显 IP；外网经本地段表
  归类国家·省·市；无表/未命中优雅降级原样）；段表
  data/geoip/segments.csv（LLM_GATEWAY_GEOIP_CSV 覆盖路径，csv Comment
  +变长行容错，10min 惰性重载）——零新依赖不引外网；virtual_ips 饼图
  接线，同区域多 IP 合并计数。
- 测试：admin 全包绿（保留段判定矩阵、透传三类、v4/v6 命中、降级、合并）；
  实现级修复两个由测试抓出的健壮性缺口（csv 注释行/变长行）。
- 风险：段表为运维供给物，缺席时饼图回退裸 IP（即现状）。

### B9 摘要 fallback 同厂优先 —— 本会话，b9d18e060
- 改动：WithTargetModelHint + ReorderSameVendorFirst 稳定重排（同厂前置
  保序），session_compressor 从请求体提取 model 作 hint（失败回退原序）；
  不改链内容与配置语义。
- 测试：summary 包绿（重排矩阵/不可变性/Summarize 级首尝试同厂+无 hint
  原序）。
- 风险：首 token 同厂近似（与 InferFamily 常见命名一致）；链内无同厂
  模型时恒等（行为同现状）。

### B10 泳道空闲 1 分钟档 —— 本会话，a99274148
- 改动：双档 idle marker（≥5min→no_traffic_5min；1~5min→no_traffic_1min），
  SSE IdleTickInterval 默认 5min→1min；前端零改动（status+elapsed 动态
  计算天然兼容）。
- 测试：admin 全包绿（90s→1min 档、6min→5min 档；既有 idle 语义测试全
  兼容）。
- 风险：1min tick 扫描为索引驱动（方案C）成本可控；已观察。

### B2 状态老化+闪断双确认+同凭据并发 —— 并行会话承接（bg/node_probe.go
等，含 Wave 3 B2②③ 常量与 credSyncSem），本会话为避撞退出，以远端/本地
提交为准。

### B8 usage↔credit 内部对账 —— 并行会话承接（638b1d41f，737
maas_reconciliation_findings）。本会话曾实现独立版本（bg/credit_reconciliation
+ 737_credit_reconciliation_findings），发现编号与功能撞车后**完整撤销**
（4 文件删除+6 点登记还原），737 编号让渡；installer 契约测试复验绿。

### B12 goal AUDIT 三轮+VERIFY —— 并行会话承接（audit_hook.go 的
InterceptNonStreamMultiRound / AuditRoundStore / AuditVerifyResult / Round、
Verify 字段，goal.audit_rounds 设置）。本会话曾并行 patch（Config.Rounds+
consensus 调用），发现叠加后**精确撤销自己的三处**（未动对方改动），
编译恢复绿。

## 未完成挂账（留 Wave 4 / 续跑）
- B6 发行分发链三补：autoupdate artifact 上传 API+字段、下载页 catalog、
  licensing 设备上限默认 1→2（若 1 为商务设定须在套餐维度区分并记录
  裁决）。无迁移需求确认前不动编号。
- B11 providers official 原厂/中转显式标记列：需迁移（下一个编号从
  **738** 起，736=本会话 B1、737=并行会话对账）+ 列表筛选 + 泳道读取。
- B13 Responses 上游方向 Parse + response.completed 终态回带。
- B14 并发 slot 300s 硬顶（executor.go 持有点 lease deadline）——本轮
  executor.go 被并行会话占用（Wave4-D4/D2 重构中），冲突未做。
- 预存在债务顺手记录：admin 包测试 env 污染（auth_identity_test.go
  newTestSigner 设 LLM_GATEWAY_JWT_SECRET 不还原）。

## 部署冒烟
- 2182/2183 两轮 deploy-local.sh：VERIFY_PASS=1（8782 /health + admin
  登录→providers→credentials 凭据解密冒烟），迁移 736 真库应用并完成
  SQL 函数对拍后网关健康。

## 映射回归执行记录（2026-09-22 收口轮补录）

按《llm-gateway-全方面测试方案.md》映射执行；P0 压测级全量矩阵（T18 等）
不在本波范围，执行的是与本波改动直接相关的用例与断言。

### T02（B3）模型标准化与解析 —— 执行方式：真库行为级回归（一次性脚本）
- **T02-02 请求侧补录 PASS（并抓出 B3 死路径 bug，修复 f7f8f66d5）**：
  真库插入 provider_models raw（`Wave3-T02-Blip-9x7Q`，大小写混合、不在
  标准目录）→ resolve.Resolver.Resolve → 首轮即触发
  `resolve: seeded canonical` 日志、返回 path=canonical、
  models_canonical/model_aliases 各落一行。
  - 回归价值：修复前该场景**静默失败**——resolveDB 全 miss 尾部返回
    非 nil passthrough，使 `resolved==nil` 分支（B3 补录 + 2026-08-15
    负缓存）在有 DB 的生产环境永不执行（负缓存自引入即死代码）。
    修复：两个 miss 返回点改报真 nil；单测未抓到是因 fake resolveDBFn
    恰好按新语义返回 nil，真实返回形态无钉桩。
  - 部署：修复已上 8782（build 2185 = f7f8f66d）。
- T02-01/03/04：单测绿（resolve 14 用例）；T02-04 同源于 Wave 1 A1
  LiveRoutingSource（已收口）。

### T05（B2）节点状态与探测 —— 执行方式：单元/行为级测试
- T05-05 闪断防护 PASS：TestProbeConfirm_VerdictMatrix（12s，2~5s 双 ping
  判定矩阵）+ TestProbeConfirm_PacingConstants/NilWorkerFailsClosed。
- 状态老化 PASS：demoteAgedHealthyBindings 老化语义
  （probe.state_aging_hours 默认 2h，裁决=suspect 落为 recovering+复核，
  见 6d3c8a28d 提交说明）。
- T05-06 探测必要性跳过 PASS：probe_necessity 三条件 6 用例全绿
  （B2 未破坏既有跳过语义，约束达成）。
- 同凭据并发≤2 PASS：nodeProbePerCredSyncConcurrency=2 + credSyncSem。

### T14（B1/B8）计量计费与对账 —— 执行方式：单测 + 真库对拍/抽查
- T14-03 积分与倍率 PASS：CalcCreditsMultimodalWithMultiplier 3x/1x/0.7x
  精度与取整用例（maas 全包绿）+ 真库 maas_resolve_rate_multiplier()
  6 点对拍一致（含 12:00 边界、07:00 跨午夜端）；SQL 估算表达式折入
  倍率钉桩（TestRequestLogCreditsSQL_FoldsRateMultiplier）。
- T14-04 账本一致 PASS：B8 对账 worker 测试全绿（ConsistencyWorker 6 用例）
  + 真库 48h 窗口 balance_after 链抽查 **0 断裂**（与 worker 同款窗口 SQL）。
- T14-01/02/05：非本波改动面（既有行为）。

### T15（B4/B5）租户隔离与权限 —— 执行方式：HTTP 全链（8782 真机）
- **T15-03 首登改密→自动注销 PASS**：创建临时用户 wave3-t15-tmp → 登录 →
  PUT /api/auth/change-password 成功 → **旧 token 调 API 得 401**（B4 核心
  断言）→ 新密码登录成功（新 token iat>纪元）→ settings_kv
  admin.auth_revocations 记录 user 44 纪元 ✓。测试用户已清理。
- B5 设置键注册验证：spec 注册链由 settings 全包测试钉住
  （PlatformSpecs 含 maas.rate_periods / llmgw_* / sensitive_* 五键）。
- T15-01/02：非本波改动面。

### T16（B7/B10）可观测性 —— 执行方式：真库/真 API + miniredis 行为级
- T16-05 看板 IP 地域 PASS：/api/admin/dashboard/board（Custom 范围绕
  Redis 缓存直查）virtual_ips 饼图——插入内网 IP 10.0.0.9 后**原样直显**
  （B7 内网臂）；哨兵 __unknown__ 正确透传不误归类；外网段表命中路径由
  单测覆盖（真容器无 segments.csv，降级=现状符合设计）。测试数据已清理。
- T16-01/02 泳道空闲块 PASS：TestIdleMarkerTiering（miniredis 行为级：
  90s 静默→no_traffic_1min、6min→no_traffic_5min）+ 既有 idle 语义
  测试全兼容。
- T16-03/04：非本波改动面。

### T10（B9）压缩与超长上下文 —— 执行方式：单元/行为级
- B9 同厂优先重排：summary 包全绿（重排矩阵+Summarize 级链序断言，
  env 注入链验证 hint 生效与无 hint 原序）；T10-01/02/03 阈值链路非
  B9 改动面（B9 只重排 fallback 顺序，不改触发阈值）。

## Wave 4 交接
直接使用源文档 §7.4 提示词开启新会话。前置状态更新：
- Wave 3 完成项以本台账 + git log（0a2dfc3a4..a99274148 及后续）为准；
- 挂账见上节（B6/B11/B13/B14）；迁移下一个编号 **738**；
- D 类五项中 D2/D4 已由并行会话在共享工作树推进中（domains/streaming、
  internal/ir、vendorstrip 有未提交改动），Wave 4 会话接手前先核对工作
  树状态，避免重复实现。
