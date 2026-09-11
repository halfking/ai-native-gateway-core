# Native Prometheus + Alertmanager deployment on 245 (pre-prod)

> 2026-08-18 credential-17 加固链第八会话产出。P0 阻断消除后，245 的告警 runtime 以
> **原生二进制 + systemd** 形态常驻（非 docker-compose）。本文记录该形态的部署事实、
> 与仓库 compose 配置的差异、以及运维操作。

## 为什么是原生部署（而不是 docker-compose）

245 是 **podman（rootful）且无 compose provider**；同时：

- `docker.io` 从 245 与办公网均不可达（http=000）；
- `registry.kxpms.cn` 只有业务镜像（无 `prom/*`）；
- GitHub releases 从 245 可直连。

因此 Prometheus 2.47.0 / Alertmanager 0.26.0 以 GitHub release tarball 落盘为原生二进制
（经办公网中转 scp，因 245 直连 GitHub CDN 只有 ~1.4MB/min）。

## 部署布局

```
/opt/monitoring/
├── prometheus/               # prometheus + promtool 二进制、consoles/
│   ├── prometheus.yml        # 适配版（见下）
│   ├── rules/                # 6 个生效 rule 文件（credential-reveal 等）
│   ├── rules-disabled/       # partition-health.yml（解析已修复，指标未实现，暂缓）
│   ├── secrets/admin_token   # 600，内容=网关 LLM_GATEWAY_ADMIN_API_KEY
│   └── data/                 # TSDB（retention 30d）
├── alertmanager/
│   ├── alertmanager + amtool
│   ├── alertmanager.yml      # 与仓库同源（matcher 双引号修复后）
│   ├── alertmanager.rendered.yml  # ExecStartPre 渲染产物（真实加载的配置）
│   ├── render-alertmanager-config.sh  # sed 替换完整 placeholder token
│   └── webhook.env           # 600，CREDENTIAL_TEAM_WEBHOOK_URL=...
└── smoke/webhook-receiver.py # 测试用 webhook 接收器（默认不运行）
```

systemd units：`/etc/systemd/system/llmgo-prometheus.service`、
`/etc/systemd/system/llmgo-alertmanager.service`（均 enabled + Restart=always）。

## 与仓库 compose 配置的差异（245 适配点）

| 仓库（compose） | 245（native） | 原因 |
|---|---|---|
| alertmanager target `alertmanager:9093` | `127.0.0.1:9093` | 无容器 DNS |
| llm-gateway target `llm-gateway:8781` | `127.0.0.1:8781` | 真实网关 llmgo-245.service 在宿主机 |
| `bearer_token_file: /etc/prometheus/secrets/admin_token` | `/opt/monitoring/prometheus/secrets/admin_token` | 路径映射 |
| node-exporter / postgres scrape | 注释掉 | 无对应 exporter（P0 不依赖） |
| entrypoint sed 替换 webhook | `render-alertmanager-config.sh`（同为完整 token 替换） | 语义一致 |

rule 文件与 `alertmanager.yml` **与仓库保持同源**；仓库修复后需同步到 245 并 reload。

## 常用运维

```bash
# 状态
systemctl status llmgo-prometheus llmgo-alertmanager
curl -s 127.0.0.1:9090/-/ready; curl -s 127.0.0.1:9093/-/ready

# credential 告警规则加载确认（应为 5 条）
curl -s "127.0.0.1:9090/api/v1/rules?rule_group=credential_reveal_failures" \
  | python3 -c 'import sys,json;d=json.load(sys.stdin);print([r["name"] for g in d["data"]["groups"] for r in g["rules"]])'

# 改规则后 reload（promtool 先验一遍）
/opt/monitoring/prometheus/promtool check rules /opt/monitoring/prometheus/rules/<file>.yml
curl -X POST 127.0.0.1:9090/-/reload

# 注入/更换 credential-team webhook（当前唯一待补的运营项）
vi /opt/monitoring/alertmanager/webhook.env     # CREDENTIAL_TEAM_WEBHOOK_URL=https://...
systemctl reload llmgo-alertmanager             # ExecStartPre 重新渲染 + 重载

# 派发链路 smoke（临时接收器 + 合成告警）
systemd-run --unit=smoke-webhook --property=Restart=no \
  /usr/bin/python3 /opt/monitoring/smoke/webhook-receiver.py 9999
echo 'CREDENTIAL_TEAM_WEBHOOK_URL=http://127.0.0.1:9999/webhook' \
  > /opt/monitoring/alertmanager/webhook.env && systemctl reload llmgo-alertmanager
curl -XPOST 127.0.0.1:9093/api/v2/alerts -H 'Content-Type: application/json' \
  -d '[{"labels":{"alertname":"CredentialRevealUnknownFormatSpike","severity":"critical","component":"credential","provider_id":"smoke","reason":"unknown_format"},"annotations":{"summary":"smoke"}}]'
cat /opt/monitoring/smoke/received.log    # 应看到 receiver=credential-team 的 POST
# 收尾：恢复 webhook.env、systemctl stop smoke-webhook
```

