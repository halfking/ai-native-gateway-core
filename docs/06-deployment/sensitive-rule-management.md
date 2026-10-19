# 脱敏规则、复核与版本管理

桌面端从“数据运维 → 敏感信息规则”进入，也可从输出合规页、系统模块的输出合规模块进入。全局配置、发布、回滚和实例状态限超级管理员；租户管理员只能查看与复核本租户命中。

## 修改与生效

1. 在规则页编辑或新增草稿。选择敏感类型、语义、阶段、动作、捕获组及租户/工具/字段范围；样例只使用虚构测试数据。
2. 先验证正反样例，再填写原因并发布。发布使用当前基准版本做并发检查；遇到冲突保留草稿，重新读取、核对其他操作者修改后再发布，系统不会自动覆盖。
3. 查看目标版本与网关实例。保存成功代表数据库已发布；各实例的版本、摘要、心跳与错误才是实际生效证据。失联实例显示陈旧状态，不能当成已生效。

系统模块参数包括：`sanitize.rules.refresh_interval_seconds`（1–300秒，默认5）、`sanitize.rules.audit_enabled`（默认开启）、`sanitize.rules.event_limit`（100–100000条聚合记录，默认10000）。保存沿用现有平台设置通道，网关动态读取。

网关先完整编译规则，再原子替换快照；Chat、Messages、Responses入口固定请求版本。Redis通知加快刷新，数据库轮询补偿丢通知；订阅握手和数据库刷新有超时。数据库故障保留最后有效快照。启动导入失败仍安装本地管理器，后台恢复后优先读取已发布版本；只有无发布版本时才完整导入旧规则并CAS保存基线。初次数据库可用时保存可回滚基线版本1；版本0表示本地回退，不能作为回滚目标。

## 动作与范围

- `mask`：输入分配可还原占位符，输出按类型遮蔽。`block`：策略拒绝。`log`：只记录。
- 自然语言声明和帐号/口令组合仅适用于输入，不能把输入语义扩展成工具输出拦截。`review_combination`仅支持input/log，记录特殊组合候选，事件动作为pending。
- `network_destination`只适用于egress，结合表达式和网络目的地解析，保留既有运维判别。egress支持block/log，不支持mask。
- egress豁免为显式配置：`inert_tool`、`nonroutable_destination`、`identity`。内置基线保留既有豁免；自建阻断规则默认无豁免。规则分别判断，某条豁免不能吞掉其他严格阻断规则。
- 重叠mask取并集覆盖，避免残留口令尾部。可信还原值在后置检查中保留；模型输出的mandatory守卫不会继承该豁免。
- 普通文本按真实JSON叶键匹配字段；工具根参数容器继续解析子键，实际字符串叶子即使内容像JSON也保留完整还原边界。JSON结构、可信还原与租户身份保持代码约束；规则不执行用户脚本。版本管理启用后，本地敏感词重载不能覆盖规则快照。

## 人工复核

命中列表包含遮蔽、阻断、记录、豁免和待确认组合，展示规则、版本、次数及安全结构摘要。帐号/口令及请求原文不进入该证据表；特殊组合仅保留斜线、竖线、冒号等结构枚举。

可确认敏感、判为误报或忽略，记录操作者与说明。“编辑实际命中规则”保留原ID与适用范围，可明确收窄误报规则；全局规则的修改影响全部租户。“新建限定补充规则”建立限定租户和阶段的草稿，保留原规则，并提示重叠规则仍可能生效；复核本身不会放行流量。发布草稿后，复核记录与新版本在同一事务中关联。样例中不要粘贴真实会话凭据。

## 硬阻断清单（哪些规则允许终止回合）

规则的动作（`block`）只说明「命中了怎么办」；**能不能真的终止回合**由**硬阻断清单**决定。清单是网关启动时读的环境变量 `LLM_GATEWAY_HARD_BLOCK_RULES`，逗号分隔的 `scope:rule`：

