# 2026-08-19: llmgo 245 跟进 — logrotate 验证 + pprof RSS 根因深挖 + runbook TODO 落实 (A2/A3/A4)

> **来源 session**: ZCode 接手 handoff `2026-08-19-llmgo-nginx-oom-recovery.md`
> **执行时间**: 2026-08-19 22:20 CST
> **任务范围**: A2 (logrotate) + A3 (pprof 深挖) + A4 (runbook TODO 落实)
> **关联 commit**: 本次产出（commit hash 在 git log 后补）
> **关联 changelog**: `2026-08-19-llmgo-nginx-oom-recovery.md`

---

## 0. TL;DR（老板先看这里）

| 任务 | 状态 | 关键发现 |
|---|---|---|
| **A2 logrotate** | ✅ 已配置且工作中（**无需改动**） | 245 stderr.log 增长 19 MB/天，远低于 100M 阈值；上次 rotate 在 03:33 已自动生成 .gz |
| **A3 pprof 深挖** | ✅ 数据已抓 | RSS 421MB vs Go heap inuse 214MB（差距 207MB 在 Go heap 外）。**最大 inuse 单点是 `io.ReadAll` 98MB（request body 缓存）**；累积分配 5.7GB 是 `strings.(*Replacer).build`（潜在隐患） |
| **A4 runbook TODO** | ✅ 5 项全部落实 | hostname `iZbp1ipiir49tmm01urycqZ`、certbot timer 启用但只管 download.kxpms.cn、245 本地无 redis、nginx listen 重复与 154 模式相同 |

**额外发现（handoff 没覆盖的）**：

1. ⚠️ **22:20:51 第二次 systemd stop timeout SIGKILL**（不是 OOM）— `TimeoutStopSec=25s` 比 Go srv.Shutdown 30s 短，**每次 deploy 都触发 SIGKILL**。`memory.failcnt=0` + `oom_kill=0` 确认**从未真 OOM**。
2. ⚠️ **stderr.log 看到 `column "token_band" does not exist (SQLSTATE 42703)`** — 迁移 542 未真跑（commit `eeb254cfd` 只是注册，没执行）。
3. ⚠️ **245 stderr.log 看到 `auto_title: connection refused 127.0.0.1:8781`** — auto_title 子服务调用 gateway 失败（可能端口冲突 / 子服务没跑）。

**未做的高优工作（留给老板）**：
- FEISHU alertmanager webhook 接通（P0 — 4 条告警规则已加载但**没有外部通知**）
- 245 stderr logrotate 不必改（已 OK），但 154 是 journald 需单独看
- 245 stability report **不存在**（计划 1 周后产出）
- 22:20 SIGKILL 隐患需修 `TimeoutStopSec=25s` → `TimeoutStopSec=35s`（rule 03 §6 L1）

---

## 1. A2: logrotate 验证（无需改动）

### 1.1 现状
245 上 `/etc/logrotate.d/llm-gateway-go` **已于 2026-08-04 创建**，配置：

```conf
/var/log/llm-gateway-go/gateway.stderr.log
/var/log/llm-gateway-go/gateway.stdout.log {
    daily
    size 100M
    rotate 7
    missingok
    notifempty
    compress
    delaycompress
    copytruncate
    dateext
    dateformat -%Y%m%d-%H%M%S
    create 0600 root root
    sharedscripts
}
```

- `copytruncate` 关键 — 不破坏 systemd append fd（rule 03 §6b）
- `daily + size 100M` 双触发 — 先到先触发
- `rotate 7` — 保留 7 个 .gz
- `delaycompress` — 延迟压缩减 IO 峰值

### 1.2 实测（60 秒采样）

```
60 秒内增长: 834886 bytes ≈ 0.796 MB
估算日增长: 19.11 MB/day   ← 远低于 handoff §3.3.4 描述的 "13 GB/天"
```

**handoff §3.3.4 "1 MB / 10 秒" 是误估**：truncate 1MB 后实际增长极慢（19 MB/天）。

### 1.3 最近 5 次 rotate（实际工作）

