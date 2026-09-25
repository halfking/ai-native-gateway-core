# mock 探测通道——优化方案

> ## ⚠️ 已废弃（2026-09-24 注记）：本文属 **v1 历史设计，已被取代**
>
> 本文为 v1 旧方案章节，已被 [03-optimization-plan.md](03-optimization-plan.md)（方案 v2）取代。v2 重写后，本文仅保留历史设计价值——安全模型与可见性矩阵一律以 [README.md](README.md) 状态块 + [03-optimization-plan.md](03-optimization-plan.md) + [05-implementation.md](05-implementation.md)（实施记录）为准，下述 v1 章节表述（含 embedded 子系统、`__self_probe__` 标签隔离、独立 prometheus.Registry、127.0.0.1:auto 等）**仅保留历史，勿按其实施**。

> 文档：`docs/design/2026-09-23-mock-probe-channel/03-optimization.md`
>
> 输入：`requirements` §3（FR/NFR）+ `02-code-audit` §3（缺口）。目标：**只新增 `internal/embedded` 与一组开关位**，不引入新语言、不开新 sidecar、不污染业务指标 / 业务路由。

---

## 0. 设计原则（沿用仓库既有纪律）

1. **god-mode / self-probe 隔离**：mock 探测用的供应商、凭据、模型、租户都打 `__self_probe__` 标签，仅自检 API 看得到，对外业务接口 / 业务 UI / 业务指标全部不可见。
2. **同语言同部署**：MP 与 MC 都是 Go 实现的 **同进程** 组件，启动顺序在 `cmd/gateway` 内编排，无 docker-compose、无 Python、无 sidecar。
3. **开关为唯一控制面**：所有可见性、启动、停机、热重载都收敛到 `MockProbeConfig`；不允许散落的 `os.Getenv` 旁路。
4. **不复用既有 `USE_NEW_PROBE_MODE`**：那是 `internal/probemode` 的旧/新栈切换，跟本轮正交（已写进需求 §4 边界）。
5. **不留端口尾巴**：MP 必须绑回环 `127.0.0.1`、端口由 manager 启动时动态选；MC 共享同进程 ticker，不开对外端口。
6. **指标独立 namespace**：所有 mock 探测相关 metrics 走 `self_probe_*` 前缀，不与 `gateway_*` 混 label / 混直方图。

---

## 1. 总体架构

```
              ┌────────────────────────────────────────────────────────┐
              │                  cmd/gateway/main.go                   │
              │  (1) 加载 config.MockProbe                            │
              │  (2) enabled=false → 全部跳过(注册空 manager)         │
              │  (3) enabled=true  → embedded.NewManager(cfg)          │
              │      ├── MockUpstream A  (127.0.0.1:auto, mode=healthy)│
              │      ├── MockUpstream B  (127.0.0.1:auto, mode=flaky)  │
              │      ├── MockClient (ticker @ IntervalSeconds)         │
              │      └── registerTo(admin/registry/router/metrics)     │
              └────────────────────────────────────────────────────────┘
                                    │
   ┌─────────────── 内可见 ─────────┼─────────────── 外可见 ───────────┐
   │                               │                                   │
   ▼                               ▼                                   ▼
/admin/self_probe/*           /admin/providers                        /v1/chat/*    业务路径
/metrics (self_probe_*)       /admin/models                           /admin/*      业务抽屉
                              （带 is_self_probe 过滤）               （永远过滤掉 is_self_probe=true）
```

**关键点**：MP 只在内可见 admin 接口 `/admin/self_probe/*` 注册（FR-5.2），业务 `GET /admin/providers` 走 `is_self_probe=false` 过滤（FR-5.1）；MC 只在 metrics namespace `self_probe_*` 注册，**不会进入 `gateway_*` 直方图**。

---

## 2. 模块划分（Go 统一部署）

### 2.1 新增包 `internal/embedded`

