# E 安全卫生横向域子代理报告（窗口：383c4b976..3909d56e0）

> R73 审计轮只读子代理原文。主代理处置：E-1 登记为 owner 遗留（754 头注钉死两个
> 语义依赖，理由：月表无 Go 读路径 + 754 已在真库应用 + RLS 语义待真库 EXPLAIN 实证）；
> E-2 已修（TrimSuffix + 表驱动用例）；E-3 关闭（M-10 证伪）；E-4 部分采纳（-race
> 本轮实跑 sessionv2mirror）；E-7 已标 deprecated。

## 一、发现（候选，待主代理复核）

| # | 级别候选 | 发现 | 证据 file:line | 建议处置 |
|---|---|---|---|---|
| E-1 | P2 候选 | **754 归档目标表绕过租户隔离惯例**：动态建的 request_logs_archive_YYYY_MM 系裸表——无 RLS 无 tenant policy，而 request_logs_archive 父表族有 ENABLE RLS 惯例。次生：源分区读取靠直查分区名躲过父表 FORCE RLS；若后人改走父表，bg 会话 tenant='default' 会静默漏读非 default 行。分区直查绕过父表 policy 属 PG 语义**待亲验** | 754 SQL:173-198,210-242；sql/objects/tables/request_logs.sql:158；sql/objects/policies/request_logs_archive.sql:5 | 补 RLS/policy 或头注钉死直查语义依赖 → **主代理裁决：头注登记（月表当前无 Go 读路径；754 已在真库应用不宜改 SQL；RLS 加固需连同读方角色设计走 owner 决策）** |
| E-2 | P3 候选（存量触包） | **upstreamurl.ModelsURLCandidates cutset 误伤**：`TrimRight(normalized, "/v1")` 是字符集裁剪——`https://gw1/v1` 的 root 变 `https://gw`，探针打错主机。4 个活跃调用方。窗口新增 fuzz 只覆盖 Build 漏掉此函数 | internal/upstreamurl/upstreamurl.go:287 | TrimSuffix + 表驱动用例 → **已落地（R73）**；fuzz 覆盖扩展登记 |
| E-3 | Info（M-10 证伪） | 「args 空传 nil → jsonb 写 NULL」实际不可达：`Command.Args` 是 map，json.Marshal(nil map) 返回 "null" 4 字节非空切片，len>0 守卫恒真——现状写 jsonb 'null' 而非 SQL NULL；读方 no-op 无 IS NULL 过滤。改 '{}' 无收益纯属观感 | center/store_pgx.go:220-236；center/store.go:54 | **M-10 以「守卫恒真死分支、NULL 不可达」关闭**；可选 admin_api handler 归一 '{}' |
| E-4 | P3 观察项 | mirror reaper drain 无每-tick 行数预算：hook 持续写入时单 tick 可无限排空，4 worker 持续占池连接。并发面本身健康（共享字段只读、FOR UPDATE SKIP LOCKED、prometheus 线程安全）；建议 -race 标注 | internal/sessionv2mirror/replay.go:264-345,178-189 | 预算留跟进；`go test -race ./internal/sessionv2mirror/` → **R73 已实跑全绿** |
| E-5 | Info（运维面） | 指标改名断流：仓内无旧名残留，db-changelog 已有下线通知 | docs/db-changelog.md:738-744 | 无 |
| E-6 | Info（守卫强度） | admin 500 回显守卫是逐行正则可绕过、只扫 admin 包 | admin/internal_error_guard_test.go:33-60 | → **R73 已升级 v2（递归+窗口+%v 启发式）** |
| E-7 | D17 死代码候选 | **admin/session_list.go 全文件失去调用方**（NewSessionListAPI 零引用，v2 接管）；session_turns.go 同为多轨但头注已声明有活跃引用不属于死代码 | admin/session_list.go:61-445；cmd/gateway/main.go:6895-6896 | 标 deprecated → **已落地（R73，头注登记清理候选）** |

## 二、核实为健康的面

1. **E4 .gitignore 根锚定终态 check-ignore 实测通过**（cmd 源码面 exit=1、根二进制仍忽略、重复行已清）。
2. **auth_mw /mock 旁路**（窗口唯一行为改动）：全局门放行被端点级守卫完全补偿（POST+Bearer+scope 白名单+MockProbeEnabled 注册门）；token 硬编码但头注钉死「公开系统标记非机密」；R69 租户钉扎未破坏；歧义 409 不回显 + 新增 138 行泄漏断言。
3. **telemetry** 窗口仅 2 处 jsonb string 化；无外发 HTTP；写超时 5s/读 3s；ring buffer 有界；db 关闭模式 fail-closed。
4. **admin 500 回显批量收口**（audit_log/ip_blocklist/prompt_injection 30+ 处）三态 helper 齐备。
5. **apihub/types.go** 纯常量新增。
6. **vapeur live 探针密钥面**：env-only + skip；无明文；nil 防护已修。
7. **数值边界三角一致**：753 [1,168]、754 [7,365]、mirror [5,30] spec=Go clamp=SQL RAISE 三方对齐；753 清扫双界 6M 行/tick。
8. **752**：无 tenant_id 无 RLS 需求（内部探测记录）；TZ 钉扎 + move-then-attach + advisory lock；并发契约自洽。
9. **资源配对抽查**（窗口改动点）：rows.Close/defer Rollback/WithTimeout 全配对；无新增共享可变状态。
10. **installer 五点同步**：runner.go canonical 清单三条全登记。
11. **provider/client.go**：decodeNativeEndpoints 纯函数抽取 + weight 排序对齐，无注入面。

## 三、未覆盖项与原因

1. 754 直查分区 RLS 语义实证需真库 EXPLAIN（本机未跑）。
2. web/ 前端与 locale 泄漏属 D15 面（只审了 Go 侧端点）。
3. bg/node_probe +81 行与 auto-testbench e2e 逻辑属 D11/D15 面。
4. -race 全量未跑（只跑了 check-ignore 等只读命令；主代理本轮补跑 sessionv2mirror）。
5. upstreamurl 11 个历史调用点全量收编审计属 D14 既有台账。