## 自检必要性（necessity gate）双机观测接入（2026-09-12）

> 需求来源：`docs/handoff/2026-09-11-selfcheck-necessity-gate-handoff.md` 遗留项 6 /
> 第二十五轮——生产观测目标钉定为 **245 与 154 两台网关机**，necessity 三指标
> （skip/retry/failed）与告警 `NodeProbeNecessityMirrorDeleteFailedHigh` 需要对两台机
> 的**每个蓝绿槽位**都有数据。本节是仓库 `deploy/prometheus/prometheus.yml` 末尾
> `llm-gateway-prod` 注释模板在 245 原生栈上的落地版。

**为什么不能沿用现有 `127.0.0.1:8781` 单目标**：

1. 只覆盖 245 本机，**154 完全在抓取面外**——154 上的 necessity 指标与告警数据不存在；
2. 蓝绿切换后 active 端口在 8781/8782 **轮换**（2083 收尾文档实测），固定 8781 的抓取
   恰在发版时刻集体落空（target DOWN），监控在最需要它的窗口致盲；
3. 每节点抓**两个槽位**（`a`=8781，`b`=8782）：非 active 槽位 target DOWN 属预期噪声；
   `instance` 标签钉死为 `节点-槽位`（不随轮换漂移），面板与告警按 instance 分系列。

**接入步骤（在 245 上执行）**：

```bash
# 1. 抓取目标：/opt/monitoring/prometheus/prometheus.yml 的 scrape_configs 追加
#    （<HOST_154_ADDR> 换成 245 可达的 154 地址；先验证连通性，见第 4 步）
#      - job_name: 'llm-gateway-prod'
#        scrape_interval: 15s
#        static_configs:
#          - targets: ['127.0.0.1:8781']
#            labels: { instance: 'gateway-245-a', service: 'llm-gateway' }
#          - targets: ['127.0.0.1:8782']
#            labels: { instance: 'gateway-245-b', service: 'llm-gateway' }
#          - targets: ['<HOST_154_ADDR>:8781']
#            labels: { instance: 'gateway-154-a', service: 'llm-gateway' }
#          - targets: ['<HOST_154_ADDR>:8782']
#            labels: { instance: 'gateway-154-b', service: 'llm-gateway' }
#        bearer_token_file: '/opt/monitoring/prometheus/secrets/admin_token'
#    （admin_token 文件已存在=245 的 LLM_GATEWAY_ADMIN_API_KEY；154 网关的
#      key 若与 245 不同，须另建 token 文件并拆出 154 专属 job——先 curl 验证）
# 2. 校验 + 热加载
/opt/monitoring/prometheus/promtool check config /opt/monitoring/prometheus/prometheus.yml
curl -X POST 127.0.0.1:9090/-/reload
# 3. 告警规则：仓库 deploy/prometheus/rules/alerts.yml 与 /opt/monitoring/prometheus/rules/
#    同步（含 NodeProbeNecessityMirrorDeleteFailedHigh）后按上方惯例 reload
# 4. 连通性/鉴权预检（在 245 上对 4 个目标各来一次）
curl -s -o /dev/null -w '%{http_code}\n' -H "Authorization: Bearer $(cat /opt/monitoring/prometheus/secrets/admin_token)" http://127.0.0.1:8781/metrics
curl -s -o /dev/null -w '%{http_code}\n' -H "Authorization: Bearer $(cat /opt/monitoring/prometheus/secrets/admin_token)" http://<HOST_154_ADDR>:8781/metrics   # 期望 200；401=两机 key 不同；000/超时=网络不通
```