```
internal/embedded/
├── manager.go        // 启动/停止/热重载 mock 探测总开关
├── mockupstream.go   // MP：OpenAI 协议 + 9 种 mode（来自 stress/mocks/main.go）
├── mockclient.go     // MC：节拍发起 Chat/Stream + 静默闸
├── admin_state.go    // /admin/self_probe/state 协议形状（来源 stress/mocks）
├── metric_scope.go   // self_probe_* 注册辅助；只在 enabled=true 时注册 promhttp handle
└── manager_test.go   // 单测：enabled false→零行为；enabled true→A/B 起来；reload→热替换
```

### 2.2 复用既有包

| 包 | 复用方式 |
|----|----------|
| `internal/routingtest` | MC 直接 embed `Client`，复用 `Chat` / `Stream`；去掉外部 timeout 配置 |
| `internal/registry` | MP 注册走 `registry.RegisterProvider` 时携带 `IsSelfProbe: true` |
| `internal/router` | MC 调用走既有 `router.Pick` 路径，验证 2x2 交叉路由 |
| `internal/admin` | `/admin/self_probe/*` handler 注册走 `admin.RegisterRoute`，**独立 prefix** |
| `internal/metrics` | 新增 `metrics.NewSelfProbeRegistry()`，挂到独立 `:9091` 或被同一 promhttp mux `/metrics` 下分 prefix |
| `config` | 新增 `MockProbeConfig` 块（见 §4） |

---

## 3. 数据面 / 控制面分离

### 3.1 数据面（业务路径）—— **不受 mock 影响**

- 业务 `POST /v1/chat/completions` 的 provider 候选集合**永远过滤 `is_self_probe=true`**（router 层做）。
- 业务 `POST /v1/responses` / admin 业务路由同样过滤。
- 业务 metrics `gateway_request_duration_seconds` 等 histogram 不增加 self_probe 维度 label，不污染既有查询。

### 3.2 控制面（self_probe）—— **仅 self_probe API 可见**

- 新增 `/admin/self_probe/{state,trigger,providers,disable}` 四条路由。
  - `GET  /admin/self_probe/state` — 当前 MP 模式、MC 最近 N 次结果摘要
  - `POST /admin/self_probe/trigger` — 临时触发一轮（不破坏 ticker 节拍）
  - `GET  /admin/self_probe/providers` — 只看 mock 供应商
  - `POST /admin/self_probe/disable` — runtime disable（写回内存 cfg，不改 .env）
- 控制面写操作需要 `SelfProbeAdminToken`，与业务 admin 鉴权复用同一中间件但**额外校验** `cfg.MockProbe.AdminToken`（env：`MOCK_PROBE_ADMIN_TOKEN`）。

### 3.3 可观测面

- 业务 `/metrics` 永久不出现 `self_probe_*`（避免业务告警噪声）。
- 新增 `/metrics` 下 `self_probe_*` 命名空间（在同一 promhttp mux / 同一端口，但 query 时 `?name[]=self_probe_*` 即可过滤）。
- 日志打 `subsystem=self_probe` tag，便于按 subsystem 过滤；logger 不要默认开 debug level。

---

## 4. 配置项（唯一开关面）

```go
// config/config.go
type MockProbeConfig struct {
    Enabled          bool          `yaml:"enabled" env:"MOCK_PROBE_ENABLED" default:"false"`
    IntervalSeconds  int           `yaml:"interval_seconds" env:"MOCK_PROBE_INTERVAL_SECONDS" default:"30"`
    RequestTimeoutMs int           `yaml:"request_timeout_ms" env:"MOCK_PROBE_REQUEST_TIMEOUT_MS" default:"5000"`

    Providers []MockProviderSpec `yaml:"providers"` // 默认 2 条：a=healthy、b=flaky
    Models    []string            `yaml:"models"`   // 默认 ["mock-gpt-4o-mini","mock-gpt-4o"]

    AdminToken string `yaml:"admin_token" env:"MOCK_PROBE_ADMIN_TOKEN"` // 空字符串=禁用 runtime trigger

    TenantTag string `yaml:"tenant_tag" env:"MOCK_PROBE_TENANT_TAG" default:"__self_probe__"`
}
type MockProviderSpec struct {
    Name string `yaml:"name"`           // 内部名，如 "self-probe-a"
    Mode string `yaml:"mode"`           // healthy/slow/flaky/server_error/...
    Port int    `yaml:"port"`           // 0=auto
}
```

