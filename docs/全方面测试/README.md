# LLM Gateway 全方面测试体系

> 统一执行入口：功能场景、并发阶梯、性能基线、可靠性长稳和独立路由回归。历史报告只作背景，不作为当前验收证据。

## 当前可执行矩阵

| 套件 | 入口 | 覆盖 | 结果目录 |
|---|---|---|---|
| 功能场景 | `bash scenarios/run_all.sh --suite functional` | S01–S23（S21 未实现时为 `SKIPPED`） | `results/runs/<run_id>/` |
| 并发 | `bash scenarios/run_all.sh --suite concurrency` | C01 steady + burst | 同上 |
| 性能 | `bash scenarios/run_all.sh --suite performance` | P01 非流/流、P50/P95/P99、TTFB、SSE 完成率 | 同上 |
| 可靠性 | `bash scenarios/run_all.sh --suite reliability` | R01 长稳探针、故障阶段、恢复与清理 | 同上 |
| 全量 | `bash scenarios/run_all.sh --suite all` | 上述套件 | 同上 |
| 路由回归 | `tests/routing/run_all_tests.sh all` | 独立 Layer 1–3 测试 | `tests/routing/` 日志 |

## 严格结果规则

- 每个结果必须包含 `schema_version`、`run_id`、`scenario`、`category`、`status`、`checks`、`metrics`、`evidence`、`failures`、`parameters`。
- `PASS` 要求所有 `checks` 为布尔 `true`、`failures` 为空，并通过 `tools/validation_report.py` 的场景 gate。
- `FAIL` 表示网关行为或测试断言失败；`BLOCKED_ENVIRONMENT` 表示依赖、Schema、隔离或资源门禁失败；`INVALID` 表示结果缺失或契约损坏；`SKIPPED` 表示能力未实现或显式跳过。
- 默认 `SKIPPED`、`BLOCKED_ENVIRONMENT`、`INVALID` 都阻断发布验收；`--allow-skipped` 只允许非发布报告，不会隐藏 `FAIL` 或 `INVALID`。
- `202 Accepted` 是异步控制响应，不计为业务 `200 OK`；预期失败场景按错误码、错误类型、响应时延和无上游证据验收。
- 报告只读取当前 run 的 `manifest.json`，`results/` 下的历史 JSON 不会自动混入。

## 执行前门禁

默认测试必须使用本地 PostgreSQL、Redis、Gateway 和 mock，不得把生产数据库、公网 admin 或远端地址作为默认依赖。

```bash
# 初始化本地 fixture（按实际环境填写密码）
PGPASSWORD="$PGPASSWORD" psql -h "${PGHOST:-localhost}" -p "${PGPORT:-5432}" \
  -U "${PGUSER:-llm_gateway}" -d "${PGDB:-llm_gateway}" \
  -f docs/全方面测试/data/seed.sql

# 快速功能验证（必须显式使用 bash）
bash docs/全方面测试/scenarios/run_all.sh --suite functional --fast

# 并发/性能/可靠性 smoke
C01_DURATION=10 C01_BURST_DURATION=5 \
  bash docs/全方面测试/scenarios/run_all.sh --suite concurrency
P01_DURATION=10 \
  bash docs/全方面测试/scenarios/run_all.sh --suite performance
R01_DURATION=30 R01_FAULT_DURATION=5 \
  bash docs/全方面测试/scenarios/run_all.sh --suite reliability

# 发布前全量：不使用 --allow-skipped
bash docs/全方面测试/scenarios/run_all.sh --suite all
```

## 证据与覆盖边界

- `tools/loadtest.py` 采集 200/非 200、错误分类、P50/P95/P99、TTFB、SSE chunk JSON 解析、`[DONE]`、取消和吞吐。
- C01/P01/R01 已具备可执行负载与严格结果，但 CPU、RSS、goroutine、DB pool、Redis latency 等资源指标仍依赖 Gateway `/metrics` 或外部采集；缺失时必须标记未采集，不得宣称容量已验证。
- S17 必须证明真实流式取消、pending 创建、同租户恢复和跨租户隔离；仅有 endpoint 404 不能算通过。
- S18/S19/S20/S22/S23 的数据库、租户、总结断言需要对应本地 Schema、测试 key 和 admin 权限；依赖缺失应为 `BLOCKED_ENVIRONMENT`。
- S21 分支会话当前未实现，默认只能是 `SKIPPED`，不能计入“全场景通过”。
- 多模态与压缩专项使用独立 fixture 和入口，不自动计入主 suite；其客户端/报告器未齐全前不得宣称专项完整通过。
- S60 跨环境测试仅在显式环境中执行；仅验证 Nginx→Mock 链路的结果只能标记 `PARTIAL_PASS`，不能替代 Gateway 稳定性验收。

## 结果查看

```bash
cat docs/全方面测试/results/runs/<run_id>/REPORT.md
python3 docs/全方面测试/tools/validation_report.py \
  --results docs/全方面测试/results/runs/<run_id> \
  --manifest docs/全方面测试/results/runs/<run_id>/manifest.json \
  --format json
```

## 文档索引

- `00-总览.md`：场景与套件总览
- `02-测试环境部署.md`：本地环境、数据库和 mock 准备
- `03-测试场景定义.md`：S01–S23 业务场景
- `05-执行流程.md`：执行顺序、清理和中断处理
- `06-验收标准.md`：机器可计算的验收规则
- `07-故障类型矩阵.md`：mock 故障与场景映射
- `routing-test/README.md`、`routing-test-guide.md`：路由专项说明
- `2026-08-06-测试审计与整改.md`：审计结论、证据边界和后续工作
- `results/REPORT-S20-S23.md`：历史专项报告，仅供背景参考

**文档状态**：2026-08-06 审计整改版。
