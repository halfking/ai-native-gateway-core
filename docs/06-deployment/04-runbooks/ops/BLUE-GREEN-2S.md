# 本地与154/245蓝绿切换方案

## 目标与边界

发布总耗时和流量切换耗时分开计量：构建、上传、数据库兼容迁移、候选预热和业务验收不属于 handoff 窗口。`handoff_elapsed_ms` 只覆盖 Nginx active-upstream fragment 的原子替换、`nginx -t`、graceful reload 和入口版本确认，目标为 ≤2000ms。

当前正式入口是单端口 8781；蓝绿资源把候选放在 8782，并由 Nginx include `/opt/llm-gateway-go/run/active-upstream.conf` 选择流量端口。154 的公网 TLS 仍由252代理到154固定地址，154本机 upstream 切换不需要重载252。

## 运行角色

- `LLM_GATEWAY_RUNTIME_ROLE=active`：拥有后台 worker、队列 claim/requeue/reaper、探针、rollup 和写入型定时任务。
- `LLM_GATEWAY_RUNTIME_ROLE=traffic-only`：只提供HTTP/API/SSE、健康、就绪和路由请求；复用 data-plane worker 门禁，不能与旧实例争抢后台任务。
- `/version` 返回 `runtime_role` 和 `listen`，切流必须确认候选版本及角色。

## 资源安装（不切流）

在154或245目标机由人工执行：

```bash
sudo bash scripts/install-blue-green-assets.sh /opt/llm-gateway-go 154
# 245 使用 245 参数；本次任务不会自动执行该命令
sudo nginx -t
```

安装脚本只写候选 systemd template、运行目录和初始 fragment，不启动实例、不停止旧服务、不修改 active upstream。

## 切换状态机

1. 通过SSOT注入环境并完成 PG 预检、bundle 上传、SHA256 校验和向前兼容迁移。
2. 读取 `run/active-port`；选择另一个端口并把 bundle 绑定到 `slots/<port>`，不改 `current`。
3. 启动 `llm-gateway-go-canary@8782.service` 或 `llmgo-245-canary@8782.service`。
4. 直接探测候选端口 `/healthz`、`/readyz`，并通过候选端口完成 DB/API 门禁。
5. 原子写入 upstream fragment，执行 `nginx -t && nginx -s reload`，再从目标机HTTPS入口确认 `/version` 是候选版本。
6. 记录 `handoff_elapsed_ms`；入口确认失败则先恢复旧 fragment/reload，再停止候选，旧实例持续服务。
7. 切流后门禁全部通过，才更新 `current`、`active-port`、`active-service`，标记 `verified=true`，并停止旧 active service 进入 graceful drain。
8. 旧实例停止失败不影响候选继续服务，保留 `active-service` 记录供人工清理。

## 回滚

回滚顺序必须是：恢复旧 upstream fragment → `nginx -t` → graceful reload → 确认旧版本入口 → 停止失败候选 → 恢复 `current` 和 active 状态文件。禁止先停止旧实例再尝试恢复代理。

数据库迁移必须向前兼容旧版本；binary rollback 不等于 schema rollback。破坏性迁移在切换前阻断。

## 本地

`local-host-blue-green.sh` 要求显式 `--proxy`，代理命令接收目标端口并在确认后才提升 `current`。没有本地反代时，继续使用 `local-host-deploy.sh` 的安全单端口回切路径，并明确标记为非蓝绿、非2秒SLO。

## 245约束

245 仍是人工部署和人工门禁环境。本方案提供兼容资源和脚本，但任何自动化 runner 都不得自行执行245发布；没有合规 gate artifact 时，154也必须保持 `manual_required/NO-GO`。
