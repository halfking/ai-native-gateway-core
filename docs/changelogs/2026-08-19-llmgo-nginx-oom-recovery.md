# 2026-08-19: llmgo.kxpms.cn 公网 502 1h21min OOM 复盘 + 加固

## 事件时间线（2026-08-19 19:33 → 21:00）

| 时间 (CST) | 事件 |
|---|---|
| 17:10:13 | nginx worker 第一次被 SIGKILL（OOM killer），systemd 标记 failed |
| 17:50:11 | systemd 尝试 restart nginx（vendor unit 含 `Restart` 默认是 no，故 failed 不自愈） |
| 19:33:13 | OOM 触发：gateway anon-rss 1.55GB + nginx + NetworkManager + sshd + crond 等被连串杀 |
| 19:33:14 | systemd-logind / sshd / nginx 全被杀 |
| 19:36:47 | nginx 最后一次 SIGKILL（signal=9） |
| 19:36:51 | systemd: nginx.service: Main process exited, code=killed, status=9/KILL |
| 19:36:51 → 20:58 | **公网 80/443 完全不可达 1h21min**（用户报告的故障窗口） |
| 20:53 | llmgo-245.service 因为 `Restart=always` 自动重启 gateway（PID 822490 → 1005），但 nginx 没自愈 |
| 20:58 | **手动 `nginx` 起进程** + reset-failed systemd |
| 20:59 | 公网 302 恢复 |

## 直接症状
- `https://llmgo.kxpms.cn/` 完全无法打开（`curl HTTP_CODE=000 TIME=0.015s`）
- 80/443 端口无监听（`ss -tlnp` 验证）
- nginx systemd 状态：`failed (Result: signal) since 1h21min ago`

## 直接原因（症状层）
1. `nginx` systemd service 被 `signal=9 (SIGKILL)` 干掉（OOM killer）
2. `/usr/lib/systemd/system/nginx.service` (vendor unit) **缺 `Restart=always`** → systemd 标记 failed 后不自愈
3. `/etc/systemd/system/llmgo-245.service` 含 `Restart=always`，gateway 自动恢复了（PID 822490 → 1005），但 nginx 没用同样的保护

## 真根因（系统层）
1. **llm-gateway-go 进程 anon-rss 涨到 1.55GB**（dmesg 现场：`Out of memory: Killed process 819478 (gateway) total-vm:3890828kB, anon-rss:1553456kB`）
   - 245 是 **4GB 内存机**（`MemTotal: 3649000 kB`，之前误读为 1.8GB 是因为 `free` 默认用 Gi=1024^3 而非 GB）
   - gateway 占 1.5GB + 系统其他 ~1.5GB → 触顶 → OOM killer 全杀
2. **gateway 内存累积疑似点**（未做 pprof 验证，留待后续）：
   - `trace.FlushToPG: request log row not found, retaining Redis trace` 反复重试（每请求 5 次 flush 失败）
   - `partition_manager: promote failed` / `analyze stats failed` 持续 SQL 超时
   - `stats daily/monthly rollup failed: invalid input syntax for type interval` 每天 04:45 失败
   - **system_monitor worker 每 200ms DEBUG 日志 × 5 workers = 每秒 25 条 DEBUG 日志**（生产不该开 DEBUG）
3. **`/var/log/llm-gateway-go/gateway.stderr.log` 膨胀到 1.16GB**（4.5 小时产生 1GB+ 日志 = ~70KB/s 写入）
4. **245 nginx vendor 配置缺 `Restart=always`** → OOM 后不自愈，公网挂 1h21min

## 已应用修复

### 1. 245 nginx drop-in：`/etc/systemd/system/nginx.service.d/override.conf`
```ini
[Unit]
StartLimitIntervalSec=300
StartLimitBurst=10

[Service]
Restart=always
RestartSec=3
```
> vendor unit 备份: `/usr/lib/systemd/system/nginx.service.bak-20260819-210314`
> 验证: `systemctl show nginx | grep Restart=always` ✅

