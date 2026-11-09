# 2026-11-09 rule 71 补强：保留名唯一性 + 规范层安装时机

> 规范侧条款见 `vibe-coding/rules/71-log-standard.md` §4.4 / §4.5；
> 归档见 `vibe-coding/docs/changelogs/2026-11-09-rule-71-reserved-names-and-early-install.md`。

## 为什么还要再改一轮

用**容器真实环境变量**跑新二进制做端到端验证时，发现首版接入留下三处
**运行态**隐患——照首版实现，服务仍会产生不合规或**有歧义**的日志。

## 保留名被业务侧占用 ⇒ 同一行 JSON 出现重复键

真实启动采样的第一行：

```json
{"timestamp":"…","msg":"hard block allowlist loaded","env":"LLM_GATEWAY_HARD_BLOCK_RULES",
 "allows":["input:*"],"count":1,"severity_text":"INFO","level":"info","severity_number":9,
 "service":"llm-gateway","version":"dev","env":"local","instance_id":"…"}
```

`"env"` 出现了**两次**：第一个是业务字段（`loadHardBlockPolicy()` 想表达
「环境变量名」），第二个是规范层注入的部署环境名 `local`。

JSON 重复键的取值行为**取决于解析器**——Python `json` / `jq` 取最后一个，
另一些工具取第一个或直接报 parse error ⇒ 同一份日志在不同解析器里
「部署环境」可能变成「环境变量名」，**AI 侧解析有歧义**。

更隐蔽的是：`json.loads` 对重复键取最后一个且不报错 ⇒ **门禁完全看不见**
（假阴性）。

### 改法

- `internal/logging/standard.go`：新增 `reservedNames` 保留名集合；
  规范层在注入前**剥离**业务侧占用保留名的属性——含 `slog.Group` 内的
  （group 会被展开，展开后同样会撞键）、含 `With()` 传的
  （With 传的 attr 落进内层 handler 的预格式化区，`Handle()` 阶段看不到，
  不在 `WithAttrs` 里剥离就会绕过规范层）。
- 冲突**不静默丢弃**：被剥离的键名记入 `reserved_name_conflicts` 诊断字段
  （数组，去重排序）。AI 侧按该字段非空即可定位违规调用点。
- 业务侧改用无歧义键名：
  - 环境变量名 `env` → `env_var`（`cmd/gateway/main.go`、
    `cmd/gateway/sanitize_rules_control.go`、`internal/probelane/ready.go`，3 处）
  - 另一个实体的实例 ID `instance_id` → `agent_instance_id`
    （`internal/centeragent`、`internal/opsreporter`、`internal/collector`、
    `cmd/license-authority`，12 处——它们指的是 **OPS agent / 被管控节点**，
    与本进程实例 ID 不同源）

## 规范层装得太晚、覆盖面不全 ⇒ 同一进程两种格式

三处独立的口子：

| 场景 | 原行为 | 后果 |
|------|--------|------|
| 配置加载过程本身打日志 | 规范层等配置加载完才装 | 早期几行是旧格式 ⇒ 同一进程两种格式 |
| 没有配置落盘文件 | `Init` 直接 no-op | slog 留在默认 `TextHandler` ⇒ 纯文本，缺全部必填字段 |
| 字段改名只挂在落盘管线 | 只有落盘路径带 `ReplaceAttr` | 早装窗口与不落盘模式打出的仍是 `time` 字段 + **本地时区** |

第三条最隐蔽：字段改名（`time`→`timestamp`）与脱敏靠 `ReplaceAttr`，
而 `ReplaceAttr` 属于**内层 handler**。只给落盘管线装上，另外两条路径就静默
退回旧形态——既不满足字段契约，也跨时区无法排序。

### 改法

- `internal/logging/logging.go`：
  - 新增 `InstallEarly(level)`——在配置加载之前把规范层装到 stderr；
  - `Init` 在 `cfg.File == ""` 时**不再 no-op**，也装规范层；
  - 新增 `newJSONHandler(w, level)`，所有构造内层 handler 的路径统一走它。
- `cmd/gateway/main.go`：`main()` 最顶部 `logging.InstallEarly(...)`
  （装在任何日志产生之前，早于 `loadHardBlockPolicy()`）。

## 持久化日志绕过规范层

`critical/error/panic/shutdown/audit` 五个文件此前走裸 `slog.JSONHandler`
——字段名是 `time`、本地时区、无必填字段。事故复盘时读这些文件的正是人和 AI，
形态必须与主日志一致。

### 改法

- `pkg/logger/persistent.go`：新增 `HandlerFactory` 注入点，五个 handler
  改走工厂。
- **依赖注入解环**：`pkg/logger` 不能直接 import `internal/logging`
  （`internal/logging` → `metrics` → `pkg/logger`，反向 import 会成环），
  因此由 `cmd/gateway` 构造好规范层 handler 再注入。
- `slog.Group("instance", …)` 拍平成 `instance_id` / `pid` 平铺字段
  （规范要求顶层单层；嵌套对象会让「找一条日志里的 instance_id」退化成遍历树）。
- 去掉冗余的 `timestamp` 传参——规范层已注入，再传一次会撞键。

## `instance_id` 不是进程级常量

规范层在一个进程里会被装多次（早装 → 全装 → 热加载），每次重生成 ⇒
同一进程不同日志行带不同 `instance_id` ⇒「按 instance_id 聚合一个实例的
全部日志」不可靠。而 `instance_id` 恰恰是 Loki 标签候选之一。

改为进程级缓存（`logging.InstanceID()`），`cmd/gateway` 复用同一份。

## 验证

- 单测：`internal/logging` + `pkg/logger` + `cmd/gateway` 全绿。
  新增 `internal/logging/standard_early_test.go`（5 组）、
  `internal/logging/reserved_test.go`（6 组，含**重复键检测器**）、
  `pkg/logger/persistent_standard_test.go`（2 组）。
- 真实环境端到端（容器真实环境变量，20 秒采样）：

  ```
  stderr.log:    16 行, 重复键 0, 不合规 0
  gateway.log:   15 行, 重复键 0, 不合规 0
  shutdown.log:   1 行, 重复键 0, 不合规 0
  ```

  早期窗口首行（此前会出现重复 `env` 的那一行）现在是：

  ```json
  {"timestamp":"2026-11-09T15:20:09.044676Z","msg":"hard block allowlist loaded",
   "env_var":"LLM_GATEWAY_HARD_BLOCK_RULES","allows":["input:*"],"count":1,
   "severity_text":"INFO","level":"info","severity_number":9,"service":"llm-gateway",
   "version":"dev","env":"local","instance_id":"…"}
  ```

- 机器门禁：`verify-log-standard.py --service llm-gateway --log-file <stderr.log>`
  → `errors=0 warnings=0`。

## 没有做成的事

- 本地容器 `llm-gateway-local-8782` **仍在跑旧镜像**（旧格式），重建容器
  需要明确确认，本轮未执行 ⇒ 运行态尚未生效。
- 本地主干（`main`）与 `origin/main` 仍分叉：本地另有一批他人未提交改动
  （`web/public/menu-config.json`、`docs/audit/*.md`、`domains/modelcredibility/*`），
  本轮一律未碰，合并走独立 worktree。
