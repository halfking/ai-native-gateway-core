# 脱敏规则管理、人工复核与集群刷新

用户已批准并要求完整实现、测试、提交推送。基线493d24ebcdaf45d792130f32b42e6724cb667905；复用已附加的干净worktree，分支codex/sensitive-rule-management。生产部署不在本轮范围。

## 可验收行为

1. 系统模块参数中提供规则刷新周期、命中审计开关与记录保留量，保存后运行时自动读取。规则管理页可读取有效规则、编辑/增删、预览样例及反例、保存发布。
2. 数据库保存不可变规则版本和当前指针，发布需携带base_version防覆盖；回滚创建新的递增版本，保留目标版本与操作者说明。无效正则/捕获组/范围/动作/样例拒绝发布，旧版本继续使用。
3. 规则包含稳定ID、名称、敏感类型、语义种类、正则、捕获组、适用阶段(input/output/egress)、动作、启停、优先级、租户及工具/字段范围。内置规则的语义不再依靠正则字符串相等判断。JSON结构校验、可信占位符还原、租户隔离不交给用户脚本。
4. 正常脱敏、阻断、语法豁免及待确认组合均可在规则命中列表查看，按脱敏后的结构指纹聚合。不得持久化原始口令或把反馈内容自动变为全局放行规则。人工可确认敏感/误报/忽略，并从关联规则进入限定范围的草稿编辑，发布后显示关联版本。
5. 各网关订阅Redis发布通知，同时定时检查数据库版本；通知丢失靠轮询补齐。先完整编译验证后原子切换。运行中请求固定一个不可变快照，新请求获得最新版本；已有会话占位符映射不被重写。
6. 网关周期上报实例ID、已生效版本、最后刷新时间、错误与心跳；管理页显示目标版本和实例差异。数据库/Redis中断不清空有效规则；通知故障不伪报全部生效。
7. 规则版本、事件、复核与实例API都按现有admin鉴权/授权链注册。规则发布/回滚及全局实例视图要求超级管理员；普通管理员只能读取授权租户事件、提交该租户复核。所有变更记录操作者。
8. 桌面现有输出合规页增加规则管理入口；规则、命中/复核、版本回滚、实例状态页面可用，支持加载/空/失败、错误提示、双击防重及无敏感真值展示。沿用主题和i18n，不另起孤立导航。

## 架构及契约

- 新域domains/sanitizerules负责规则模型/验证/不可变快照、Postgres版本/事件/心跳存储、原子刷新管理器。只依赖标准库与现有Redis库，不依赖sanitize以免循环。
- security/sanitize负责将YAML默认规则转为带语义的Rule并消费管理器快照，输入/输出/工具三阶段沿用现有主链；domains/outputcompliance消费同一快照的输出规则，避免前后两套规则漂移。
- Config={rules:Rule[]}。Rule={id,name,type,kind,pattern,group,scopes,action,enabled,priority,tenant_id,tools,fields,samples,exemptions}；Sample={text,match}。kind采用pattern/credential_declaration/credential_assignment/credential_pair/account_declaration/account_assignment/network_destination/review_combination等封闭值；合规sed识别仍由内置语法解析器完成并记为exempted。
- Version={id,parent_id,rollback_of,config,actor,comment,created_at}；id=0表示无发布时的内置/YAML基线。所有持久化发布ID递增，CAS以当前指针为准。首次可用数据库将默认配置CAS保存为baseline1，竞态节点采用获胜版本。0仅表示离线本地回退，显示config_digest，不可作为rollback目标；baseline1可正常回滚。
- Event={id,fingerprint,tenant_id,rule_id,version,phase,action,status,sample_masked,count,first_seen,last_seen,reviewer,review_comment,reviewed_at}。sample_masked由系统生成类型/字段结构摘要，绝不接收客户端原文作为存储证据。
- Instance={instance_id,version,config_digest,last_seen,last_refresh,error}；实例ID由主机/端口/进程启动标识生成，避免154/245或蓝绿实例覆盖。
- HTTP根路径/api/admin/sanitize-rules：GET当前配置；POST /validate；POST /publish {base_version,config,comment,review_event_ids}；GET /versions；POST /rollback {base_version,target_version,comment}；GET /events?status=&action=&limit=；POST /events/{id}/review {status,comment}；GET /instances。
- kind合法矩阵：自然credential_declaration/account_declaration/credential_pair仅input；pattern/credential_assignment/account_assignment/vendor_key_shape可显式选阶段。egress不允许mask，仅block/log。优先级高者先扫描但不短路；所有block优先于log和语法豁免，mask重叠并集合并覆盖完整值。Refresh串行并拒绝倒退版本。
- 启用版本管理后，/sensitive-words/reload不得改写PatternDetector，受管理的Build/Reload拒绝本地覆盖。输入、mandatory guard、后置Checker及buildRedactBodyFn的独立Checker均消费同一Manager；请求快照钉在共同入口，独立写时检查按context快照（无上下文时取当前快照）。
- 旧pii_patterns及硬编码secret/internalIP检测在管理器启用后由初始快照中的对应规则统一接管并停止重复执行；毒性/偏见等独立合规功能保持原入口。API停用一条规则不得仍由旧PII/secret链执行同条规则。
- 发布携带review_event_ids，由服务端绑定认证租户为ReviewLink，Store事务校验已复核及租户匹配并写resolved_version；版本历史保留关联。事件聚合键含tenant/rule/version/phase/action，禁止继承旧版复核状态。
- 后台反馈只改事件处理状态；“编辑关联规则”打开当前版本规则编辑器，确认保存发布后才影响数据面。每次发布显示新的目标版本及实例回报，不把HTTP保存成功等同所有节点已经生效。
- 新增startup迁移并同步实际installer embed/通道登记，提供可重复执行及回滚SQL；只在本地临时Postgres验证，不改生产库。

