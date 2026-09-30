# Handoff: hotzone F5 批判式审计轮（2026-10-01）

> 接力对象：llm-gateway-go-3 `main`（多克隆同 remote，接手前先 `git fetch` + `git log --stat` 核实时态）
> 关联审计：`docs/audit/2026-09-30-hotzone-p3p4-critical-audit.md`（§三 F5 / F5-R1~R3、§六）

## 一、本轮做了什么

1. **核查 handoff 接力文档的三条待办 → 全部过期，零可执行内容。**
   摘要给的 commit `8c70ff333` 已被其他轨道推进；三个失败用例
   （`TestSQLReadGuardWhitelistCurrent` / `TestStrictProxyTransportBlocksOverseasWithoutProxy` /
   `TestDiscoverGateways`）实测**全绿**，已由 `9b0801f83` 修复。**没有按摘要动手改任何一条。**
2. **修并行会话遗留的编译中断**：`admin/tenants.go` 的 `logsTable` 字符串缺收尾反引号 →
   `go vet` 13 处语法错误。最小修复（补一个反引号）。该修复后被另一会话的提交
   `935be9493` 一并带入并已进 origin/main。
3. **F5：admin HTTP ingest 接热区镜像**（owner 拍板"接镜像"）。首版实现（`f9947d3a3`）已完成并入 main。
4. **对 F5 做批判式自审 → 抓到首版一个真实缺陷并修复**（见 §二 F5-R1）。
5. **拉取整合时撞上 origin/main 编译级破坏 → 顺带修复**（见 §二 P0）。

> **P0（已修，`d2ff2c3b1`）**：`317f28556`（S4 会话族读路径迁移批次）把
> `loadSessionDetailDataInTx` 的 timeline 读取抽到 `loadSessionTimelineInTx`，删掉局部
> `rows` 变量却把调用点的 `if err := rows.Err()` 留在原地 →
> `admin/session_panorama_handler.go:179: undefined: rows`，**origin/main 无法编译**
> （`b41689139` / `317f28556` 均已在远端）。本轮 `git pull` 后立即撞上，不修则任何内容
> 都推不上去。修法：删悬空块 + 去掉随之无引用的 `"fmt"`。**未削弱检查**——错误传播由
> `admin/session_timeline_query.go:77 return timeline, rows.Err()` 承担，调用方原样上抛。
> **教训**：同 remote 多克隆并发下，`git pull` 后的树必须先 `go build ./...` 再提交；
> 别人的红会以「我的提交被 non-fast-forward 拒绝」的形式出现，容易被误判成网络问题。

## 二、结论/根因

| 编号 | 判定 | 根因 |
|---|---|---|
| F5 | 已修（`f9947d3a3`，单测级） | admin ingest 是 `request_logs_bodies_hot` 第三落库点，方案 §3-H3 漏列 |
| **F5-R1** | **首版缺陷，本轮已修** | 首版把镜像投递放在 `persistRequestLog` 入口且**不接** S4 停写门 `storage.request_logs_write_enabled`；telemetry client 侧明确接了。镜像写的是同一批正文 → 运维关停该键止血时，admin 镜像仍落盘 = "停写却仍落盘" |
| F5-R2 | 留档待拍板，**本轮未改** | `admin.keepAllBodies()` 是死代码（定义即无调用），其文档声称"只留失败行省 ~90% 磁盘"，但 admin ingest 实际全量落正文。改它影响生产落库量 |
| F5-R3 | 留档 | 同一 `request_id` 经 client 与 admin HTTP 双写时，镜像两次落不同 tenant 目录；PG 侧有 `ON CONFLICT DO NOTHING`，镜像层无跨写方去重 |

**F5-R1 的通用教训**：同一语义门在两处调用点各自内联 settings 键字面量 = 分裂入口。
三个镜像消费方接入时，凡复用同一 settings 键的判定一律抽 helper 收口，并加
"键字面量只出现一次"的结构性钉桩 —— **行为用例钉不住这类缺陷**。

## 三、改动文件与关键行为

本轮（审计修正）：

- `admin/telemetry.go` — 新增 `requestLogsWriteEnabled()` helper（S4 键字面量收敛为唯一一处）；
  原内联门一并改用 helper；镜像投递纳入 `if requestLogsWriteEnabled()` 同键同门。
- `admin/telemetry_ingest_body_mirror_test.go` — 新增 `TestF5AdminIngestMirrorHonorsStopWriteGate`
  （判"门关零投递"，非"门开有投递"）+ `TestRequestLogsWriteEnabledSingleKeyLit`（结构性钉桩）；
  既有 F5 用例显式钉住"门开"，脱离 `settings.Global` 环境默认。