```bash
LLM_GATEWAY_HARD_BLOCK_RULES="egress:builtin.egress_sink,output:builtin.pii.email"
```

| 写法 | 含义 |
|---|---|
| `*` | 该 scope 下全部规则 |
| `builtin.network_destination` | 精确规则 ID |
| `secret.*` | 同前缀的一族规则 |

scope 只有三个：`input`（请求侧）、`output`（模型输出侧）、`egress`（工具参数的外传目的地侧）。

**出厂默认 = `input: *`**：请求侧照旧 fail-closed；输出侧与外传侧**不终止回合**，命中只记台账（`sanitize_rule_events`，动作记 `log`/`mask`）并继续往下走。设置方法：改容器/服务的环境变量后重启网关即可，不必改代码发版。启动日志会打一行 `hard block allowlist loaded`，清单写错会当场报 error 并**保持原清单**（一个错别字不会悄悄撤掉守门）。

环境变量的三条语义：

| 环境变量 | 效果 |
|---|---|
| 未设置 | 保持出厂默认（只有 input 允许硬阻断）—— 忘了配 ≠ 要全放开 |
| 有值 | **出厂默认 + 配置项**（**累加**）—— 只加不减，请求侧底线始终在 |
| `-` | 显式清空（任何 scope 都不允许硬阻断）—— 唯一能关掉请求侧底线的写法，必须显式 |

> ⚠ 累加语义是 211 首轮部署实测后改的：原先「有值 = 替换」，运维只想开一条
> egress 规则时，启动日志的 allows 里 **input 消失** ⇒ 请求侧的明文凭据
> fail-closed 被**静默**关掉。配置项只允许加规则，不允许顶掉出厂默认。

### 外传目的地侧的三档

| 配置 | 效果 |
|---|---|
| 不设（默认） | 工具参数里出现公网主机/IP/`password=` 这类形状命中，**一律不终止回合** |
| `egress:builtin.egress_sink` | 只拦**真的外传**：请求体、上传文件、自定义头、把本地数据拼进请求（`$(...)`/变量插值）、以及发送型客户端（nc/ssh/scp/mail 等）。下载、clone、拉镜像照常 |
| `egress:builtin.network_destination` | 恢复旧行为：参数里**提到**公网主机就拦（连下载一起拦） |

`egress:pii.server_ip` 单独控制「公网 IP 字面量」那条规则（与 r1013 契约对齐，与方向无关）。生产 YAML 路径上的规则 ID 用 `category.name`（如 `pii.server_ip`、`secret.password.assignment`），内建回退规则的 ID 为 `pii.phone` / `pii.id_card` / `pii.email` / `pii.credit_card` / `secret.api_key`，凭据字段硬阻断统一是 `builtin.secret_field.egress`。

> 2026-10-20 起默认不拦 egress/output：211 生产事故里，模型要 curl 下载 Android SDK 镜像，Bash 参数里的公网主机被整条工具调用拦掉，流被终止，客户端只看到「模型未返回任何内容」（`empty_model_response`）。「提到公网主机」不等于「把数据送到公网主机」。

## 回滚

在版本历史选择目标、比较配置、填写原因并确认回滚。回滚创建新的递增版本，记录`rollback_of`，不会改写历史。各网关继续通过相同通知和轮询通道收敛到新版本。

也可在已配置网关地址、管理令牌及数字版本号后使用一行命令：

```bash
jq -n --argjson base "$BASE_VERSION" --argjson target "$TARGET_VERSION" '{base_version:$base,target_version:$target,comment:"人工回滚"}' | curl -fsS -H "Authorization: Bearer $ADMIN_TOKEN" -H 'Content-Type: application/json' --data-binary @- "$GATEWAY/api/admin/sanitize-rules/rollback"
```

迁移866新增版本、当前指针、复核事件和实例表。安装器、部署迁移通道及台账同步登记；配套`.down.sql`用于卸载表结构，会删除规则历史，日常策略回滚应使用上述版本回滚。
