# Sessions V2 双写配置参考

**分支**: `feat/session-turns-v2`  
**用途**: 控制会话 V2 表（`session_turns` + `session_bodies` + `sessions`）的双写行为，为全面切换到 V2 做准备。

---

## 配置项

所有配置通过 `settings` 表的 `platform` 域或网关配置文件 YAML 设置。网关 `SessionPersistHook` 在运行时热加载这些标志，无需重启。

### `sessions_v2.enabled`

- **类型**: `boolean`
- **默认值**: `false`
- **含义**: Sessions V2 主开关。`false` 时，网关不加载 V2 pipeline hooks，V2 表完全不写入。
- **影响范围**: 所有 V2 相关功能（双写、读取、验证）的总闸。
- **何时开启**: 在 staging 完成 schema migration + 工具验证后，首次开启此项 + `shadow_write`。

---

### `sessions_v2.shadow_write`

- **类型**: `boolean`
- **默认值**: `false`
- **含义**: 双写开关。`true` 时，网关在写 `request_logs` 的同时也写 `session_turns` + `session_bodies`（影子写），两者必须同事务。
- **依赖**: 必须 `sessions_v2.enabled = true` 才生效。
- **影响范围**: 仅写路径。读路径仍优先 `request_logs`（除非 admin 读逻辑显式切到 V2）。
- **何时开启**: 在回填历史会话 + 验证一致性后，开启此项使新会话同时写 V1 + V2，为切换读路径做准备。

---

### `sessions_v2.rollout_percent`

- **类型**: `integer` (0–100)
- **默认值**: `0`
- **含义**: 双写流量百分比。`0` = 关闭双写（即使 `shadow_write=true`），`100` = 所有新会话都双写，`5` = 随机 5% 的会话双写。
- **依赖**: 必须 `sessions_v2.enabled = true` 且 `shadow_write = true` 才生效。
- **影响范围**: 写路径流量控制。灰度放量时逐步提高此值（5 → 20 → 50 → 100）。
- **实现**: `SessionPersistHook.Enabled()` 在每次请求时读取此值，通过 `session_id` 哈希取模决定是否双写。

---

### `sessions_v2_compression_read`

- **类型**: `boolean`
- **默认值**: `true`
- **含义**: 读取 V2 时是否支持压缩正文（`outbound_body` + `submit_mode`）。
- **影响范围**: `admin/unified_detail.go` 与 `validate_sessions_v2` 的 V2 读路径。
- **何时关闭**: 仅当压缩回填/校验失败需临时回退到非压缩路径时设为 `false`。

---

## 配置示例

### 阶段 1: 完全关闭（默认，生产当前状态）

```yaml
sessions_v2:
  enabled: false
  shadow_write: false
  rollout_percent: 0
```

**效果**: V2 表不写入，读路径走 `request_logs`，前端 V2 优先逻辑自动回退到 V1 派生。

---

### 阶段 2: Staging 小流量双写

```yaml
sessions_v2:
  enabled: true
  shadow_write: true
  rollout_percent: 5
```

**效果**: 5% 的新会话同时写 V1 + V2，用于验证双写逻辑与一致性。旧会话仍只在 V1。

---

### 阶段 3: Staging 全量双写

```yaml
sessions_v2:
  enabled: true
  shadow_write: true
  rollout_percent: 100
```

**效果**: 所有新会话都双写。观察 `shadow_write_failed` 指标与延迟影响。

---

### 阶段 4: 生产灰度（验证通过后）

逐步放量，与 staging 阶段 2/3 同配置，但在生产环境执行。每次提高 `rollout_percent` 后观察 24h+。

---

### 阶段 5: 切换读路径（需代码改动）

当双写稳定且存量回填完成后，修改 `admin/unified_detail.go` 与前端，将读优先级改为 `session_turns` → `request_logs`（当前分支已实现）。

**配置不变**，仅代码逻辑调整。重启 admin 服务生效。

---

### 阶段 6: 停写 V1（需代码改动 + 高风险）

修改写路径，停止向 `request_logs_bodies` 写入正文，仅保留指标记录。`request_logs` 降级为审计日志。

**配置可选**:
```yaml
sessions_v2:
  enabled: true
  shadow_write: false  # 不再是"影子"写，V2 成为唯一写
  rollout_percent: 100 # 保留此项用于应急回退流量控制
```

---

## 监控指标

| 指标名 | 含义 | 告警阈值 |
|-------|------|---------|
| `sessions_v2_shadow_write_total` | 双写尝试总数 | — |
| `sessions_v2_shadow_write_failed` | 双写失败次数 | > 0.1% |
| `sessions_v2_shadow_write_latency_p99` | 双写 p99 延迟 | > 50ms |
| `sessions_v2_rollout_sampled_in` | 命中双写的会话数 | 应 ≈ `rollout_percent` % |
| `session_bodies_disk_usage_mb` | V2 正文存储占用 | — |
| `request_logs_bodies_disk_usage_mb` | V1 正文存储占用 | 应 > V2（套娃重复） |

---

## 故障排查

### 双写失败率高

1. 检查 `session_turns`/`session_bodies` 表是否有分区冲突（月初未创建下月分区）。
2. 检查 `SessionBodiesWriter` 日志，确认 JSON 序列化错误（NaN/Inf/非法 UTF-8）。
3. 检查事务隔离级别是否导致死锁（双写与 V1 写竞争）。

### V2 读取为空

1. 确认该会话在 `session_turns` 中有记录（`SELECT * FROM session_turns_with_current_month WHERE session_id=...`）。
2. 确认 `session_bodies` 有对应轮次（`SELECT * FROM session_bodies WHERE session_id=...`）。
3. 检查前端是否正确调用 `/api/admin/sessions/{id}/turns/bodies`（Network 面板）。
4. 若该会话早于双写开启，需先回填（见 runbook）。

### 存储增长异常

1. V2 增长速度应 < V1 的 50%（消除套娃）。若相近，检查 `request_delta` 是否错误包含了全量消息。
2. 检查 `submit_mode` 分布，`full` 模式不压缩（backfill 派生的默认是 `full`；双写实时推断 `delta`/`snapshot`）。

---

## 回退路径

| 场景 | 回退操作 | 影响范围 |
|------|---------|---------|
| 双写错误率高 | `shadow_write=false` | 停止 V2 写入，V1 不受影响 |
| 读路径切换后发现问题 | 代码回退读优先级为 `request_logs`，重启 admin | 前端回到 V1 读路径 |
| V2 正文数据错误 | 清空 `session_bodies`，重新回填 | 需停服或切回 V1 读路径 |
| 停写 V1 后无法恢复 | **不可逆** — 需从备份恢复 `request_logs_bodies` | 全量回滚 |

---

**文档维护**: 本配置参考随分支演进，合并到 main 后归档到 `docs/会话优化v3/配置参考.md`。