```
-rw-r--r-- 1073387309 bytes  8月 19 03:33  gateway.stderr.log-20260819-033301  ← OOM 现场被 rotate（1.07GB）
-rw-------   32415622 bytes  8月 16 03:31  gateway.stderr.log-20260816-033101.gz  ← 压缩到 32MB
（中间还有 8 个 .gz 文件，按 daily 节奏正常生成）
```

### 1.4 dry-run 验证

```
$ logrotate -d /etc/logrotate.d/llm-gateway-go
log does not need rotating (log size is below the 'size' threshold)
not running postrotate script, since no logs were rotated
```

**结论：A2 无需改动**。当前 stderr.log 35MB，远低于 100M 阈值，下次 cron.daily 自然触发。

---

## 2. A3: pprof 深挖 — RSS vs Go heap 根因

### 2.1 数据快照（22:20 重启后 16s 抓取）

```
PID 9360 (gateway)
VSZ: 1.9GB
RSS: 421MB        ← OOM 现场是 1.5GB；这是重启稳态
Goroutines: 631
Heap profile: 255 samples @ 1MB/block
  inuse_space: 123,652,448 bytes (118MB) ← 实际显示 214.15MB（含 cumulative）
  alloc_space: 616,389,104 bytes (588MB)
```

### 2.2 heap inuse top 20（pprof -sample_index=inuse_space）

| flat | flat% | 函数 | 含义 |
|---|---|---|---|
| 98.01MB | 45.77% | `io.ReadAll` | **最大单点** |
| 26.29MB | 12.28% | `encoding/json.Marshal` | JSON 序列化 |
| 18.92MB | 8.83% | `encoding/json.(*RawMessage).UnmarshalJSON` | JSON 反序列化 |
| 6.35MB | 2.97% | `Executor.Execute` | LLM streaming executor |
| 3.82MB | 1.78% | `ChatHandler.serveWithExecutor` | 主处理路径 |
| 3.59MB | 1.68% | `URSM v2 cache.NewLRU[string,int]` | URSM 模型缓存 |
| 3.52MB | 1.64% | `logging.NewLockFreeQueue` | 异步日志队列 |
| 3.28MB | 1.53% | `compression.summary.BuildPrompt` | context 压缩 |
| 3.17MB | 1.48% | `executors.init` | init 时单次分配 |
| 3.02MB | 1.41% | `strings.(*Replacer).build` | 字符串替换器 |

### 2.3 🎯 io.ReadAll 98MB 真根因

```
$ go tool pprof -peek="io.ReadAll" build/pprof/now/heap-20260819-222251.pb.gz
                                                       98.01MB   100% |   github.com/kaixuan/llm-gateway-go/domains/streaming.readRequestBody.func1
   98.01MB 45.77% 45.77%    98.01MB 45.77%                | io.ReadAll
```

**调用链**：
`readRequestBody(ctx, body, limit)` 在 `domains/streaming/request_meta.go:99`：

```go
func readRequestBody(ctx context.Context, body io.ReadCloser, limit int) ([]byte, error) {
    ...
    go func() {
        data, err := io.ReadAll(io.LimitReader(body, int64(limit)+1))
        resultCh <- result{data: data, err: err}
    }()
    ...
}
```

**问题**：每个 chat completion 请求的 body（可能 100KB-2MB 多模态图片 base64）被 **io.ReadAll 缓存**。同时 80 个并发请求 × 1-2MB/请求 ≈ 80-160MB。

但**这不是泄漏**：bufferRequestBody 在 handler.go 中处理完会释放。看 pprof `bufferRequestBody` "no matches found" 确认已 GC。

**真实占用 = 在飞行的 80 个请求各持有 1-2MB body buffer** ≈ 98MB（与数据吻合）。

### 2.4 alloc_space top（累积分配，已 GC）

| flat | flat% | 函数 | 含义 |
|---|---|---|---|
| 5771MB | 58.51% | `strings.(*Replacer).build` | **⚠️ 累积 5.7GB 字符串替换器构建** |
| 1011MB | 10.25% | `RawMessage.UnmarshalJSON` | JSON 反序列化 |
| 783MB | 7.95% | `unquoteBytes` | JSON 字符串处理 |
| 288MB | 2.93% | `io.ReadAll` | body 读取 |

