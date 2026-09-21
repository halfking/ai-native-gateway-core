# D16 流程/闭环（部署脚本 + no-nodes E2E）子代理报告（窗口：876302d5e..67f78247c）

改动面：scripts/deploy-local-lib.sh（be24c65c9 + e0f94a799）、scripts/deploy-local.sh、tests/mock-system-test/scenario_no_nodes_survival.py。

## 一、发现（候选，待主代理复核）

| # | 级别候选 | 发现 | 证据 file:line | 建议处置 |
|---|---|---|---|---|
| 1 | **P1** | **Layer 2 防线无效：脚本写裸数字 90，gateway 用 time.ParseDuration 解析必失败、静默回退 20s 默认**。e0f94a799 声称 "Layer 2 verified via docker inspect" 只验证了 env 变量存在，未验证解析结果。仓内同日审计文档已明确此坑 | scripts/deploy-local-lib.sh:387；cmd/gateway/main_helpers.go:92-104、:119；docs/audit/2026-09-17-prompt-too-large-413-audit.md:73 | 改为写 90s；钉桩断言 env 值带单位 |
| 2 | P2 | **dl_wait_pg_isready 的 "could not parse" 护栏是死代码**：sed -nE 无匹配退出码 0，`! user=$(...)` 永不为真；无密码 DSN/unix socket/缺库 DSN 带空错组件硬探 90s，cutover 路径最多烧 6 分钟 | scripts/deploy-local-lib.sh:632-639、:646-667 | 护栏改校验变量非空 |
| 3 | P2 | **Docker 模式 pre-flight 与实际 docker run 传参不一致，probe_host 重映射是死逻辑**：赋值仅在 DL_DOCKER=1 时发生但只被 DL_DOCKER=0 分支读；docker 分支探的是容器内网络视角；DL_DB_MODE=external 时可能 exec 进任意扫到的容器（含 stopped）；:645 日志把函数名当变量恒显 none | scripts/deploy-local-lib.sh:640-641、:645、:649-655、:671-681；scripts/deploy-local.sh:389、:785-790 | 修正日志；DSN host 非 loopback 强制走 psql/TCP 分支 |
| 4 | P2 | **scenario D 断连验证空壳**：只断 socket 不验证服务端停止重试；行为 (d) 目前只有 Go 层 e2e 在守护 | scenario_no_nodes_survival.py:316-333、:373-381 | 补服务端重试冻结断言或 docstring 明示 |
| 5 | P3 | 场景 C 不存在，--skip-budget 是 no-op；docstring 声称的凭证禁用回退无实现；commit message 说 gpt-5.6-luna 而代码默认 glm-5.2 | scenario_no_nodes_survival.py:9-12、:14-18、:428 | 删 flag/docstring 或补场景 C |
| 6 | P3 | readyz 探活子串判断失效：b"ready" in not_ready 恒匹配，WARNING 永不打 | scenario_no_nodes_survival.py:450-455 | 查精确 '"status":"ready"' |
| 7 | P3 | py 解析器拿不到 per-retry reason：str 分支只提取 attempt/wait，reason 恒 None（dict 分支是死路） | scenario_no_nodes_survival.py:143-163 | str 分支加 原因= 提取 |
| 8 | P3 | /dev/tcp fallback 误报面：TCP 握手成功即判 ready，掩盖 #1 | scripts/deploy-local-lib.sh:662-664 | 注释声明局限 |
| 9 | P3 | DEFAULT_API_KEY 硬编码（仅测试网关有效）；--interval 兼作 read timeout 基数属隐式耦合 | scenario_no_nodes_survival.py:52、:103 | 文档化或拆 --read-timeout |

## 二、核实为健康的面

- LLM_GATEWAY_DB_BOOT_RETRY_SECONDS 作用域：仅 deploy-local.sh write_instance_env 写入本地 bundle env，无 245/seamless 生产路径消费方——问题只在值无效，不在泄漏。
- cutover 2→3 + 5s stagger 后回滚路径结构完整（active_bundle 原子切换前捕获；三次全失败回滚旧 release）。窗口前已存在的残留：回滚重启不跑 verify_instance；start_instance 内 die 绕过回滚；回滚 env 重生成使旧 bundle SHA256SUMS 失真。
- think 线格式与 py 解析兼容（attempt/wait 可解析）。
- 规范生成的 DSN（postgresql://user:pass@127.0.0.1:5432/db?sslmode=disable）五组件全解析正确。
- pre-flight 失败语义：`|| true` 显式设计，set -e 下无静默吞没；e0f94a799 后成功路径也有 log。
- 90s 预算与 7-probe 无不一致（纯时间上限 2s 间隔，7 probes 只是当次实测）。
- Go 层 e2e 有真断言兜底，py 层缺口不等于行为无守护。
- attempt_outcome.go reason enrichment 向后兼容。

## 三、未覆盖项与原因

- 未实际运行 deploy-local.sh / py 脚本（只读审计且无本地网关/docker 环境）；#1 依据 Go 标准库语义 + 仓内审计文档双重佐证，建议主代理复核。
- macOS Docker Desktop loopback-publish 可达性未实测（vpnkit 平台相关），#3 的 Linux 侧风险为推理。
- e0f94a799 commit message 自登记的 717:37 customer_id cast bug 不在本域三文件范围，需另派域核查。
