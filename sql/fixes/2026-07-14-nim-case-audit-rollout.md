# 2026-07-14 NIM 模型大小写 / 模型名规范化 — 部署顺序

> 适用范围：NVIDIA NIM provider 上的 `glm-5.2 / minimax-m3 / minimax-m2.7`
> 收到 `model_not_found` 问题，以及"网关对客户端/供应商模型名大小写一致性"
> 的全局整改。详细背景见 `docs/changelogs/2026-07-13-glm52-outbound-modelname-drift.md`。

## 1. 前置条件

- 252 上 `pg-252-pg17 / llm_gateway` 可达；备份已完成。
- 仓库当前 `main` 分支已合入：
  - NIM per-candidate outbound 修复（`domains/streaming/executors/executor_chat.go`、
    `domains/streaming/handler.go` 等）
  - 全局大小写规范化（`modelname.CanonicalizeClientModel`、
    `provider/client.go`、`resolve/resolve.go`、
    `discovery/discovery.go`、`modelcatalog/upsert.go`、
    admin `routing.go / logs.go / models.go / probe_history.go / credential_monitor.go / credential_success_rate.go / provider_vendor.go`）
  - 数据迁移 `394 / 395 / 396 / 397`
  - 防回归巡检脚本 `sql/fixes/check-nvidia-nim-outbound-model-id-drift.sh`

## 2. 数据库首轮更新（部署前）

按以下顺序应用三个迁移；任意一步失败则回滚：

```bash
# 一键顺序执行 + 前置预检 + 后置校验
PGPASSWORD=xxx ./sql/fixes/apply-nim-case-fixes.sh
```

等价于手动执行：

```bash
PGPASSWORD=xxx psql -v ON_ERROR_STOP=1 -f sql/migrations/startup/394_nvidia_nim_outbound_model_id.sql
PGPASSWORD=xxx psql -v ON_ERROR_STOP=1 -f sql/migrations/startup/395_provider_models_canonical_raw_name.sql
PGPASSWORD=xxx psql -v ON_ERROR_STOP=1 -f sql/migrations/startup/396_model_aliases_canonical_lowercase.sql
```

`394` 修 NIM 上 `outbound_model_name` 与 `raw_model_name` 的版本错位
（6 条 `z-ai/glm-5.2` → `glm-5.1` 类型的 drift），
`395` 新增 `provider_models.canonical_raw_name` 列并回填 + 加
`UNIQUE (provider_id, canonical_raw_name)` 索引，
`396` 把历史 mixed-case 的 `model_aliases.raw_name` 与
`models_canonical.canonical_name` 一次性下转小写并解决冲突。

## 3. 系统部署

```bash
git pull --ff-only origin main
./deploy/deploy-245.sh prod  # 或运维约定的发布命令
```

部署后关键校验：

```bash
# 1. 网关已重启并加载 modelname.CanonicalizeClientModel
ssh <host> 'systemctl status llm-gateway | grep Active'

# 2. NIM 三个目标模型走通
curl -fsS https://llmgo.kxpms.cn/v1/chat/completions \
  -H 'Authorization: Bearer <API_KEY>' \
  -H 'Content-Type: application/json' \
  -d '{"model":"glm-5.2","messages":[{"role":"user","content":"ping"}],"max_tokens":8}'
# 抓包：trace 上游应收到 "z-ai/glm-5.2"

curl ... model=minimax-m3 ... # 上游应收到 minimaxai/minimax-m3
curl ... model=minimax-m2.7 ... # 上游应收到 minimaxai/minimax-m2.7

# 3. 巡检
PGPASSWORD=xxx ./sql/fixes/check-nvidia-nim-outbound-model-id-drift.sh
# 预期: ✅ 无 drift
```

## 4. 系统正常后第二轮数据库更新

新版本网关从这一刻起，所有"客户端入站 + 模型发现写入"
路径都已经走 `modelname.CanonicalizeClientModel` / `lower(...)`，
**新写入的 `request_logs` / `model_aliases` / `model_offer_events` /
`model_probe_state` / `credential_model_stats_1m` 等运行表**全部是
小写。但仍有历史数据是 mixed-case —— 等到系统稳定（建议部署后
24 小时以上，让 in-flight session / 重试完成；或者更保守一点
到下周），执行第二轮迁移：

```bash
PGPASSWORD=xxx psql -v ON_ERROR_STOP=1 -f sql/migrations/startup/397_runtime_logs_lowercase.sql
```