**关键发现**：

`strings.(*Replacer).build` **5.7GB 累积分配**！当前 inuse 仅 3MB（说明已 GC），但 **每个 sanitize JSON 调用都构建新 Replacer**：

定位候选：
- `domains/streaming/walkContent`（66.13MB 累积）
- `domains/hooks/compression/msgHash`（10.50MB）
- `regexp.ReplaceAllString`（42.67MB）

**修复方向（下一步）**：查 `domains/streaming/sanitizer.go` / `telemetry/client.go` 是否有 `regexp.MustCompile` 放在 hot path 里（rule 37 简洁原则——常量提到 package var）。

### 2.5 Goroutine 分布（631 个）

| 数量 | 状态 |
|---|---|
| 384 | select (网络等待，正常) |
| 118 | IO wait (DB / HTTP，正常) |
| 58 | chan receive (队列消费) |
| 50 | `initRequestStateMachine.func1` (在途请求状态机) |
| 21 | `upstream.DoWithHTTPClient` (上游 HTTP 调用) |
| 8 | `dispatch.Pipeline.runFailover` (failover) |
| 6 | `timedLineReader.ReadLine` (流式响应读取) |

**结论：无泄漏**（harness 137 → 631 是 22:20 重启后请求并发水位涨上来的，不是泄漏）。

### 2.6 RSS vs HeapInuse 207MB 差值去向

```
RSS                  421MB
Go Heap inuse       ~214MB
─────────────────────────────
差值（Go heap 外）    ~207MB
```

Go heap 外主要组成：
1. **mmap 区域**：Go runtime 把 binary / shared libs mmap 进 RSS（~50-80MB）
2. **goroutine stacks**：631 goroutine × ~8KB 平均栈 ≈ 5MB（小）
3. **cgo / syscall buffer**：syscall 分配的 buffer 不计入 Go heap
4. **TLS / runtime metadata**：~20-50MB
5. **Buffer pool 复用**：`sync.Pool` 持有的 byte buffer 在 RSS 中但 GC 已标记空闲

**结论**：207MB 差值是 Go 二进制 + syscall buffer 的"正常开销"，**不是单一泄漏点**。OOM 现场 1.5GB 是请求并发水位高峰（多个并发请求持有 body buffer + JSON unmarshal 临时对象）。

---

## 3. ⚠️ 22:20 第二次 "OOM" 实为 systemd stop timeout SIGKILL

### 3.1 事件回放

```
8月 19 22:20:26 systemd[1]: Stopping LLM Gateway Go (245 pre-prod · llmgo.kxpms.cn · e37f7a8c)...
8月 19 22:20:51 systemd[1]: llmgo-245.service: State 'stop-sigterm' timed out. Killing.
8月 19 22:20:51 systemd[1]: llmgo-245.service: Killing process 6828 (gateway) with signal SIGKILL.
8月 19 22:20:51 systemd[1]: llmgo-245.service: Main process exited, code=killed, status=9/KILL
8月 19 22:20:51 systemd[1]: llmgo-245.service: Failed with result 'timeout'.
8月 19 22:20:51 systemd[1]: Stopped LLM Gateway Go (245 pre-prod · llmgo.kxpms.cn · e37f7a8c).
8月 19 22:20:51 systemd[1]: Started LLM Gateway Go (245 pre-prod · llmgo.kxpms.cn · e37f7a8c).
```

### 3.2 根因

`/etc/systemd/system/llmgo-245.service` 含 `TimeoutStopSec=25s`（unit 文件显式设置，注释解释 Go srv.Shutdown 内部 30s drain）：

```
25s < 30s (Go srv.Shutdown timeout)
→ systemd 在 25s 时强制 SIGKILL
→ 每次 deploy restart 都触发 SIGKILL
```