**热重载路径**：业务 reload 回调里若 `cfg.MockProbe.Enabled` 由 true→false，manager 先 drain MC ticker → 关 MP server → 注销 metrics → 删 admin routes → `is_self_probe=false` 过滤生效；false→true 反向。

---

## 5. 启动顺序 / 关闭顺序（与 `cmd/gateway/main.go` 现有阶段对齐）

```
阶段           动作（enabled=true 时）
─────────────────────────────────────────────────────
config load    解析 MockProbeConfig（enabled=false 则跳过下面一切）
register core  先 registry/router/admin/metrics core 起来（业务能力先就绪）
mock up        embedded.Manager.Start(cfg) // 启 MP A/B + MC ticker
healthz ready  完成 self_probe state=ready 后才置 ready=true
─────────────────────────────────────────────────────
shutdown SIG   embedded.Manager.Shutdown(ctx) // 先 MC ticker cancel → 等 drain → 关 MP
                在 admin/metrics 注销之前完成，避免新探针进入
```

启动顺序保证：**业务 admin / 业务 metrics 必须先就绪**，mock 探测是后挂的；这样 healthz 报 ready 时业务能力已经可用，self_probe 是后到的能力增强。

---

## 6. 可见性矩阵（FR-5 落地）

| 维度 | enabled=true | enabled=false |
|------|--------------|---------------|
| `GET /admin/providers` | 不显示 self_probe provider（过滤） | 不显示（不存在） |
| `GET /admin/models` | 不显示 self_probe model（过滤） | 不显示 |
| `GET /admin/self_probe/*` | **可访问**（需 admin token） | **404** |
| `GET /metrics` 业务 series | 不增加 self_probe label | 不变 |
| `GET /metrics` self_probe_* | 注册 | **不注册** |
| Web 抽屉 | 过滤 is_self_probe | 不显示 |
| 业务路由表（router.Pick） | 候选过滤 self_probe | 过滤（同上） |
| MC 出站请求 | 每 IntervalSeconds 1 次 | 静默（ticker stop） |
| MP 监听 | 127.0.0.1:auto | 不启动 |
| 日志 subsystem=self_probe | 出现 | 不出现 |

---

## 7. 安全 / 边界

- **回环绑定**：MP 必须 `127.0.0.1`，杜绝远程访问（即使配错也兜底）。
- **API key deny**：`__self_probe__` 内部租户在业务 auth 中间件显式 deny，外部用户拿业务 API key 调不到 mock 通道。
- **资源上限**：MC ticker 默认 30s/轮；并发限制 `MaxConcurrent=2`；超时 5s；一轮失败直接计入 metrics，不重试不雪崩。
- **数据不入业务库**：MC 走与业务请求相同的入库管线；但在入库 hook 处加 `if session.TenantTag == "__self_probe__" { skip }`，避免污染生产 trace 表（具体 hook 点 TBD，由 PR2 决定）。
- **关闭顺序保证**：shutdown 阶段必须先 manager.Shutdown 再 metrics/admin 注销；测试要加 `TestShutdownOrder`。

---

## 11. 附录：关键约束细化（PR 实现期防翻车）

### 11.1 router hot path 的 self_probe 过滤写法

`internal/router/picker.go` 是热路径，`Pick` 在每次请求都会被调用一次。**禁止**：

```go
for _, p := range candidates {
    if p.Tags["self_probe"] == "true" { continue }   // 错：map lookup + 字符串比较
    ...
}
```

**必须**：

```go
type Provider struct {
    ...
    IsSelfProbe bool   // 直接 bool 字段，零开销
}
for _, p := range candidates {
    if p.IsSelfProbe { continue }                    // 一条 cmp 指令
}
```

要求 PR2 实施时把 `IsSelfProbe` 字段直接加到 `Provider` struct（不是 metadata map 也不是 tag），picker 路径单测要钉死性能阈值（既有的 `BenchmarkPick*` 不许变）。