`397` 包含：

- `request_logs.client_model / outbound_model` 下转小写
- `model_aliases.raw_name`、`model_offer_events.raw_model_name`、
  `model_probe_state.raw_model_name`、`credential_model_stats_1m.raw_model`、
  `credential_model_peak_1m.raw_model`、
  `credential_model_call_history.raw_model`、
  `candidate_failure_logs.raw_model_name` 一并规范化

`397` 内含详细的 pre/post-flight snapshot；执行结束后任何
列仍出现 mixed-case 计数 > 0 的告警应该被立刻调查。

## 5. 防回归

- `sql/fixes/check-nvidia-nim-outbound-model-id-drift.sh` 接入 daily cron
  （参考 `docs/2026-07-09-glm52-fp-slot-audit-followup.md` 的 cron 配置），
  退出码 1 / 2 触发告警。
- `sql/migrations/startup/394 / 395 / 396 / 397` 都已加入
  `pre-commit-check.sh` 与 `migrate-db-kaixuan1.sh` 的迁移清单。
- 代码侧：
  - `modelname/normalize_test.go::TestCanonicalizeClientModel_AlwaysLower`
    保证所有客户端入口都收敛到 lowercase。
  - `modelcatalog/upsert_test.go::TestUpsertSQL_LowercaseContract` 保证
    SQL 中不能再次出现 `lower(raw_model_name)` /
    `lower(canonical_raw_name)`，并要求 `canonical_raw_name` 列始终存在。
- 部署回滚（如必要）：
  - `395` 回滚：先 `DROP INDEX` 然后 `ALTER TABLE ... DROP COLUMN canonical_raw_name`，
    重新部署一个 *旧版* 网关即可（不会读取 `canonical_raw_name`）。
  - `396 / 397` 回滚：上一轮的 mixed-case 数据已经消失，但可以从
    `request_logs_archive` / `model_aliases_archive` 等备份表回灌；
    这次部署前后**务必**完成一次 `pg_dump`。

## 6. 长期维护 (2026-07-14 部署后)

### 6.1 已安装的 245 cron 任务

部署到 245 时已自动安装 systemd timer：

```
/opt/llm-gateway-go/ops/nim-case-cron.sh      # 主脚本（local-only 健康检查）
/opt/llm-gateway-go/ops/nim-case-cron.service
/etc/systemd/system/nim-case-cron.{service,timer}
```

每天 04:30 自动运行，输出 `/var/log/llm-gateway-go/nim-case-cron.log`。
最近一次手工触发：exit 0；`Wed 2026-07-15 04:30:00 CST` 下次自动运行。
检查项：healthz / version / postgres disabled / memory / discovery / WARN 计数。

### 6.2 跨机器 drift 巡检 (在 252 上跑)

`sql/fixes/check-nvidia-nim-outbound-model-id-drift.sh` 跑在 252 容器里
(`REMOTE_MODE=1` + `docker exec pg-252-pg17`)；由人工 / 部署脚本触发：

```bash
ssh root@115.29.212.252 "set -a; . /opt/pms-dev/.runtime-secrets/infra.env; set +a; \
  bash /tmp/check-nvidia-nim-outbound-model-id-drift.sh"
```

未来若要在 245 自动跑，可加 245→252 的 SSH 密钥到 `/root/.ssh/`，然后把
上面的 ssh 嵌入 nim-case-cron.sh。

### 6.3 列存表历史 mixed-case 清理

`sql/fixes/normalize-columnar-historical.sql` 是一份**手操工具**，需要在维护窗口运行。
它会：
1. 把 `candidate_failure_logs` 临时 rename → 新建 rowstore 表 → `INSERT...SELECT` → DROP 旧表
2. 对 `request_logs` 每个 columnar 分区做相同流程（外加 `ATTACH PARTITION`）
3. 然后 `columnar_ensure_columnar()` 还原

风险：转换期间阻塞写入，**仅在月度分区轮换时跑**。

### 6.4 154 生产部署

245 验证通过后，154 走相同流程：

```bash
PGPASSWORD=... ./sql/fixes/apply-nim-case-fixes.sh --phase a   # 同样适用于 154
PGPASSWORD=... ./sql/fixes/apply-nim-case-fixes.sh --phase b   # 245 部署后 ≥24h 跑
bash scripts/deploy-154.sh                                    # 用 154 现有 runbook
```

154 的 cron / drift 同样按上述 6.1 / 6.2 接入即可。