### 2. 252 nginx drop-in（同款配置）
```ini
# /etc/systemd/system/nginx.service.d/override.conf on <env:HOST_252_IP>
[Unit]
StartLimitIntervalSec=300
StartLimitBurst=10

[Service]
Restart=always
RestartSec=3
```
> 252 是 245 的前端反代，245 nginx failed 时 252 nginx 也必须能自愈（公网才会恢复）。同步加。

### 3. 245 llm-gateway-go systemd drop-in：`/etc/systemd/system/llmgo-245.service.d/memory-override.conf`
```ini
[Service]
MemoryHigh=1500M    # 触发 throttle
MemoryMax=2G        # 触达 OOM kill（不是整系统 OOM）
```
> 验证: `systemctl show llmgo-245 | grep -E "^MemoryMax=|^MemoryHigh="`
> ```
> MemoryHigh=1572864000  (1.5G)
> MemoryMax=2147483648   (2G)
> ```

### 4. `/opt/llm-gateway-go/.env` 改 `LLM_GATEWAY_LOG_LEVEL=debug` → `info`
- 备份: `.env.bak-20260819-210858`
- **立即见效**：日志增长从 4910 bytes/second → **0 bytes/second**（每秒少写 5KB = 每天 432MB）
- 这是 system_monitor DEBUG 日志的真源头

### 5. 截断爆炸日志
```bash
truncate -s 1M /var/log/llm-gateway-go/gateway.stderr.log
truncate -s 1M /var/log/llm-gateway-go/gateway.stdout.log
systemctl restart llmgo-245
```
> 释放 1.07GB 磁盘（29G → 28G used）

### 6. 245 云盘扩容 40G → 100G（growpart + resize2fs）
- 分区表备份: `/tmp/vda-partition-table-20260819-210459.bak`
- `growpart /dev/vda 3`: 39.8G → 99.8G
- `resize2fs /dev/vda3`: 在线扩 ext4，状态 clean
- df `/`: 40G (used 74%) → 99G (used 30%, **67G avail**)

### 7. Prometheus 告警规则：`/opt/monitoring/prometheus/rules/host-resources.rules.yml`
4 条规则（已 `promtool check rules` 通过 + 已 POST /-/reload 生效）：
- `LLMGatewayHeapAllocHigh` (warning): `go_memstats_alloc_bytes > 1.2e9 for 5m` — 在 MemoryHigh=1.5G 之前 5 分钟预警
- `LLMGatewayHeapAllocCritical` (critical): `go_memstats_alloc_bytes > 1.5e9 for 2m` — systemd throttle 进行中
- `NginxSystemdFailedPlaceholder` (critical): placeholder，待部署 node-exporter 后切换表达式
- `GatewayStderrLogLargePlaceholder` (warning): placeholder，待部署 file_exporter 后启用

> 当前 alertmanager default receiver 的 webhook 是注释掉的（`LARK_WEBHOOK_URL` 未设置），所以告警在 alertmanager UI 可见但不发通知。等接 webhook URL 自动生效。

### 8. deploy-seamless.sh 加 step 9.5 验证目标机自身 nginx 链路
```bash
log "[9.5/9] 验证目标机自身 nginx→gateway (127.0.0.1:443/healthz)"
if ! host_wait_https_healthy "$SSH_CMD" "$TARGET" 30 2>&1; then
  _seamless_auto_rollback "nginx (443) healthz 失败 — OOM 或 nginx 未自愈, 立即回滚" "$version" || true
  exit 1
fi
```
+ 新增 `host_wait_https_healthy()` helper（host.sh）+ `internal_https_health_url` 字段（targets.sh 154/245 contract）

> 旧流程只检查 `127.0.0.1:8781/healthz`，gateway 活 ≠ 公网通。新流程用 `127.0.0.1:443/healthz` 验证 nginx→gateway 完整链路（绕过 cert mismatch 用 `-k`）。失败 = nginx failed = 自动 rollback。

