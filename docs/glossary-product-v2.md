# 产品词汇表 v2.0

> **用途**: 本表规定所有对外文档、UI 文案、API 文档中使用的标准术语。  
> **版本**: v2.0 (2026-11-22)  
> **维护原则**: 客户词优先、中英对照、禁止同义混用  
> **门禁**: web/scripts/i18n-internal-jargon-baseline.json 自动检测内部词泄漏

---

## 0. 术语使用规范

### 0.1 客户词 vs 内部词

| 层级 | 用途 | 示例 |
|------|------|------|
| **客户词** | 对外文档、UI、帮助手册、营销材料 | 租户、预算、护栏、探活 |
| **内部词** | 代码标识符、内部设计文档、变量名 | tenant、armor、guardian、probe |

**规则**: 
- 客户词必须在全站保持一致（禁止同义混用）
- 内部词仅作实现标识，不得出现在客户界面
- 翻译时以中文客户词为源，英文跟随

### 0.2 已统一的 6 组同义词

| 组 | 统一术语 | 禁用同义词 | 口径 |
|---|---------|------------|------|
| 1 | **预算** (Budget) | 配额（非速率语境） | 预算=金额（USD/积分），用于成本控制 |
| 1 | **配额** (Quota) | 预算（非金额语境） | 配额=速率与并发（RPM/TPM/QPS/并发数） |
| 2 | **护栏** (Guardrail) | 围栏、防护、合规（孤立使用） | 统一为"护栏"，内部 armor/guardian 不外露 |
| 3 | **探活** (Health Probe) | 探测（非流量语境） | 探活=健康检测；"探测流量"作为短语保留 |
| 4 | **租户** (Tenant) | 企业（技术语境）、组织（除非引入 org 表） | 租户=隔离单位；"企业"仅作营销词 |
| 5 | **usage_facts** (表名) | request_logs（已废弃） | 新部署不再创建 request_logs 表 |
| 6 | **硬额度** (Hard Limit) | 硬拦截、强制额度 | 达限拦截 429 budget_exceeded |
| 6 | **软额度** (Soft Limit) | 软提醒、预警额度 | 预警阈值告警，不拦截 |

---

## 1. 统计类术语（40 词）

> **来源**: 帮助手册 §10.7《统计术语总表》  
> **分组**: 数字类 18 词 / 时间类 7 词 / 状态类 7 词 / 口径类 8 词  
> **规则**: 本表不重复罗列，详见 `web/content/help-manual.zh-CN.md:874-937`

**快速索引**（完整定义见帮助手册）:

| 中文术语 | English | 分类 |
|---------|---------|------|
| 请求数、成功率、失败请求 | Requests, Success Rate, Failed Requests | 数字 |
| 输入/输出 Token、缓存读/写 Token、总 Token | Input/Output Tokens, Cache Read/Write | 数字 |
| 内部成本（美元）、外部成本（积分）、总花费 | Internal Cost (USD), External Cost (Credits) | 数字 |
| 平均延迟、健康分、合规率、误报率 | Avg Latency, Health Score, Compliance Rate | 数字 |
| 窗口、统计日、今日、近 7/30 日、实时、累计口径 | Window, Statistical Day, Today, Real-time | 时间 |
| 在途、活跃会话、静默会话、探测/业务流量 | In-flight, Active/Silent Session | 状态 |
| 单一真源、快照、含/不含探测、同源对拍 | Single Source of Truth, Snapshot | 口径 |

---

## 2. 组织类术语（14 词）

| 中文 | English | 定义 | 内部标识 |
|------|---------|------|----------|
| **租户** | Tenant | 隔离单位；数据、权限、预算的顶层边界；RLS 按租户隔离 | tenant |
| **团队** | Team | 租户内的预算与授权子单位；支持层级（parent_id）| team |
| **用户** | User | 自然人账号；归属于租户，可挂入团队 | user |
| **密钥** | API Key | 程序调用凭证；归属租户+用户，可选挂团队 | api_key |
| **应用** | Application | 密钥分类维度；定义默认配置与权限模板 | application |
| **角色** | Role | 权限模板；super_admin / tenant_admin / user 三级 | role |
| **超级管理员** | Super Admin | 跨租户权限；平台运维角色 | super_admin |
| **租户管理员** | Tenant Admin | 租户内全权限；不可跨租户 | tenant_admin |
| **普通用户** | Regular User | 受限权限；仅访问自己的资源 | user (role) |
| **所有者** | Owner | 资源归属标识；密钥/会话的 owner_user 字段 | owner_user |
| **归属租户** | Home Tenant | 用户的主租户；单用户当前仅支持单租户 | tenant_id |
| **跨租户** | Cross-Tenant | 超管操作场景；普通用户禁止 | cross_tenant |
| **系统密钥** | System Key | 平台保留密钥；is_system=true，不可吊销 | is_system |
| **默认租户** | Default Tenant | 平台自身租户；code='default'，管理面所在租户 | default (tenant) |

---