## 测试矩阵

- 规则：非法正则/组、重复ID、空规则、未知阶段/动作、正反样例、优先级/租户/工具/字段约束及旧R1034语义。
- 版本：首次发布、CAS冲突、并发发布、不可变历史、回滚产生新版本、失败保留旧快照。
- 刷新：两个独立Manager共享真库、Redis通知、丢通知轮询、错误恢复、在途请求固定旧版本、race；实例状态与实际执行版本一致。
- 事件：mask/block/exempted/pending、聚合、租户隔离、复核审计、秘密真值零持久化。
- API：匿名401、非超级管理员写403、跨租户拒绝、发布/回滚/复核成功与失败。
- UI：单测+类型/构建+browser-use真实交互（参数保存、规则校验拒绝、发布、双实例生效、误报复核、回滚、非法版本不覆盖）。
- 回归：sanitize/outputcompliance/hooks/streaming/settings/admin相关包、race、全仓vet/build；统一门按当前基线实测，不套用历史失败结论。关键逻辑做撤修复变异。

## 审查后澄清

- 敏感类型采用可还原的十种类型闭集；导入既有PII表时bank_card映射credit_card，地址类型映射server_ip，其他旧类型映射custom，避免产生不能还原的标记。
- egress可配置exemptions={inert_tool,nonroutable_destination,identity}，只对携带该豁免的规则生效；内置基线保持既有运维豁免，自建阻断规则默认无豁免。重叠规则独立判定，任何未豁免block均不能被另一条规则吞掉。
- network_destination把既有网络目的地识别作为可版本化语义，限定egress；review_combination限定input/log，只记录候选。候选事件动作pending，结构枚举pair_slash/pair_pipe/pair_colon/pair_other，不保存帐号、口令或原始请求。
- Redis订阅建立与数据库轮询独立，握手有真实读取期限；数据库刷新也有超时。后置及write-time检查保留可信还原值，mandatory模型输出守卫不继承该豁免。

- 人工复核提供两条显式入口：编辑实际命中规则保留原ID与适用范围并提示全局影响；新增限定补充规则保留原规则且明确不覆盖。发布关联只表示变更溯源，不自动宣称误报已消失。
- 字段约束使用真实文本叶键；管理模式仅对协议根arguments/input容器解码JSON，实际嵌套字符串（含自定义字段）保持完整值。
- main的write-time callback传递请求ctx与认证tenant，恢复来源在运行时读取，避免启动装配顺序捕获空来源。
