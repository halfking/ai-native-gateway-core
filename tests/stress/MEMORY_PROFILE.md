# LLM Gateway 内存配比（实测边界 vs 配置建议）

**生成时间**: 2026-09-22 02:10 CST  
**被测进程**: `tests/stress/gateway`（无 PG / Redis / File cache / WAL）  
**本机**: macOS arm64；压测时 gateway RSS ≈ 25 MB。host 空闲内存会波动（本轮 `vm_stat` free 曾到 ~9 GB，上一轮记录过 ~4.7 GB）。  
**不要把本文件当成生产 `cmd/gateway` 的 4 规格实测。**

---

## 1. 两类数字必须分开

### 1.1 本轮 mock 网关实测

全量 16 场景结束后（2026-09-22 02:03）：

| 指标 | 值 | 来源 |
|---|---|---|
| `/admin/memstats` alloc_mb | 2 | curl |
| goroutines | 18 | curl |
| sys_mb | 24 | curl |
| `ps` RSS gateway | ≈ 25.4 MB | pid 42350 |
| 每个 mock RSS | ≈ 19–20 MB | pid 42310–42313 |

没有 PG 池、没有 Redis、没有 10 GB file cache。这些 RSS **不能**外推到生产。

### 1.2 生产静态预算（配置推算，未在本机用 cgroup 验证）

下面沿用生产默认值做**纸面拆分**，方便 154/245 配 GOMEMLIMIT。不是本次压测结果。

| 规格 | OS（估） | PG 池假设 | Redis 假设 | File cache 建议 | 纸面静态合计 |
|---|---:|---:|---:|---:|---:|
| 2c4G | 600 MB | 32 conn × 5 MB = 160 MB | 32 × 100 KB | 1 GB | ~1.8 GB |
| 2c8G | 800 MB | 64 × 5 MB = 320 MB | 64 × 100 KB | 3 GB | ~4.1 GB |
| 4c8G | 800 MB | 128 × 5 MB = 640 MB | 128 × 100 KB | 3 GB | ~4.5 GB |
| 4c16G | 1 GB | 200 × 5 MB = 1000 MB | 200 × 100 KB | 10 GB 过大，建议 6 GB | ~8.0 GB |

上一版把「200 conn × 5 MB = 1 GB」套到 2c4G 上，再得出「会话上限 80K」。那是把生产满配连接池塞进最小规格，**会话上限表作废**。

5 MB/conn、256 B/session、80K/160K/380K 都没有在本机用真实 SessionState 量过。

---

## 2. `-simulate-prod-mem` 实际发生了什么

`gateway/main.go` 用 `make([]byte, …)` 预分配缓冲，再加 `GOMEMLIMIT`。这能验证 sampler 读 `/admin/memstats` 的路径，**不能**验证 Linux cgroup OOM。

上一轮记录（未在本轮复跑，保留为「曾观察到」）：

| 尝试 | 结果 |
|---|---|
| 基线 16 场景，无模拟 | RSS ~25 MB，未 OOM |
| 手动 GOMEMLIMIT=15000MB + ~1215 MB 模拟 | 独立进程曾看到 RSS ~1259 MB（host 当时有余量） |
| 4c8G / 4c16G `-simulate-prod-mem` | host 内存紧时被 macOS OOM kill |

因此：**没有 4 规格内存退化曲线实测。** 上一版 §3.2 ASCII 曲线和 §3.3「2c4G 100 并发触发退化」是公式推算。

吞吐对照（mock，不是生产）：

- 本轮无内存压力 s8 ≈ 940 req/s（2000/2.126s）
- 旧 memprofile 写过「基线 962.93 / 2c4G 651–1347」。1347 那档注明过是 fail-fast 503，不能当吞吐。

---

## 3. 仍可保留的配置建议（154/245）

这些是运维建议，发布前要用 Linux cgroup 重测：

```bash
# 生产主力建议：4c8G（本机未 cgroup 验证）
LLM_GATEWAY_STORAGE_MAX_CONNECTIONS=128   # 不要默认拉满 200
LLM_GATEWAY_CACHE_MAX_SIZE_GB=3
LLM_GATEWAY_ASYNC_WRITERS=4
GOMEMLIMIT=7GB
GOGC=100
```

| 环境 | 规格建议 | CacheMaxGB | MaxConns | GOMEMLIMIT |
|---|---|---:|---:|---|
| 154（主力） | 4c8G | 3 | 128 | 7 GB |
| 245（备份） | 2c8G | 3 | 64 | 7 GB |
| 边缘 | 2c4G | 1 | 32 | 3.5 GB |

发布门禁（§11，本次未做）：

- 必须设 `GOMEMLIMIT`
- 30 分钟 RSS 不单调增长
- `GODEBUG=gctrace=1` 的 P99 pause 阈值需要在目标机上重新标定，本机没有 30 分钟样本

---

## 4. 相关文件

| 文件 | 用途 | 可信度 |
|---|---|---|
| `tests/stress/scripts/memprofile/main.go` | 4-spec 驱动 | 代码在；大规格本机跑不全 |
| `tests/stress/gateway/main.go` `-mem-limit-mb` / `-simulate-prod-mem` | 预分配 + SetMemoryLimit | 已实现 |
| `tests/stress/results/memory*.jsonl` | 旧采样 | 混有 OOM 中断，解读需看对应 summary 的 error 字段 |
| `tests/stress/results/report.json` | 本轮 16 场景 | 可信（mock 网关） |