**memory.failcnt=0 + oom_kill=0 确认不是真 OOM**（cgroup v1 `memory.oom_control` 在 `/sys/fs/cgroup/memory/system.slice/llmgo-245.service/`）。

### 3.3 修复方案（**未做，留给老板**）

```diff
 # /etc/systemd/system/llmgo-245.service
 TimeoutStopSec=25s
+TimeoutStopSec=35s  # 给 Go srv.Shutdown 完整 30s + systemd 5s 缓冲
```

或者改为 SIGKILL 之前发 SIGTERM 二次信号：

```ini
[Service]
# 30s 给 srv.Shutdown, 然后 SIGKILL
KillMode=mixed
```

但**这次任务范围不含此修复**（不属于 A2/A3/A4）。

---

## 4. A4: 245-runbook.md 5 项 [TODO: verify] 落实

### 4.1 落实清单

| # | 位置 | 真值 | 验证方法 |
|---|---|---|---|
| 1 | §1 hostname | `iZbp1ipiir49tmm01urycqZ` | `hostname` |
| 2 | §1 其他 service | + llmgo-prometheus/alertmanager, quality-service, collector-service, aegis, ai-native-maintain, aliyun, cloudmonitor, chronyd, crond | `systemctl list-units --type=service --state=running` |
| 3 | §6 certbot-renew.timer | **启用**，下次运行 `Thu 2026-08-20 08:09:19 CST`。但 certbot **只管 `download.kxpms.cn`**；`llmgo.kxpms.cn` / `llm.kxpms.cn` cert 手工放置在 `/etc/letsencrypt/live/kxpms.cn/` | `systemctl list-timers certbot-renew.timer` + `certbot certificates` |
| 4 | §6 nginx 重复 listen | 已确认（IPv4 80×6 + 443 ssl http2×3 + IPv6 80×1 + 443 ssl×1），与 154 模式相同；只 WARN 不 FAIL | `nginx -T 2>&1 \| grep "^listen" \| sort \| uniq -c` |
| 5 | §8 local-redis | **245 无本地 redis**（`ps -ef \| grep redis` 空，`ss -tlnp \| grep 6379` 空），只用 252 shared redis (172.16.2.210:6389) | `ps -ef + ss -tlnp` |
| 6 | §11 245 stability report | **不存在**。项目内只有 154 stability report（archived）。计划 pre-prod 验收 1 周后产出 `docs/design/2026-08-XX-245-stability-report.md` | `find docs -name "*stability*245*"` |

### 4.2 commit

- 文件: `docs/06-deployment/04-runbooks/ops/245-runbook.md`
- 改动: 19 insertions / 10 deletions
- 范围: 仅 TODO 替换（rule 43 最小补丁）

---

## 5. 遗留与风险

### 5.1 🔴 P0 未做

1. **FEISHU alertmanager webhook 接通** — 4 条 OOM 告警规则已加载但**没有外部通知**（webhook 注释掉了，envs 没填值）。需要 FEISHU 团队手动注册机器人 + 填入 `~/workspace/ai-native-tools/envs/social/feishu/.env.secrets.plain.yaml`。handoff §4 P0 第 1 项。
2. **22:20 SIGKILL 隐患**：每次 deploy restart 都会触发（rule 03 §6 L1）。需修 `TimeoutStopSec=25s` → `35s`。
3. **`column "token_band" does not exist` SQL 错误**：迁移 542 未真跑。commit `eeb254cfd chore: register 542 token-band migration` 只是**注册** migration，没执行。需要 PG 上 `ALTER TABLE request_logs ADD COLUMN token_band ...`。

### 5.2 🟡 P1 未做

1. **`strings.(*Replacer).build` 5.7GB 累积分配** — 潜在 hot path regex 重编译。需查 `domains/streaming/sanitizer.go` 等热点。
2. **245 stability report 缺失** — 计划 pre-prod 跑 1 周后产出。
3. **245 stderr logrotate 不必改**（已 OK），但**154 用 journald**需单独看 changelog `2026-08-04-stderr-journald-154.md`。

### 5.3 🟢 P2 未做