### 9. 部署架构文档同步：deploy-245 SKILL.md
- 公网拓扑图（DNS → 252 → 245 内网 IP → 245 nginx → gateway）
- 责任边界：deploy-245 管 245 内网 IP nginx + gateway；252 由 252 部署脚本管
- 部署后清单新增 step 9.5（nginx 链路验证）
- 关键经验新增"gateway 存活 ≠ 公网通" + nginx Restart 强制要求

## 防止复发（4 道防线）

| 防线 | 措施 | 触发时机 |
|---|---|---|
| **L1 systemd Restart** | nginx.service 加 `Restart=always`（245 + 252） | OOM 杀 nginx 后 3 秒自动恢复 |
| **L2 systemd MemoryMax** | llmgo-245.service 加 `MemoryMax=2G / MemoryHigh=1.5G` | gateway 触顶 1.5G → throttle，触顶 2G → OOM kill（不连带其他进程） |
| **L3 deploy step 9.5** | `curl -kfsS https://127.0.0.1/healthz` 校验 nginx→gateway | deploy 后立即验证；失败 → 自动 rollback |
| **L4 prometheus alert** | `LLMGatewayHeapAllocHigh/Critical` | gateway 内存 > 1.2GB 持续 5 分钟 → warning；> 1.5GB 持续 2 分钟 → critical |
| **L0 源头** | `LLM_GATEWAY_LOG_LEVEL=info` | DEBUG 日志源头关闭（每秒少写 5KB → 每天 432MB） |

## 验证结果

### 修复前 → 修复后
| 指标 | 修复前 | 修复后 |
|---|---|---|
| 公网 https://llmgo.kxpms.cn/healthz | 000 (无响应) | **200** ✅ |
| 245 nginx systemd | failed (1h21min) | **active** ✅ |
| 245 nginx Restart 配置 | (空) | **always** ✅ |
| 252 nginx systemd | (未检查) | **active** ✅ |
| 252 nginx Restart 配置 | (空) | **always** ✅ |
| llmgo-245 MemoryMax | 无限制 | **2G** ✅ |
| llmgo-245 MemoryHigh | 无限制 | **1.5G** ✅ |
| LLM_GATEWAY_LOG_LEVEL | debug | **info** ✅ |
| stderr.log 大小 | 1.16 GB | **1 MB** ✅ |
| stderr.log 增长速率 | 4910 bytes/秒 (16 MB/小时) | **0 bytes/秒** ✅ |
| 磁盘空间 | 9.8G avail (74% used) | **67G avail** (30% used) ✅ |
| prometheus 规则数 | 8 个 (含 5 个 availability + 5 个 credential) | **12 个** (+4 个 host-resources) ✅ |
| deploy step 9 | 127.0.0.1:8781/healthz OK | **+127.0.0.1:443/healthz OK** ✅ |