**接入后验收（Prometheus 侧）**：

```bash
# 4 个 instance 各应有 llmgw_node_probe_necessity_skip_total 系列（计数器注册即预热）
curl -s '127.0.0.1:9090/api/v1/series?match[]=llmgw_node_probe_necessity_skip_total' | python3 -m json.tool | grep instance
# 告警规则已加载（应含 NodeProbeNecessityMirrorDeleteFailedHigh）
curl -s '127.0.0.1:9090/api/v1/rules' | python3 -c 'import sys,json;d=json.load(sys.stdin);print([r["name"] for g in d["data"]["groups"] for r in g["rules"] if "Necessity" in r["name"]])'
```

**判读规则**（与 handoff 第六轮 runbook 一致，按 instance 分节点判）：
`mirror_delete_retry_total` 偶发增长=瞬时失败被同轮吸收（设计内）；`failed_total` 在
**同一 instance 上** 10m 增量 >5（即告警阈值）=该节点有 (cred,model) 镜像删除翻动循环
→ 按 handoff 遗留项 6 升级短 TTL 抑制；`skip_total` 单节点异常高 → 排查该节点 Redis
键 schema（legacy/k2/dual）与 tenant 归属。蓝绿轮换后计数器从 0 重新累计属预期
（新进程），`increase()` 自动处理清零；轮换后请在 **active 槽位的新 instance 系列**
上继续判读。

**边界（如实记录）**：Grafana 不在 245 原生栈内（本文件只覆盖 Prometheus +
Alertmanager）——`selfcheck-necessity-dashboard.json` 面板需要 Grafana 实例方可导入
（compose 栈内有；生产双机观测当前以 Prometheus UI / api/v1 查询为准，面板导入待
Grafana 落点定夺）。

## 已知状态（2026-08-18）

- **`CREDENTIAL_TEAM_WEBHOOK_URL` 尚未提供**：webhook 保持 `https://placeholder.invalid/##...##`
  占位，credential 告警派发会 loud-fail（设计内"请配置我"信号）。runtime 与派发链路本身
  已通过 smoke 验证可用。
- `llmgw_credential_reveal_failure_total` 计数器已注册并在进程启动时预热
  （`provider/credential_decrypt_metrics.go`），当前应可见 7 个
  `provider_id="0"` sentinel series，均为 0；真实失败会按真实 `provider_id` 懒增量创建。
  baseline、dashboard 和阈值查询必须使用 `provider_id!="0"` 排除 sentinel。
- 真实 `CREDENTIAL_TEAM_WEBHOOK_URL` 注入、真实 reveal failure E2E 与 24h baseline
  校准仍待 owner/oncall 授权和运营数据；不要用破坏性 ciphertext 注入替代这些前置条件。
- `partition-health.yml` 解析 bug 已修复（`now()`/`date_trunc` 均为 2.49/2.50+ 函数），
  但其规则依赖未实现的自定义指标（`partition_next_month_exists` 等）+ postgres exporter，
  故仍置于 `rules-disabled/`，规则处于休眠态。

## 历史教训（别再踩）

1. **alertmanager matcher 引号**：`severity = 'critical'`（单引号）在 Prometheus matcher
   语法里非法且**静默不匹配**——credential 告警会全部 fall-through 到无 webhook 的 default
   receiver 被吞掉。必须双引号 `severity = "critical"`。
2. **webhook sed 必须匹配完整 token**：只替换 `##TOKEN##` 会把
   `https://placeholder.invalid/` 前缀留在结果里生成畸形 URL。
3. **`promtool check rules` 对部分函数错误返回 exit 0**（2.47.0 观察：`now()`/`date_trunc`
   均未被它拦下），**以 `/-/reload` 实际结果为准**。
4. 245 上 SSH 内联后台进程（`setsid ... &`）易导致会话 255 断连；长驻测试进程用
   `systemd-run --unit=...` 派生。
