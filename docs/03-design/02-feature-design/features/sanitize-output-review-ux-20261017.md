# 敏感信息 / 输出检测：默认关闭与命中复核体验（2026-10-17）

## 1. 背景

管理页 `https://llm.itestu.cn/admin/sanitize-rules`「命中与复核」列表目前只展示合成摘要
`[REDACTED:<type>] field=…`，看不到足以确认误报的结构信息，也缺少命中规则的快速查看与修正入口。
输出合规模块（`output_compliance.enabled`）历史上默认开启，容易在本地脚本/文档还原场景下造成过度拦截观感。

## 2. 产品原则

| # | 原则 | 说明 |
|---|---|---|
| P1 | **输出合规默认关闭** | `output_compliance.enabled` 默认 `false`；未配置时按关闭。**强制外传守卫**（`OutputSensitive` / ScopeEgress）与**输入脱敏**不受此开关影响，继续防供应商侧泄露。 |
| P2 | **命中可复核、可修正** | 输入 / 输出 / egress 命中共用「命中与复核」列表；每条须能看到结构类型、字段、**命中规则快照**（表达式/样例/阶段/动作），并一键进入编辑或起草限定规则。 |
| P3 | **列表不存请求原文** | `sanitize_rule_events` 仍禁止持久化请求原文或 detector 捕获值（防二次泄露）。复核用「形状摘要 + 规则快照」；会话级可掩码映射见既有 `GET …/sessions/{id}/sanitize-matches`。 |
| P4 | **本地原样、供应商脱敏** | 客户端本地脚本/文档/代码经占位符还原后显示真值；出站供应商侧保持脱敏。Admin 输出合规记录可展示**已脱敏证据**（`evidence` 列，非 `original_output`）。 |
| P5 | **误报快速纠正** | `pending` 状态即可「查看命中规则 / 编辑实际命中规则 / 新建限定补充规则」（编辑仍仅 super_admin）；复核三态不变。 |

## 3. 范围与非目标

**做：**

1. 改 `output_compliance.enabled` 默认与运行时 fallback 为 `false`（settings / interceptor / write-time redactor / UI 初值）。
2. 事件列表 API 附带 `matched_rule` 只读快照；前端展示 type/field + 规则快览；pending 可跳转修正。
3. 输出合规命中记录 `content_preview` 回填 `evidence`（已脱敏），便于确认。

**不做（本轮）：**

- 不把请求明文写入 `sanitize_rule_events`。
- 不关闭强制 egress 守卫。
- ~~不批量改写生产库里已显式设为 `true` 的 `output_compliance.enabled`~~：**2026-10-17 落地时已将共享库 `settings_kv` 显式改为 `false`**（与「原则上默认关闭」对齐；强制 egress 守卫不受影响）。新装/未配置仍走代码默认关。

## 4. 验收

1. 无 settings 时 `outputComplianceEnabled()` / `enabled()` 为 false；显式 `true` 仍启用。
2. `GET /api/admin/sanitize-rules/events` 每条含 `type`/`field`/`matched_rule`（规则仍在对应版本时）。
3. 前端「命中与复核」：可见类型字段、可展开规则表达式与样例；pending 有编辑/起草按钮（super_admin）。
4. `GET /api/admin/output-compliance/records` 在 `redacted_output` 空时仍可用 `evidence` 填 `content_preview`；永不返回 `original_output`。
5. 相关单测绿；变异：撤掉 matched_rule  enrichment 或把 enabled fallback 改回 true 时对应判据变红。

## 5. 联带缺陷：Bash `output_policy_blocked`（r1035，2026-10-17）

154 日志取证（client `59d936b8…` / gateway `913cf85b…`）：

- `blocked stream lane` label=`__tool_json@Bash` reason_codes=`["custom"]`
- 同秒 `sanitize_rule_events`：`pii.address.egress|custom|command|block`

根因：托管规则 `pii.address.egress`（中文地址 pattern）命中 Bash 路径/文档字样；
强制外传守卫把任意 egress 命中都硬拦，打断本地执行。地址类 custom 不是外传目的地。

修复：本地执行工具（Bash/exec_command…）与惰性内容工具上，跳过
`type=custom && kind!=network_destination` 的硬拦；仍拦真正的网络目的地与 secret/公网 IP。
判据 `security/sanitize/r1035_local_exec_address_not_egress_test.go`。
