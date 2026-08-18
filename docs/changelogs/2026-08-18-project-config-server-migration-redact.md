# 2026-08-18 — PROJECT_CONFIG.md 服务迁移 184→154 + 凭据脱敏

> 把"操作手册级"文档（AI 每次会话首读）从已废弃的 184 server 切换到当前 154 生产网关，
> 并把残留的明文密码占位为 `<env:KEY>` 引用（rule 39 / rule 47）。

## 1. 做了什么

1. **服务端信息**：SSH `14.103.112.184:25022` → `<env:HOST_154>:25022`；
   默认用户 `admin` → `root`；明文密码 `Veritrans&9527` / `Kaixuan2026&#*9527`
   → `<env:SSHPASS>` 占位符。
2. **数据库配置**：移除"通过 SSH 隧道连 127.0.0.1"叙述，改"直连 252 内网"；
   `postgres` 用户 → `<env:COMMON_PG_SUPERUSER>` (= `llm_gateway`)；
   新增 `<env:COMMON_PG_SUPERUSER_PASS>` 密码占位。
3. **服务器部署命令示例**：`ssh admin@14.103.112.184` + `sudo su -` + 明文 root 密码
   → `ssh -i <env:SSH_KEY_154> root@<env:HOST_154>` + sshpass -e fallback。
4. **数据库操作示例**：移除 SSH tunnel 步骤，改为 `PGPASSWORD=<env:COMMON_PG_SUPERUSER_PASS> psql ...`，
   并附 `.pgpass` 推荐写法（env-injector 注入一次后 psql 自动读取）。
5. **文件头警告**：原"包含敏感信息，请勿提交到公共仓库"（与"文件已在仓内"自相矛盾）
   → 改为 "<env:KEY> 占位符 + env-injector/loader.sh 注入"提示。
6. **服务单元名修正**（附带）：`llm-gateway` → `llm-gateway-go.service`
   （与 `servers/47.97.111.154/metadata.yaml:19` SSOT 一致；之前命令会得到
   `Unit llm-gateway.service could not be found`）。

## 2. 改动清单

| 文件 | 类型 | 说明 |
|---|---|---|
| `PROJECT_CONFIG.md` | 修改 | 4 处 replace（旧 184 → 新 154 占位符）+ 单元名修正 + 文件头警告重写 |
| `CHANGELOG.md` | 修改 | 新增 `[Unreleased] - 2026-08-18 (PROJECT_CONFIG.md ...)` 段 |
| `docs/changelogs/2026-08-18-project-config-server-migration-redact.md` | 新增 | 本文档（rule 36 归档） |

净变化：`+27 -24` 行（PROJECT_CONFIG.md）。

## 3. 为什么这样做

- **rule 31 §1 强制**：154 替换 184 后，所有指向 184 的文档（特别是"AI 每次会话首读"的
  PROJECT_CONFIG.md）必须同步，否则 AI 会按错误 server 操作（SSH 超时、找不到服务单元）。
- **rule 39 铁律 1**：仓库入仓文件零明文敏感值。
  PROJECT_CONFIG.md 行 24-25 / 221 已暴露 `Veritrans&9527` + `Kaixuan2026&#*9527`，
  且后者已被 `scripts/scan-secrets.replacements:11` 列为已知泄露
  （`Kaixuan2026&#*9527==>__REDACTED_SSH_PASSWORD__`）—— 说明 git filter-repo 历史清洗
  走过，但当前文件未同步。
- **rule 47 互引**：占位符 `<env:HOST_154>` / `<env:SSHPASS>` / `<env:COMMON_PG_*>` 全部对应
  `~/workspace/ai-native-tools/envs/` SSOT（`common/ssh-keys.yaml`、`common/database.yaml`、
  `servers/47.97.111.154/metadata.yaml`），实现"代码引用占位符，env-injector 解析真值"。
- **附带单元名修正**：与 SSOT 一致性（避免命令失败误导后续会话）。

## 4. 验证结果

- **占位符 SSOT 对账**（rule 47 §6.5 + rule 49）：
  - `<env:HOST_154>` ↔ `envs/servers/47.97.111.154/metadata.yaml:1` ✓
  - `<env:SSH_KEY_154>` ↔ `envs/common/ssh-keys.yaml:4` (`~/.ssh/id_ed25519`) ✓
  - `<env:SSHPASS>` ↔ `envs/common/ssh-keys.yaml:9` (`SSH_PASSWORD_ENV_VAR: SSHPASS`) ✓
  - `<env:COMMON_PG_HOST_252>` / `_PORT_252` / `_SUPERUSER` / `_SUPERUSER_PASS`
    ↔ `envs/common/database.yaml:26-27, 9-10` ✓
- **scan-secrets.sh**（`bash scripts/scan-secrets.sh --paths=PROJECT_CONFIG.md`）：
  - 无 BLOCK；仅 8 条 INTERNAL_DOMAIN WARN（`llmgo.kxpms.cn` 等公网域名，rule 39 §5.1
    允许 public domain；不在 §5.1 8 类强制脱敏范围）。
  - **关键**：`Kaixuan2026&#*9527` 文本已从 PROJECT_CONFIG.md 完全消失（diff 验证）。
- **pre-commit-check.sh**：`PASS=4 FAIL=0 WARN=0 SKIP=2`（go vet / SQL / migration NNN /
  migration down.sql 全过；vue-tsc 与 token compliance 因 web 文件未变更自动 skip）。

## 5. 遗留与风险

- **当前 shell 中残留 `LLM_GATEWAY_ADMIN_PASSWORD=Veritrans&9527` 明文**（被 env-injector
  list 输出捕获到）：不在本任务范围，但提示此前某次部署或 shell 初始化脚本把明文密码
  export 到了环境变量。建议 owner 单独 PR 排查：哪些脚本/配置会 export 明文密码？
  是否走 `<env:...>` 占位符更安全？本次未处理，避免越界（rule 11 §1 + rule 42）。
- **公网域名 WARN（`llmgo.kxpms.cn`）**：当前 scan-secrets 阈值是 WARN（rule 39 5.1
  列为低敏感），保留原样。若未来团队把"公网域名"也升为 BLOCK，再统一治理。
- **未覆盖范围**：README.md / DEVELOPMENT_STANDARDS.md / SESSION_RESUME.md /
  docs/archive/ 中可能仍残留旧 server 引用 —— 本次仅修 PROJECT_CONFIG.md（AI 首读文件）。
  Owner 后续可按需 sweep。

## 6. 下一步建议

- **本次会话剩余**：P1 任务 B（245 deploy 验证 `3b6bdce18` reveal-metric）、P2 任务 C（告警规则回归）、
  P3 任务 D（rule 49 §9-2 view freeze 补强）。详见 handoff 文档。
- **owner 决策**：是否要建立"PROJECT_CONFIG.md 内容每周自动对照 SSOT"
  的轻量 CI 检查（参考 handoff 中提到的 `scripts/verify.sh credentials`），
  防止未来 154→新 server 迁移时再次漏改 PROJECT_CONFIG.md。