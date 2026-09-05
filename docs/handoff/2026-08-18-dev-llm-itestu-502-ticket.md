# Ticket 草稿 — dev llm.itestu.cn 502（252 nginx 上游无监听）

> 状态：草稿（待登记到 issue 跟踪系统）。来源：2026-08-17 部署门禁只读审计。
> 关联：`docs/session-logs/2026/08/2026-08-17-deploy-gate-readonly-audit.md`

## 标题

dev 环境 `llm.itestu.cn` 持续 502：252 nginx vhost 上游 `127.0.0.1:11008` 无监听进程

## 严重级 / 影响

- 级别：P3（仅影响 dev 环境；**不阻塞** 245→154 晋级路径——245 healthz 已 200，
  `llm.kxpms.cn`（生产，指向 154）亦正常）
- 影响：无法通过 dev 域名做浏览器联调；直接 IP/端口访问不受影响（如仍可达）

## 复现

```bash
curl -sS -o /dev/null -w '%{http_code}\n' https://llm.itestu.cn/healthz   # → 502（或经 252 nginx 后 502）
```

## 根因（2026-08-17 实测）

252（115.29.212.252）上 nginx 的 `llm.itestu.cn` vhost 将请求代理到
`127.0.0.1:11008`，但该端口当前**没有任何进程监听**（dev 网关实例未运行或未部署到该端口）。

## 修复选项（按建议顺序）

1. 在 252 上启动/重新部署 dev 网关实例并绑定 `127.0.0.1:11008`
   （对照 `configs/env-252.sh` 与 252 上的 compose/systemd 单元确认既有约定）。
2. 若 dev 实例已迁址：更新 nginx vhost `proxy_pass` 至实际上游后 `nginx -s reload`。
3. 若 dev 域名已废弃：删除该 vhost，避免后续误判为服务故障。

## 验证

```bash
# 252 上确认监听
ss -ltnp | grep 11008
curl -sS -o /dev/null -w '%{http_code}\n' https://llm.itestu.cn/healthz   # 期望 200 + version JSON
```

## 备注

- 2026-08-16 handoff 曾将 dev 阻塞表述为影响 245 晋级，实测已证伪（见关联审计 §3 事实校正表）。
- 修复属运维动作，不涉及本仓库代码变更。