## 3. 计费类术语（22 词）

| 中文 | English | 定义 | 内部标识 |
|------|---------|------|----------|
| **预算** | Budget | 金额限制（USD 或积分）；key 级/租户级/团队级 | budget_usd |
| **配额** | Quota | 速率限制（RPM/TPM/QPS/并发数） | quota, rate_limit |
| **硬额度** | Hard Limit | 达限拦截 429 budget_exceeded；enforcement_mode='hard' | hard_limit |
| **软额度** | Soft Limit | 预警阈值告警；enforcement_mode='warn'，不拦截 | soft_limit |
| **积分** | Credits | 计费货币；1 积分 = cents_per_credit 美分 | credits |
| **内部成本** | Internal Cost | 网关按上游价目折算的美元成本 | internal_cost_usd |
| **外部成本** | External Cost | 计费体系里的积分消耗 | external_cost_credits |
| **单价** | Unit Price | 按千 Token 计价；unit_price_minor 以 1m_tokens 为基准 | unit_price_minor |
| **月度账单** | Monthly Bill | 按租户/团队/用户出账；每月 1 日生成上月账单 | maas_monthly_bills |
| **优惠券** | Coupon | 抵扣券；coupon_amount 直接减免账单 | coupon |
| **充值** | Top-up | 预付费充值；增加租户积分余额 | topup |
| **欠费** | Arrears | 积分余额不足；warn 模式放行+告警 | arrears |
| **对账** | Reconciliation | 网关记账 vs 供应商账单双视角对照 | reconciliation |
| **快照** | Snapshot | 对账冻结时点；快照后新流量不回填 | snapshot |
| **账本** | Ledger | 请求流水聚合；usage_ledger 单一真源 | ledger |
| **消费桶** | Consumption Bucket | 按小时/天/月聚合的消费汇总 | consumption_bucket |
| **流水** | Usage Facts | 请求级明细；usage_facts 表 | usage_facts |
| **定价策略** | Pricing Strategy | 模型定价规则；按供应商/模型/租户维度 | pricing_strategy |
| **计费周期** | Billing Cycle | 按自然月；UTC 月界 | billing_cycle |
| **免费池** | Free Pool | 免费额度池；供应商提供的免费 API 凭据 | free_pool |
| **预算超限** | Budget Exceeded | 429 错误码；tenant_budget_exceeded / key_budget_exceeded | budget_exceeded |
| **月累计** | Month-to-Date (MTD) | 本月 1 日 00:00 至今的累计消费 | mtd |

---

## 4. 安全类术语（18 词）

| 中文 | English | 定义 | 内部标识 |
|------|---------|------|----------|
| **护栏** | Guardrail | 安全防护机制统称；包括 PII/注入/敏感词/输出合规 | guardrail |
| **脱敏** | Sanitization | PII 检测与清洗；sanitize 模块 | sanitize |
| **拦截** | Block / Hard Block | 检测到风险时拒绝请求；返回 403 blocked_by_guardrail | hardblock |
| **监控** | Monitor | 检测但不拦截；记录审计日志 | monitor |
| **审查** | Review | 人工复审队列；检测为可疑时入队 | review |
| **敏感词** | Sensitive Words | 违规内容词库；sensitive engine | sensitive_words |
| **PII** | Personally Identifiable Information | 个人身份信息；手机/身份证/邮箱等 | pii |
| **提示注入** | Prompt Injection | 恶意 prompt 攻击；prompt_injection 检测器 | prompt_injection |
| **越狱** | Jailbreak | 绕过模型安全机制的攻击 | jailbreak |
| **输出合规** | Output Compliance | 模型返回内容合规检查 | output_compliance |
| **工具权限** | Tool Permission | MCP 工具调用白名单；tool allowlist | tool_permission |
| **审计日志** | Audit Log | 操作留痕；admin_audit_logs 表 | audit_log |
| **凭据监控** | Credential Monitor | API 密钥泄漏监控 | credential_monitor |
| **异常检测** | Anomaly Detection | 异常行为识别；format_anomalies / request_anomalies | anomaly |
| **合规率** | Compliance Rate | 合规会话占比；合规/警告/违规三档 | compliance_rate |
| **违规** | Violation | 触发护栏拦截；violation 状态 | violation |
| **误报** | False Positive | PI 分析中判定为攻击但实为误报 | false_positive |
| **白名单** | Allowlist | 豁免检查的规则列表；不使用"白名单" | allowlist |

---

## 5. 路由与流量术语（12 词）

