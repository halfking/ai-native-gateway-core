# tests/local — 本地测试全景

> **2026-07-12 事件响应**：为支持"先开会话压缩/缓存/限流降级三模块"的紧急验证而新增。
> 在不重启线上服务的前提下，使用 kill-switch + 本地 mock infrastructure 复现问题并验证修复。

## 三、布局

```
tests/
├── local/
│   ├── docker-compose.yml          # PostgreSQL 17 + Redis 7 + Mock LLM upstream
│   ├── Dockerfile.gateway          # 网关容器镜像构建
│   ├── env.example                 # 环境变量样例
│   ├── Makefile                    # 一键命令
│   ├── seed/                       # 数据库初始化 schema + fixture data
│   │   └── 01-schema-bootstrap.sql
│   ├── scenarios/
│   │   ├── 01-baseline.sh          # 基线验证（全部开启，期望 99%+ 通）
│   │   ├── 02-killswitch.sh        # kill-switch 矩阵验证
│   │   └── 03-high-concurrency.sh  # 高并发压测驱动
│   └── scripts/
│       └── wait-healthy.sh         # docker wait helper
├── k6/
│   ├── llm-gateway-spike.js        # spike + baseline（k6）
│   └── llm-gateway-soak.js         # soak 5-min（k6）
└── README.md
```

## 二、快速开始

### 准备

```bash
# 1. 起基础设施
cd tests/local
cp env.example .env
make up

# 2. seed fixture data
make seed

# 3. 启动 gateway (with mock upstream)
make gateway-up
```

### 场景

```bash
# 基线
make scenario-01

# kill-switch 验证
make scenario-02

# 高并发（依赖 k6）
brew install k6  # 一次性
make scenario-03
```

### 开关（Kill-Switch）

```bash
# 示例：临时关掉 session_cache，重启后生效
KILL_SESSION_CACHE=1 make gateway-down
KILL_SESSION_CACHE=1 make gateway-up
make scenario-01   # 重新验证业务仍 200
```

可用的开关：

| Env var | 关闭模块 | 影响范围 |
|---|---|---|
| `KILL_SESSION_COMPRESSION=1` | compression hook | 上下文压缩、LCS diff |
| `KILL_SESSION_CACHE=1` | session_cache (L1/L2/L3) | session 状态读写 |
| `KILL_CIRCUIT_DEGRADATION=1` | circuit breaker | credential 熔断 / 半开 |
| `KILL_RATE_LIMITER=1` | RPM/TPM token bucket | API key 限流 |
| `KILL_FP_SLOT=1` | fp_slot prefilter | fingerprint 隔离 |

默认值：全部启用（生产稳定行为）。

## 三、故障场景

### 复现 2026-07-12 minimax-m3 间歇 503

```bash
# 开启异常模拟：DB schema drift 触发 mirror write 失败
make db-shell
UPDATE credential_model_bindings
   SET unavailable_recover_at = NOW() + INTERVAL '15 minutes',
       unavailable_reason = 'model_probe_broken'
 WHERE credential_id = 1;  -- mock-primary

# 业务应进入"路由失败 → router all filtered" 路径
# 然后开 kill-switch 验证旁路有效：
KILL_FP_SLOT=1 make gateway-down && KILL_FP_SLOT=1 make gateway-up
make scenario-01  # 应当重新通过
```

### 高并发探测（spike + soak）

```bash
# Spike 200 RPS × 30s
TARGET_RPS=200 DURATION=30s k6 run tests/k6/llm-gateway-spike.js

# Soak 50 RPS × 5min（看是否有内存泄漏）
k6 run tests/k6/llm-gateway-soak.js
```

## 四、验证证据（templates）

完成后请把以下文件加到 PR：

- `tests/local/reports/<date>/baseline.log` — 01-baseline.sh 输出
- `tests/local/reports/<date>/spike.log` — k6 spike 输出
- `tests/local/reports/<date>/soak.log` — k6 soak 输出
- `tests/local/reports/<date>/killswitch.log` — 02-killswitch.sh 输出

## 五、注意事项

1. **不要直连线上 DB** —— `.env` 中默认 localhost:55432，会被 docker 自动起本地容器
2. **不要用真实 API key seed 到本地** — 只用 `sk-loc-*` 测试 key
3. **gateway 重启会丢 5xx 雪崩测试用例的内部状态**，所以 spike 测试前后各跑一次 baseline 对比
4. **kill-switch 重启后立即生效**，不需要重新 build 镜像