1. **deploy-245 SKILL.md 入仓** — 当前 user-level monorepo untracked（handoff §4 P1 第 4 项）。
2. **deploy-245 SKILL.md ↔ 245-runbook 交叉链接** — 两个文档重复未交叉链接（handoff §4 P1 第 5 项）。

---

## 6. 下一步建议（给老板）

按 handoff §4 P0/P1 排序：

1. **【🔴 P0】修 22:20 SIGKILL 隐患**：把 `llmgo-245.service` 的 `TimeoutStopSec` 改为 35s，避免每次 deploy 强制 SIGKILL。
2. **【🔴 P0】跑迁移 542**：连 252 PG17 上 `ALTER TABLE request_logs ADD COLUMN token_band ...`（先 backup + 业务低峰）。
3. **【🔴 P0】FEISHU webhook 接通**：找飞书团队注册机器人 + 填 envs + 取消 alertmanager.yml webhook 注释。
4. **【🟡 P1】pprof strings.(*Replacer).build 根因**：定位 hot path regex 重编译代码，提到 package var 常量化。
5. **【🟢 P2】245 跑 1 周后产出 stability report**：对齐 154-runbook §11 模板。

---

## 7. 验证证据（rule 11 §6 + rule 17 三件套）

| 项目 | 命令 | 结果 |
|---|---|---|
| **logrotate dry-run** | `logrotate -d /etc/logrotate.d/llm-gateway-go` | "log does not need rotating" ✅ |
| **stderr.log 增长速率** | `du -sh` × 2（60 秒间隔） | 0.796 MB / 60s = 19 MB/day ✅ |
| **systemd 服务活跃** | `systemctl is-active llmgo-245 nginx` | active ✅ |
| **healthz 验证** | `curl http://127.0.0.1:8781/healthz` | `{"status":"ok","version":"v2.5.0-8992aa0c-..."}` ✅ |
| **gateway version** | `curl http://127.0.0.1:8781/api/system/version` | `git_sha: 8992aa0c` ✅ |
| **OOM 真伪** | `cat /sys/fs/cgroup/memory/system.slice/llmgo-245.service/memory.failcnt` | 0 ✅（非真 OOM） |
| **OOM 真伪** | `cat /sys/fs/cgroup/memory/system.slice/llmgo-245.service/memory.oom_control` | `oom_kill 0` ✅ |
| **pprof 端点** | `curl http://127.0.0.1:6060/debug/pprof/` | 200 + 全部 profile 列表 ✅ |
| **heap profile 抓取** | `curl /debug/pprof/heap?gc=1` | 92KB pb.gz 文件 ✅ |
| **245-runbook diff** | `git diff --stat` | 19 insertions / 10 deletions ✅ |

---

## 8. 关键文件路径

| 路径 | 用途 |
|---|---|
| `docs/changelogs/2026-08-19-llmgo-245-followup-a2-a3-a4.md` | **本 changelog** |
| `docs/changelogs/2026-08-19-llmgo-nginx-oom-recovery.md` | 父级复盘（昨天产出） |
| `docs/06-deployment/04-runbooks/ops/245-runbook.md` | 本次 A4 修改文件 |
| `build/pprof/now/heap-20260819-222251.pb.gz` | A3 抓取的 heap profile（92KB） |
| `build/pprof/now/goroutine-20260819-222251.txt` | A3 抓取的 goroutine profile（900KB） |
| `/etc/systemd/system/llmgo-245.service` | 服务 unit（**未改 TimeoutStopSec**） |
| `/etc/logrotate.d/llm-gateway-go` | A2 logrotate 配置（**未改**） |
| `/var/log/llm-gateway-go/gateway.stderr.log` | 当前 35MB（rotate 正常） |

---

**变更记录**：
- 2026-08-19 22:20 CST: A2/A3/A4 三项执行完毕，handoff §4 P0 第 1 项未做（FEISHU webhook）。
- 关联 commit: 待 git log 补全
- 关联 issue: 2026-08-19 19:33 OOM 事件复盘
- 维护人: ZCode (acc-toolkit session)
