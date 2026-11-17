# llm-gateway-go · 技能包（跨智能体共享）

> 本目录把仓内历轮审计沉淀的**可复用工作方法**提炼成标准技能文件。
> 任何智能体（Claude Code / Cursor / Codex / opencode / MCP 客户端 / 人类）都能消费。

## 为什么有这个目录

本仓的知识此前散落三处，各有消费成本：

| 载体 | 规模 | 问题 |
|---|---|---|
| `AGENTS.md` | ~500KB | 只对「读全文的智能体」可见；超长后中间内容被截断 |
| `LESSONS.md` | 30K+ 行 | 通用式按 §编号 累积，检索靠人肉 grep |
| `docs/audit/*-主档.md` | 数十份 | 方法论嵌在叙事里，跨轮复用要重读全文 |

技能文件解决「**下次遇到同类任务时，智能体能直接调用方法而不是重新发现**」。

## 格式与标准

- 每个子目录一个 `SKILL.md`，YAML frontmatter（`name` / `description`）+ Markdown 正文。
- `name`：小写连字符，全目录唯一。
- `description`：第三人称写清「何时用」，这是智能体路由加载的判据，必须具体。
- 正文面向**执行者**：步骤可复现、判据可检验、坑有反例，不写叙事。
- 这是 Anthropic Agent Skills 开放格式；仓内 MCP 网关的 `Skill.Manifest`
  （`mcp/types.go`，注释「SKILL.md 语义的结构化形式」）可直接承载同一份内容，
  经 `POST /mcp/control/skills`（需 `mcp.register` scope）注册后可供任意 MCP 客户端使用。

## 各平台消费路径

| 平台 | 用法 |
|---|---|
| Claude Code / Codex / opencode 等 | 把本目录（或其中子目录）复制/软链到平台的 skills 目录即可按 frontmatter 路由加载 |
| MCP 客户端 | 经仓内 MCP 控制面注册：`POST /mcp/control/skills`，`manifest` 字段填 SKILL.md 的 frontmatter+正文 |
| 人类 / 任意智能体 | 直接读 Markdown——格式对不认识 SKILL.md 标准的读者仍然友好 |

## 技能索引

| 技能 | 何时用 | 来源 |
|---|---|---|
| [`pi-chain-audit`](pi-chain-audit/SKILL.md) | 审计/复核一条「LLM 判官 + 队列 + 后台 worker」型智能体链路是否真的接线、真的可用 | 2026-11-17 PI 智能体审计轮（49 缺陷修 31） |
| [`migration-delivery-gate`](migration-delivery-gate/SKILL.md) | 新增/修改 PG 迁移：七点登记、探针不毒丸、规则集投递面核查 | 同轮（977/978 两迁移 + 探针毒丸教训） |
| [`maestro-browser-verification`](maestro-browser-verification/SKILL.md) | 用 Maestro 对网关 UI 做逐页浏览器验证（mock 栈与线上两种形态） | 同轮（L3 九 flow + 245 线上验收） |
| [`guard-test-mutation`](guard-test-mutation/SKILL.md) | 给守门测试做变异验证：确认「改坏实现 ⇒ 测试变红」而不是装饰性断言 | 同轮（21+9+1 变异全红→还原绿） |
| [`pg-instance-sync-252`](pg-instance-sync-252/SKILL.md) | 本地 Docker PG 与 252 实例对齐：盘点→计划→增量 schema→仅插入合数据→FK 校验→回滚仅入新库 | 既有技能（提交 `a6b587a38`，db-sync 轮沉淀） |

## 维护纪律

1. **新技能必须有实战来源**：至少一轮真实审计/部署/验证轮沉淀，不写理论技能。
2. **改代码不改技能**：技能描述的是方法论；实现细节变了要回检技能是否失真（本仓「自述与实现不符」是登记在案的缺陷形态，技能自己不能犯）。
3. **来源可溯**：每个技能的 frontmatter 或正文给出出处主档路径，复核者可回查证据。
4. 索引表与本 README 同步维护（新增技能必须两处都登记）。
5. 以上由机械门禁看守：`python3 scripts/check-skills-package.py`
   （frontmatter 合法性、索引双向同步、路径引用仓内可解析；提交前必须绿）。