| 中文 | English | 定义 | 内部标识 |
|------|---------|------|----------|
| **供应商** | Provider | 上游模型服务商；OpenAI / Anthropic / Azure 等 | provider |
| **模型** | Model | 大语言模型；gpt-4 / claude-3-sonnet 等 | model |
| **凭据** | Credential | 供应商 API 密钥；credential / api_credential | credential |
| **路由** | Routing | 请求分发策略；routing_policy | routing |
| **探活** | Health Probe | 凭据健康检测；probe_health | probe |
| **降级** | Degradation | 超预算时选贱模型；modelbudget 降级梯子 | degradation |
| **故障回退** | Failover | 上游失败时换凭据/供应商 | failover |
| **熔断** | Circuit Breaker | 故障隔离；circuit_breaker 机制 | circuit_breaker |
| **限流** | Rate Limiting | QPS/并发限制；rate_limiter | rate_limit |
| **负载均衡** | Load Balancing | 多凭据轮询；weighted / round_robin / sticky | load_balance |
| **智能路由** | Smart Routing | 按复杂度分流贵贱模型；autoroute 模块 | smart_routing |
| **探测流量** | Probe Traffic | 探活合成请求；默认不计入业务口径 | probe_traffic |

---

## 6. 运维类术语（10 词）

| 中文 | English | 定义 | 内部标识 |
|------|---------|------|----------|
| **蓝绿部署** | Blue-Green Deployment | 零停机发布；deploy-seamless-docker.sh | blue_green |
| **健康检查** | Health Check | /healthz 端点；返回版本与状态 | health_check |
| **迁移** | Migration | 数据库 schema 变更；sql/migrations/*.sql | migration |
| **部署序号** | Build Sequence | build_seq；递增编号标识构建 | build_seq |
| **回滚** | Rollback | 切回旧版本；蓝绿双槽保留回滚锚 | rollback |
| **门禁** | Gate / Gatekeeper | 部署前置检查；sqlguard / vitest 测试门 | gate |
| **列存归档** | Columnar Archive | 热分区按月归档；columnar 表压缩 8-10× | columnar_archive |
| **分区** | Partition | 按月分区表；usage_facts_YYYYMM | partition |
| **备份** | Backup | 配置文件归档；/opt/.../backups/ | backup |
| **日志轮转** | Log Rotation | 保留 N 份备份；删除超期日志 | log_rotation |

---

## 7. 会话与分析术语（8 词）

| 中文 | English | 定义 | 内部标识 |
|------|---------|------|----------|
| **会话** | Session | 多轮对话单元；session_id 标识 | session |
| **轮次** | Turn | 会话内单次请求-响应；session_turns | turn |
| **任务** | Task | 业务任务维度；task_id 标识 | task |
| **客户端** | Client | 调用方标识；client_id（IP/UA/标签） | client |
| **用户画像** | User Profile | 按用户聚合的使用分析 | user_profile |
| **会话分析** | Session Analytics | 多维下钻；按用户/客户端/任务/时间 | session_analytics |
| **健康分** | Health Score | 会话质量评分；A/B/C/D/F 五档 | health_score |
| **活跃会话** | Active Session | 最近 24h 内有请求的会话 | active_session |

---

## 8. 插件与扩展术语（8 词）

| 中文 | English | 定义 | 内部标识 |
|------|---------|------|----------|
| **插件** | Plugin | 进程外扩展；plugin-runtime 框架 | plugin |
| **扩展点** | Extension Point | 插件挂载位置；extpoints 定义 | extpoint |
| **清单** | Manifest | 插件元数据；manifest.yaml | manifest |
| **能力** | Capability | 插件声明的功能；capability 注册 | capability |
| **MCP 工具** | MCP Tool | Model Context Protocol 工具面 | mcp_tool |
| **错误处理模板** | Error Remediation Template | 自动化故障处理；error-playbook | error_template |
| **插件市场** | Plugin Marketplace | 插件分发目录（规划中） | marketplace |
| **激活** | Activation | 插件装载生效；activate 原子切换 | activation |

---

## 9. 术语使用检查清单

### 9.1 文档撰写时

- [ ] 所有客户词已在本表登记
- [ ] 无同义混用（预算/配额、护栏/围栏、探测/探活）
- [ ] 中英对照一致
- [ ] 内部词未出现在客户界面

### 9.2 代码提交时

- [ ] UI 文案使用客户词
- [ ] locale 文件已同步（8 语种）
- [ ] jargon 门禁全绿（web/scripts/i18n-internal-jargon-baseline.json）

### 9.3 翻译时

- [ ] 以中文客户词为源
- [ ] 英文跟随本表 English 列
- [ ] 术语在 8 语种保持一致

---

## 10. 版本历史

| 版本 | 日期 | 变更 |
|------|------|------|
| v2.0 | 2026-11-22 | 新增组织/计费/安全/路由/运维/会话/插件七类 130+ 词；统一 6 组同义词；建立客户词/内部词分层规范 |
| v1.0 | 2026-10-XX | 初版：统计类 40 词（帮助手册 §10.7） |

---

## 11. 维护责任

- **Owner**: 产品团队 + 技术写作
- **Review**: 每季度复核一次，删除废弃术语、补充新增术语
- **门禁**: CI 集成 jargon 检测，文案 PR 必须全绿

---

**注**: 本表与帮助手册 §10.7、statistics-standardization-framework.md 同源。发现冲突时以最新版本为准并报障。
