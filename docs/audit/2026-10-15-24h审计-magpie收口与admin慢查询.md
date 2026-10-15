# 2026-10-15 24h 审计轮 —— magpie P0 收口合入 + admin 慢查询根修

> checkout：`llm-gateway-go-3`。基线 `origin/main` 于开跑时为 `149d6fc91`。

## 一、分支面

| 窗口 | 结果 |
|---|---|
| 24h 内、≥1h 未活动、未合并远程子分支 | **0** |
| 本地已合并陈旧分支（ahead=0） | 10 条，本轮删除 |
| `cursor/magpie-p0-closeout` | 活跃（<1h），含 P0-B/C/D 收口 + 判据；已 `--no-ff` 合入 main |
| `r43-fresh-install-prereqs-and-startup-gates` | 窗口外（9-30）、ahead=1；含删 704/705 embed 的危险 diff，**留置不合并** |

## 二、24h main 提交批判摘要（按主题，非逐 SHA）

1. **magpie P0 / streaming**（`ed620001e`→`9ea45f7f8`+判据）：CoolUntil 单飞、quotaHeld、breaker×PenaltyOf、Anthropic 剥参契约——方向正确；本轮抓到进程级 `defaultMemoryProbeOneFlight` 跨测试污染（见 §三 D1）。
2. **S4 / compression r1031**（`9a66472f1`/`6db2d18ed`）：L2 复水 hash 与审计门——与本轮日志无新冲突。
3. **迁移 863/864**：handoff 形态自适应 + `model_aliases` 部分索引——生产台账已有 860–864。
4. **URSM persist 留存**（`24dd83c14` 等）：DROP 互斥与 30→7d——文档续写正确；`REFRESH routing_analytics_7d` 仍是 pg_stat_statements 总耗时冠军（既有登记）。
5. **mobile 权限档连批**：UI 接入为主，服务端角色下限债登记在 commit message——本轮未展开。
6. **标题/$2 + liveness retired**（`f1d31b580`）：生产未再扫到 `unused argument`；liveness 噪声需部署后确认。

## 三、本轮真缺陷与修复

### D1 — probeOne 进程级单飞污染包测（P0 收口回归）

- **现象**：`TestExecutor_NoCandidateProbe_HonorsClientCancel` 单独绿、与同包 NoCandidate/ProbeOne 连跑红（ProbeSync never called）。
- **根因**：`defaultMemoryProbeOneFlight` 进程单例 + `TryAcquire(..., holdTimeout)` 无 Release → 同 model 锁跨测试残留。
- **修**：per-Executor 惰性 `MemoryProbeOneFlight`；删进程单例；钉子 `TestDefaultProbeOneFlightIsPerExecutor`。

### D2 — `/api/admin/sessions/online` 5s 超时 500（生产）

- **现象**：154 journal `session_online.go:133` `context deadline exceeded`，dashboard 连带 500。
- **根因**：`session_last_requests` ⋈ `request_logs_with_current_month`；停写后视图臂 `id` 对 session 行为 `NULL::bigint`，slr 表 **0 行**，EXPLAIN ANALYZE **52.8s / 0 行**。
- **对照**：`session_summaries` + `idx_session_summaries_tenant_time` 近 1h 窗 **0.76ms / 180 行**。
- **修**：online 列表改读 `session_summaries`（1h 窗 + tenant + cursor）；inventory/cutover 登记出列；源码钉子。

### D3 — `/api/logs/top-models` 10s 超时 500（生产）

- **现象**：dashboard 3d 窗 `duration_ms≈10014` → 500。
- **背景**：`REFRESH MATERIALIZED VIEW CONCURRENTLY routing_analytics_7d` mean≈26s，与短查询争用。
- **修**：≤8d 窗改 `SessionFamilyTurnsHotSourceSQL()`（仅 hot 腿）；长窗仍全族。钉子更新。

## 四、系统/DB 日志其它观察（登记不修或非本轮）

| 信号 | 判定 |
|---|---|
| `session_cache: db load error` / `no rows` | 冷会话 miss，WARN 噪声；非故障 |
| `fp_slot_saturation_critical` claude-opus | 既有指纹槽；owner |
| `model alternatives lookup` timeout | 与视图/争用同族；已有会话族迁移测试，留观 |
| `REFRESH routing_analytics_7d` 总耗时第一 | 既有 URSM/MV 债；非请求热路径本次未动调度 |
| `analyze_llm_gateway_table_stats` mean≈140s | 产品作业 |
| 154=`2637-6db2d18e` / 245=`2634-f280c9dc` | **本轮代码未部署**；上线后 online/top-models 需复验 |

## 五、验证

```text
CGO_ENABLED=0 go build ./...                         # RC=0
CGO_ENABLED=0 go vet ./...                           # RC=0
CGO_ENABLED=0 go test ./domains/streaming/executors/ # RC=0（含污染复现组）
CGO_ENABLED=0 go test ./admin/ ./db/ -run 'TestSessionsOnlineSQL|TestListTopModelsSQL|TestRequestLogsReadInventory|TestViewArmCutover'
bash tests/observe_magpie_p0_contract_test.sh        # ALL PASS
```

生产 SQL 对照（252 `pg-252-pg17` / `llm_gateway`）：见上文 EXPLAIN 数字。

## 六、下一轮提示词

1. ★ **部署本轮**（154/245/211）：magpie P0-B/C/D + D1/D2/D3；复验 `/api/admin/sessions/online` 与 `/api/logs/top-models` 亚秒、dashboard 无 500。
2. **MV REFRESH 争用**：`routing_analytics_7d` 降频或 CONCURRENTLY 窗口错峰（URSM §10.110 续）。
3. **`r43-fresh-install`** 门/脚本是否拣有用文件合入（禁止带删 704/705）。
4. **model alternatives** 超时与 nvidia probe 502：owner。
5. 窗口外九月远端三分支留置。
