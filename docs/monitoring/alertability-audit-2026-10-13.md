# 服务可告性核查（2026-10-13，245 预发布）

> 结论一句话：**告警在算，但没有出口；而且没有看门狗，连「告警链路坏了」
> 这件事本身也不会被发现。** 另外，已恢复的告警会在 Alertmanager 里
> **永远显示为 active**（resolved 通知同样送不出去）⇒ Alertmanager UI
> 本身是一个不可信的读数源。

本文档记录**每一环的实测证据**，不是推测。凡是没验到的，写「未验证」。

## 一、链路全貌

```
指标产生 ──► Prometheus 抓取 ──► 规则求值 ──► Alertmanager ──► 接收器 ──► 人
   ✅            ✅              ✅ 55 条       ✅ 有 2 条 firing   ❌ 无出口
```

| 环节 | 实测结果 | 证据 |
|---|---|---|
| 指标产生 | ✅ | `telemetry_sanitize_events_total` 等有数据 |
| Prometheus 规则求值 | ✅ **13 组 / 55 规则，非 ok = 0** | `GET :9090/api/v1/rules` |
| Prometheus → Alertmanager | ✅ 已配置 | `alerting.alertmanagers.targets = 127.0.0.1:9093` |
| Alertmanager 收告警 | ⚠️ **2 条 firing** | `GET :9093/api/v2/alerts` |
| **接收器 → 人** | ❌ **零投递** | 见下 |

## 二、断点在哪（三个，都验过）

### ① `default` 接收器没有集成

`/opt/monitoring/alertmanager/alertmanager.yml`：

```yaml
receivers:
  - name: 'default'
    # Webhook disabled by default - uncomment and set LARK_WEBHOOK_URL to enable
    # webhook_configs:      ← 整段被注释
```

⇒ 路由到 `default` 的告警**没有任何出口**。

### ② `credential-team` 指向占位域名

同一个文件里 `credential-team` 的 webhook URL 由容器启动时的 sed 从
`$CREDENTIAL_TEAM_WEBHOOK_URL` 替换。实测：

```
渲染后的实际 URL: https://placeholder.invalid
```

`placeholder.invalid` 是 RFC 2606 保留 TLD，**必然投递失败**。
配置注释自己也写明了这点：env 未注入时就是这个占位值。

而 `CREDENTIAL_TEAM_WEBHOOK_URL` 在三个候选位置
（`/opt/monitoring/alertmanager/.env`、`/opt/monitoring/.env`、
`/opt/llm-gateway-go/.env`）与 systemd 进程环境里**都不存在**。

### ③ 网关自身的候选失败 webhook 也没配

```
进程环境里没有 CANDIDATE_FAILURE_WEBHOOK_URL
启动日志: "candidate_failure_monitor configured" ... "webhook_set":false
```

⇒ `bg/candidate_failure_monitor.go` 触发告警后**只写内存环 + slog.Warn**，
不发任何外部通知。告警只能靠人主动打开 `/api/candidate-failures/alerts` 才看到。

## 三、为什么这两条 firing 一直没人管

当前 2 条告警的路由归属（`GET :9093/api/v2/alerts` 的 `receivers` 字段）：

| 告警 | severity | component | 路由到 |
|---|---|---|---|
| `TelemetrySanitizeDiscarded` | critical | `telemetry` | `default`（无出口） |
| `LLMGatewayAvailabilityReadDurationHigh` | warning | 未设 | `default`（无出口） |

路由树里只有 `severity=critical|warning` **且** `component=credential` 的
才走 `credential-team`；这两条都不满足 ⇒ 全部落进 `default`。

## 四、已恢复的告警为什么还显示 active（这条最隐蔽）

`TelemetrySanitizeDiscarded` 自 `2026-10-12T22:12:26` 起 active，但实测
当前 rate **全为 0**：

```
rate(telemetry_sanitize_events_total{outcome="discarded"}[5m]) = 0（全部 series）
```

也就是说**条件此刻已不成立**，Prometheus 早已 resolve。但 Alertmanager 里
它仍是 active —— 因为 **resolved 通知同样要经接收器投递**，接收器坏了，
Alertmanager 就永远收不到「好了」这个信号。