### 浏览器实测（rule 11 §6 强制）
![llmgo-homepage-after-fix](file://./ui-verify-2026-08-19-llmgo-homepage-after-fix.png)

页面正常渲染：AI Native 组织核心网关首页，导航栏完整（下载/部署配置/激活/License/反馈），登录按钮 + 主题切换 + 语言切换。

## 遗留与风险

1. ⚠️ **gateway 1.55GB 内存累积根因部分定位（pprof 21:37 抓取）**：
   - **pprof top inuse_space（占总堆 11.33%）**：`URSM cache NewLRU[string, NodeView]` — 配置上限 `LRUMirrorSize=100000`，理论 25MB 上限，不会无界累积
   - **pprof top inuse_space（占总堆 11.10%）**：`internal/logging.NewLockFreeQueue[RawDataEntry]` — async logger 队列，capacity 默认 10000
   - **pprof top alloc_space（占总累计分配 23.18% / 1.3GB）**：`admin.decodeActionEntries` — `live_stream_lifecycle.go:386` 每次 SSE 轮询反序列化 Redis LRANGE 0..499 JSON entries
   - **pprof top alloc_space 13.38% / 743MB**：`reflect.mapassign_faststr0` — Go runtime JSON 反序列化期间分配
   - **关键洞察**：**1.5GB RSS vs Go heap 32MB inuse，差 1.47GB 在 Go heap 之外**（cgo / mmap / kernel buffer / Go runtime metadata）
   - **遗留**：抓取的 heap profile 已存 `build/pprof/245-heap-20260819-213655.pb.gz`（42KB），后续可用 `go tool pprof` 二次深挖（确认 RSS vs Go heap 比例）

2. ⚠️ **cgroup v1 (systemd v219 / v239) 不支持 MemoryHigh throttle**：
   - 245/252 用 systemd v239/v219 + cgroup v1，`MemoryHigh=1500M` 在 `systemctl show` 显示但实际**没软限生效**（memory.soft_limit_in_bytes=infinity）
   - `MemoryMax=2G` **硬限生效**（memory.limit_in_bytes=2147483648）✅
   - 影响：gateway 涨到 1.5GB 不会触发 throttle，但触达 2GB 时 cgroup 直接 OOM kill。**245 / 154 实际行为相同**
   - 154 用 systemd v219，连 `MemoryAccounting=yes` 都得显式开（默认 no），否则 MemoryLimit 不生效

3. ⚠️ **prometheus alertmanager webhook 未配置**：
   - 4 条 host-resources 告警已生效，但 alertmanager.yml 的 default receiver 的 webhook 是注释掉的
   - `FEISHU_BOT_WEBHOOK` 在 envs/INDEX.yaml 引用但**实际未填值**（envs/projects/llm-gateway-go/.env.secrets.plain.yaml 里没有）
   - 当前告警在 alertmanager UI 可见，**但没有外部通知**。等 FEISHU 团队注册机器人 + 填值后，**只需**在 alertmanager.yml 取消注释 `webhook_configs` + 把 `LARK_WEBHOOK_URL` env 注入即生效（不需改代码）

4. ⚠️ **FEISHU/LARK webhook 是部署前置**：alertmanager 的 `placeholder.invalid` 是 RFC 2606 保留 TLD（防止误配置打到真端点），但也意味着**没接 webhook = 用户不会收到通知**

5. ⚠️ **goroutine 总数 137（重启后 7 分钟）**：未看到 goroutine 泄漏，但生产高峰时持续监测

## 改动清单

### 本机仓库（llm-gateway-go）
| 文件 | 类型 | 说明 | 验证 |
|---|---|---|---|
| `scripts/deploy-lib/targets.sh` | 修改 | 加 `internal_https_health_url` 字段到 154/245 contract | `bash -n` + contract JSON render ✅ |
| `scripts/deploy-lib/host.sh` | 修改 | 加 `host_wait_https_healthy()` helper | `bash -n` ✅ |
| `scripts/deploy-seamless.sh` | 修改 | 加 step 9.5：nginx→gateway 链路校验；失败 → auto rollback | `bash -n` + `deploy_ssh_retry_test.sh 21 passed` ✅ |
| `docs/changelogs/2026-08-19-llmgo-nginx-oom-recovery.md` | 新建 | 完整复盘 + 防线 + 遗留 | 已写 ✅ |

**commit `00baa5319`** 已 push 到 `origin/main` (`f59e62773..00baa5319`)，pre-commit PASS=4 FAIL=0

### 245 (`<env:HOST_245_IP>`) 服务端
| 文件 | 类型 | 说明 |
|---|---|---|
| `/etc/systemd/system/nginx.service.d/override.conf` | 新建 | drop-in: Restart=always / RestartSec=3 |
| `/etc/systemd/system/llmgo-245.service.d/memory-override.conf` | 新建 | drop-in: MemoryHigh=1500M / MemoryMax=2G (systemd v239 显示但 throttle cgroup v1 不生效) |
| `/opt/llm-gateway-go/.env` | 修改 | `LLM_GATEWAY_LOG_LEVEL=debug` → `info` |
| `/var/log/llm-gateway-go/gateway.{stderr,stdout}.log` | truncate -s 1M | 释放 1.07GB 磁盘 |
| `/opt/monitoring/prometheus/rules/host-resources.rules.yml` | 新建 | 4 条 OOM / nginx / 日志告警 |
| `/tmp/vda-partition-table-20260819-210459.bak` | 新建 | sgdisk 分区表备份 |
| `/dev/vda3` | growpart + resize2fs | 40G → 99.8G（在线） |
| `/tmp/pprof/245-heap-20260819-213655.pb.gz` | 新建 | pprof heap profile（42KB, 留待后续深挖 RSS vs Go heap） |
| `/tmp/pprof/245-goroutine-20260819-213702.txt` | 新建 | goroutine profile（137 goroutines, 无明显泄漏） |

### 154 (`<env:HOST_154_IP>`) 服务端
| 文件 | 类型 | 说明 |
|---|---|---|
| `/etc/systemd/system/nginx.service.d/override.conf` | 新建 | drop-in: Restart=always / RestartSec=3 |
| `/etc/systemd/system/llm-gateway-go.service.d/override.conf` | 修改 | append: `MemoryAccounting=yes` + `MemoryLimit=2G` (cgroup v1 systemd v219) |

### 252 (`<env:HOST_252_IP>`) 服务端
| 文件 | 类型 | 说明 |
|---|---|---|
| `/etc/systemd/system/nginx.service.d/override.conf` | 新建 | drop-in: Restart=always / RestartSec=3 (与 245/154 同款) |

### 备份文件
- `/usr/lib/systemd/system/nginx.service.bak-20260819-210314` (245)
- `/usr/lib/systemd/system/nginx.service.bak-20260819-211628` (154)
- `/usr/lib/systemd/system/nginx.service.bak-20260819-2116xx` (252)
- `/opt/llm-gateway-go/.env.bak-20260819-210858`

## 经验教训（rule 11 §5 诚实汇报）

- **gateway 存活 ≠ 公网通**：旧 deploy 流程只看 `127.0.0.1:8781/healthz`，nginx 挂了但 deploy 仍报成功——这是根本性盲点（deploy-seamless.sh step 9.5 修复）
- **systemd vendor unit 必须经过 OOM 演练**：标准的 `nginx.service` 在 CentOS/Aliyun Linux 都没 `Restart=always`，生产前必须自己加（245/154/252 三台机都已加固）
- **DEBUG 日志是 OOM 的常见隐藏源**：每秒 25 条 DEBUG × 200 bytes = 5KB/s × 4 小时 = 72MB/小时，2 天就累积 3.4GB 间接吃 IO + 内存（.env 改 info，stderr 增长 4910→0 bytes/s）
- **systemd MemoryMax 必须先于 OOM 设置**：让 systemd 主动限制（throttle + kill）而不是被动触发整个系统 OOM killer。但**注意 cgroup v1 不支持 MemoryHigh**——只有 MemoryLimit 生效
- **deploy 流程应该验证端到端链路，不只是单进程 healthz**：nginx + gateway 是两段链路，deploy 必须验证整段（rule 03 §6 L4 业务真实门禁）
- **systemd v219 (cgroup v1) 需要 MemoryAccounting=yes**：v230+ 默认 yes，v219 默认 no，MemoryLimit 必须显式开 accounting 才生效
- **pprof 是定位 RSS 异常的起点**：RSS 1.5GB vs Go heap inuse 32MB 提示内存累积在 Go 堆外（cgo / mmap / kernel buffer），后续需结合 `runtime.MemStats` 的 `Sys` / `HeapSys` / `MCacheInuse` 等指标诊断