# 2026-09-08 — safehttpclient DNS 重绑定 TOCTOU 修复 + CGNAT 封锁

合并远程 main 时对 `internal/safehttpclient`（W4-F6 SSRF 防护交付物）做代码审计，发现两个安全缺口。

## 根因

- `dialWithValidation` 先解析 DNS 并验证所有 IP，随后把**主机名** addr 原样交给 `net.Dialer` 拨号——拨号器内部会**再次**解析 DNS。攻击者控制权威 DNS 时，可在"验证解析"与"连接解析"之间翻转记录（经典重绑定 TOCTOU），全部校验被绕过。
- 封锁 CIDR 缺少 CGNAT 段 `100.64.0.0/10`。阿里云 ECS 元数据服务 `100.100.100.200` 在该段内（本项目部署在阿里云体系），webhook 渠道可被 SSRF 读取元数据凭据。

## 修复

| 项 | 行为 |
|---|---|
| 重绑定 TOCTOU | 验证后直接拨**已验证 IP**（`JoinHostPort(ip, port)`），不再回传主机名；按 `network` 参数选择匹配地址族（tcp/tcp4/tcp6），无匹配族时报错拒绝。TLS SNI/证书校验仍基于请求目标主机名（http.Transport 从请求派生），HTTPS 语义不变 |
| CGNAT 封锁 | `blockedCIDRs` 增加 `100.64.0.0/10` 与基准测试段 `198.18.0.0/15` |
| 测试 | `TestBlockedIPRanges` 新增 11 个用例：CGNAT 边界、阿里云元数据、基准段、IPv4-mapped IPv6（`::ffff:127.0.0.1` 等）绕过尝试、段外公网不误伤 |

## 验证

- `go test ./internal/safehttpclient -count=1` 全绿
- `go build ./...` + webhook/notification 渠道测试无回归

## 影响面

- `domains/notification/webhook.go` 是唯一消费方；allowlist 语义未变（白名单主机仍直接拨号）
- 行为变化：主机名请求现在固定连接验证过的首个同族 IP（放弃 Happy Eyeballs 双栈并行），属安全加固的预期取舍