⇒ 后果：**Alertmanager 的 active 列表不能当作「现在正在出问题」来读**。
本次这两条里，至少一条（Redis 读延迟 p99 = 0.07s）明显已恢复。

## 五、这次丢弃到底是不是真的（runbook 要求的判别）

`docs/runbooks/telemetry-sanitize-discarded.md` 要求先跑 doctor 脚本判别。
本次直接查指标（只读）：

| 窗口 | 丢弃次数 |
|---|---|
| 最近 30 分钟 | 16.1 |
| 最近 60 分钟 | 38.2 |
| 最近 180 分钟 | 158.1 |

按字段分解（近 6 小时）：

```
field=response_body  丢弃 173.1 次   ← 只有这一个字段在丢
其余 11 个字段全是 0.0
```

⇒ **这是真实的、持续的数据丢弃**：按 runbook 的定义，`discarded` 表示
`sanitizeUTF8JSON` 返回空串、对应 JSONB 列被 NULL 化，**审计员看不到原始
response_body**。严重度定级为 critical 是合理的。

⚠️ 未验证：具体是哪类上游响应触发丢弃（需要 runbook 的 doctor 脚本 +
请求日志侧归因，本轮没做）。

## 六、没有看门狗

```
grep -rniE "watchdog|deadman|dead.mans|up == 0" /opt/monitoring/prometheus/rules/
→ 无命中
```

⇒ 告警链路（Prometheus / Alertmanager / 接收器）整体挂掉时，
**没有任何东西会来告诉我们**。这正是「可告性」里最容易缺、也最难在
出事当天发现的一环。

## 七、要补什么（按性价比排序，都需要人工决定故本轮未动）

| # | 动作 | 需要什么 |
|---|---|---|
| 1 | 给 `default` 接收器启用 webhook | 通知渠道地址（配置注释指向 `LARK_WEBHOOK_URL`） |
| 2 | 注入 `CREDENTIAL_TEAM_WEBHOOK_URL` | 凭据团队 oncall 的 webhook |
| 3 | 注入 `CANDIDATE_FAILURE_WEBHOOK_URL` | 同 1 |
| 4 | 加看门狗规则（永远 firing 的 `Watchdog`） | 无（可立即做） |
| 5 | 查 `response_body` 丢弃的根因 | 按 runbook 跑 doctor 脚本 |

⚠️ 第 4 项技术上不依赖任何人，**但它只有在 1~2 落地后才有意义** ——
往一个没有出口的链路里加"永远在响"的告警，不会让人知道，只是多一条噪音。
⇒ 故与 1~2 一起做，不单独做。

## 复核命令

```bash
ssh root@245 '
  set -a; . /opt/llm-gateway-go/.env 2>/dev/null; set +a
  echo "--- 规则健康 ---"
  curl -s --noproxy "*" http://127.0.0.1:9090/api/v1/rules \
    | python3 -c "import sys,json;d=json.load(sys.stdin);gs=d[\"data\"][\"groups\"];\
print(\"组\",len(gs),\"规则\",sum(len(g[\"rules\"]) for g in gs),\
\"非ok\",sum(1 for g in gs for r in g[\"rules\"] if r[\"health\"]!=\"ok\"))"
  echo "--- firing 告警及其 receiver ---"
  curl -s --noproxy "*" http://127.0.0.1:9093/api/v2/alerts \
    | python3 -c "import sys,json;\
[print(a[\"labels\"].get(\"alertname\"),\"→\",a.get(\"receivers\")) for a in json.load(sys.stdin)]"
  echo "--- 渲染后的 webhook 主机 ---"
  grep -oE "https?://[^/\" ]+" /opt/monitoring/alertmanager/alertmanager.rendered.yml | sort -u
  echo "--- 是否有看门狗 ---"
  grep -rlniE "watchdog|deadman" /opt/monitoring/prometheus/rules/ || echo "无"
'
```

⚠️ 这台机器上 **`/usr/bin/timeout` 不存在**（只有 `/opt/homebrew/bin/timeout`）。
用错路径时命令会被静默跳过、输出为空 —— 看起来像「查到了 0 条」。