- `admin/providers_refresh_test.go` — 删过期 `//nolint:unused`（`strPtr` 现被 3 个测试文件使用）。
- `docs/audit/2026-09-30-hotzone-p3p4-critical-audit.md` — F5 行补 S4 门修正与**验证等级**；
  新增 F5-R1/R2/R3 三行发现；状态行与 §六 遗留清单同步。

前序（F5 本体，已在 main）：

- `storage/file/request_mirror.go` — 新增 `ConvertBodyPayload` / `MirrorablePayload` 作镜像载荷
  契约的**单一事实源**；头注接线状态补第 3 条。
- `domains/hooks/observability/telemetry/client.go` — `strPtrToJSON` / `mirrorableBody` 转为
  转调上述单一事实源（消除镜像侧与落库侧各写一份的失配面）。
- `cmd/gateway/main.go` — `admin.SetIngesterBodyMirror(storageRt.bodyMirrorFn())` 接线。
- `admin/telemetry.go` — `SetIngesterBodyMirror` / `mirrorRequestBodies`（入口投递，仅 req/resp，
  tenant 取 `nonEmptyDefault(TenantID)` 与同事务 PG 行同源）。

## 四、测试命令与结果

```
go build ./...                      # ✅
go vet ./...                        # ✅
go test ./...                       # ✅ 0 失败
go test ./admin/ -run "TestF5|TestRequestLogsWriteEnabledSingleKeyLit|TestTelemetryIngestRequestLogStopWriteGate" -count=1 -v   # ✅ 全绿
cd web && pnpm build                # ✅
cd web && pnpm test                 # ✅ 150 文件 / 1091 用例
```

**变异验证**（门是否真的有守卫力）：

| 变异 | 结果 |
|---|---|
| 摘掉 `persistRequestLog` 里的入口投递 | ✅ 转红（4 子用例） |
| 摘掉镜像调用的 S4 门 | ✅ 转红（`gate_off_零镜像投递`） |

两次还原后均 `diff -q` 逐字节确认一致、无残留。

## 五、遗留风险（实话说）

1. **F5 无部署级实证。** 只有单测 + build/vet/web。未在任何环境跑通真实 ingest 流量并读镜像文件。
   **不要按"已闭环"对外表述。**
2. **`/api/telemetry` 真实流量未取证。** 仓内无生产者（外部 HTTP 入口），该端点是否有真实流量
   本身未验证。若长期零流量，F5 属"接线正确但无收益"。
3. **admin 侧"PG 停机仍落镜像"是推断。** 该性质只有 telemetry 侧被 E2E 演练 O5 实测过，
   admin 侧未测。
4. **F5-R2 未修。** admin 侧正文落库量与文档描述的策略不符，F5 使其放大。
   当前唯一生效的止血手段是关停 S4 键。
5. **同 remote 多克隆并发**（go-2/go-3/go-4/cursor）：本轮开工时我的未提交改动曾被另一会话
   代为提交（`f9947d3a3`）；开工前务必 `git status` + `git log --oneline -3` 核实时态。
6. **既有测试缺口（本轮未补）**：`loadSessionTimelineInTx` 迁出后，调用方
   `loadSessionDetailDataInTx` 的 timeline 迭代错误传播**无测试覆盖**。检查本身在
   （`session_timeline_query.go:77`），但没有门钉住它。
7. **`/api/telemetry` 是外部 HTTP 入口**（`admin/handler.go:1211`，admin 鉴权），仓内无生产者。
   任何「它有真实流量」的判断都需环境侧取证，不能从代码推断。

## 六、下一轮入口

1. 接手先核实时态：`git fetch origin && git status --short && git log --oneline -5`。
2. **要收口 F5，需部署级验证**（本轮未做）：按
   `docs/audit/2026-09-30-hotzone-e2e-deploy-drill.md` 的手法，在一个环境
   （245/154 任一）显式 `LLM_GATEWAY_STORAGE_MODE=full` + 热区开启 → 打真实
   `/api/telemetry/request-log` 流量 → gunzip 读 `data/hotzone/requests/{tenant}/` 下文件
   → 确认两件套落盘；再 `storage.request_logs_write_enabled=false` 重启对照
   确认**镜像同步停**（F5-R1 的部署级验证）。**需 env-injector 注入 + owner 授权
   有副作用操作。**
3. **F5-R2 需 owner 拍板**：`keepAllBodies()` 死代码是接（恢复"只留失败行"）还是删（承认全量留存）。
4. 若建对账脚本：F4 口径豁免 null/{} 行 + F5-R3 的多写方去重 + v2 以 tx.Commit 为界 /
   telemetry-admin 以入口为界的**不同向**孤儿语义，按"镜像可能多于 PG"单向容错。