### 11.2 metrics 隔离方案（避免污染业务 `/metrics`）

`internal/metrics/` 既有代码把 `gateway_*` 系列注册到一个全局 `prometheus.Registry`。**绝不能**把 `self_probe_*` 直接塞进去，原因是：

1. 业务告警规则（`gateway_request_duration_seconds`）会因 histogram 新增 label 维度而爆炸；
2. self_probe 在 enabled=false 时**不能注册**任何 series，否则 `curl /metrics` 永远能看到 `self_probe_*` —— 违反 FR-5.2（开关关闭不可见）。

**正确做法**：

- `internal/metrics/self_probe.go`（PR3 新增）：
  ```go
  type SelfProbeRegistry struct { *prometheus.Registry }
  func NewSelfProbeRegistry(enabled bool) *SelfProbeRegistry {
      r := prometheus.NewRegistry()
      if !enabled { return &SelfProbeRegistry{r} }   // 空注册器，零 series
      // 注册 self_probe_request_duration_seconds / self_probe_request_total ...
      return &SelfProbeRegistry{r}
  }
  ```
- `cmd/gateway/main.go`：把 self_probe registry 用 `promhttp.HandlerFor(selfProbeReg, ...)` 挂到 `/metrics` 的 sub handler，但 `/metrics` 顶层用 `promhttp.HandlerFor(businessReg, ...)`；二者合并到同一 mux 用 `mux.Handle("/metrics", combinedHandler)`。
- `combinedHandler`：业务用业务 reg、self_probe 用 self_probe reg，**两个 reg 物理隔离**，关闭时不注册即不出现任何 series。

### 11.3 启动顺序硬约束

PR1 实施时，`cmd/gateway/main.go` 的 OnStart 阶段顺序必须严格：

```
1. config load         → cfg.MockProbe 解出
2. logger init         → logger.With("subsystem","self_probe") 可用
3. core ready          → registry/router/admin/metrics core 起来
4. self_probe up       → embedded.Manager.Start(ctx)         // 后挂
5. healthz ready=true  → 必须 4 完成后才置位
```

反序会出现"业务尚未 ready，self_probe 已经在请求"的边界 bug（既有 healthz 检查会先打到不存在的 router）。

---

## 8. 与既有 test harness 的关系（保留 + 不动）

- `tests/local/mocks/`：保留，作为 e2e harness 外部 mock，**不被本轮替代**。
- `tests/stress/mocks/`：保留作为压测 harness 外部 mock；**协议层参考**，实现被 `internal/embedded/mockupstream.go` 内嵌化。
- `cmd/routing-test-client/`：保留；本轮 MC 内嵌版不等同于它（routing-test-client 是一次性 N 轮，MC 是永驻 ticker）。

---

## 9. 与 "统一部署" 的契合

- **零外部依赖**：删 `scripts/mocks/llm-mock-upstream/*.py`（记录在 02-code-audit §1 行 C，列废弃项），python 进程从部署包消失；`scripts/mocks/nginx.conf` 同时删除。
- **零 sidecar**：不引入第二个二进制；MP+MC 与 `gateway` 主进程同包。
- **零语言混合**：仓库 `go.mod` 不变，不增加 `requirements.txt` 依赖。
- **零外部端口泄漏**：MP 仅 127.0.0.1，部署脚本不需要新增端口白名单。

---

## 10. 落地时序（与下一步动作清单 §11.6 的拆分对齐）

1. PR1：`internal/embedded` 骨架 + `MockProbeConfig` 注入 + 启动顺序（无真实探测，仅 up/down）。
2. PR2：MP 9 mode + `/admin/self_probe/state`；业务接口过滤 `is_self_probe=true`。
3. PR3：MC ticker + self_probe metrics 注册 + 静默闸 + 热重载。
4. PR4：删 python mock（`scripts/mocks/llm-mock-upstream/*`），统一部署验证。
5. PR5：单测 + 集成测试 + README/CHANGELOG 收口。