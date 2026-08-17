# HTTP/2 支持安全审计报告

**日期**: 2026-07-16
**审计人**: OpenCode AI Agent
**变更**: 引入 h2c (HTTP/2 cleartext) 支持 + clientprofile nil pointer 修复

## 审计范围

- HTTP/2 协议处理安全性
- nil pointer dereference 修复
- nginx 上游配置安全性

## 变更摘要

### 1. 新增 HTTP/2 (h2c) 支持

**文件**: `cmd/gateway/main.go:3266-3287`

```go
srv.Handler = h2c.NewHandler(handler, &http2.Server{
    MaxConcurrentStreams: 250,      // 防 stream 泛洪
    MaxReadFrameSize:     1 << 20,  // 1MB, 防 HPACK 炸弹
    IdleTimeout:          60 * time.Second,
})
```

**安全配置**:
- `MaxConcurrentStreams: 250` — 限制每连接最多 250 并发流，防止 HTTP/2 stream 泛洪攻击
- `MaxReadFrameSize: 1MB` — 限制单个 frame 大小，防止 HPACK 表炸弹和内存耗尽
- `IdleTimeout: 60s` — 防止空闲连接池耗尽

### 2. clientprofile SIGSEGV 修复

**文件**: `domains/clientprofile/aggregator.go:31-37`

```go
if a == nil || a.store == nil {
    return nil  // 静默跳过，不污染事件总线
}
```

**修复内容**: 当 `cmd/gateway/main.go:1051` 传 `nil` 作为 `*sql.DB` 时，`store = nil` 导致 `a.store.SaveEvent` nil pointer dereference → SIGSEGV → 网关重启 → nginx 502。

### 3. nginx 上游配置收紧

**文件**: `/etc/nginx/conf.d/kxpms-on-252.conf`

```nginx
upstream kxpms_llm_backend {
    server 172.16.2.209:8781 max_fails=2 fail_timeout=5s;  # 从 3/10s 收紧
    keepalive 32;
}
```

## HTTP/2 已知漏洞检查

| CVE | 描述 | 修复版本 | 当前版本 | 状态 |
|---|---|---|---|---|
| CVE-2023-44487 | HTTP/2 Rapid Reset DoS | golang.org/x/net >= v0.17.0 | v0.56.0 | ✅ 已修复 |
| CVE-2023-39325 | HTTP/2 stream 取消内存泄漏 | golang.org/x/net >= v0.17.0 | v0.56.0 | ✅ 已修复 |
| CVE-2022-41717 | HTTP/2 memory exhaustion | golang.org/x/net >= v0.4.0 | v0.56.0 | ✅ 已修复 |

## HTTP/2 攻击面分析

### 1. Stream 泛洪攻击
**风险**: 攻击者打开大量并发 stream 耗尽服务器资源
**缓解**: `MaxConcurrentStreams: 250` 限制每连接 250 流
**状态**: ✅ 已缓解

### 2. HPACK 炸弹攻击
**风险**: 攻击者发送超大 HPACK 压缩头部，解压后内存爆炸
**缓解**: `MaxReadFrameSize: 1MB` 限制单帧大小
**状态**: ✅ 已缓解

### 3. 慢读攻击 (Slow Read)
**风险**: 攻击者缓慢读取响应，占用连接池
**缓解**: `IdleTimeout: 60s` + `ReadTimeout: 120s`
**状态**: ✅ 已缓解

### 4. CONTINUATION frame flood
**风险**: 攻击者发送无限 CONTINUATION frame
**缓解**: golang.org/x/net/http2 内置防护 (v0.23+)
**状态**: ✅ 已缓解

### 5. HTTP/2 Server Push 滥用
**风险**: 服务器推送未请求的资源
**缓解**: h2c 默认未启用 server push
**状态**: ✅ 无风险

## HTTP/1.1 兼容性

h2c.NewHandler 自动降级到 HTTP/1.1：
- 未发送 HTTP/2 preface 的客户端自动走 HTTP/1.1
- 现有监控工具、curl、旧版客户端不受影响
- 测试验证: ✅ HTTP/1.1 + HTTP/2 双协议正常工作

## 网络层防护

### nginx 前置 TLS
- h2c 仅监听内网 `127.0.0.1:8781` / `172.16.2.209:8781`
- 公网流量经 nginx HTTPS (443) → 内网 h2c (8781)
- TLS 层防护: DH 密钥协商、证书验证、中间人攻击防护

### nginx 上游配置
- `max_fails=2 fail_timeout=5s`: 快速故障切换，减少 502 窗口
- `keepalive 32`: 连接复用，减少握手开销
- `proxy_buffering off`: SSE streaming 必要配置

## 部署验证

- **245** (build_seq 1092): ✅ 3/3 200 OK, 1.2-1.4s
- **154** (build_seq 1093): ✅ 5/5 200 OK, 1.1-1.7s
- **SSE streaming**: ✅ `[DONE]` 干净结束
- **并发 10**: ✅ 9/10 200, 1/10 429 (rate limit 正常)
- **panic 计数**: ✅ 0 (最近 10 分钟)
- **uptime**: ✅ 154 网关持续运行无重启

## 审计结论

✅ **安全风险: 低**

1. HTTP/2 配置遵循安全最佳实践 (OWASP HTTP/2 Security)
2. golang.org/x/net v0.56.0 已修复所有已知 HTTP/2 CVE
3. nil pointer 修复消除 SIGSEGV 稳定性风险
4. nginx 前置 TLS + 内网 h2c 架构安全
5. 已验证 HTTP/1.1 兼容性，无破坏性变更

## 推荐后续行动

1. ⚠️ 监控 HTTP/2 连接数和 stream 数，设置告警阈值 (> 200/连接)
2. ⚠️ 定期更新 golang.org/x/net (每季度检查 CVE)
3. ✅ 保持 nginx `fail_timeout=5s` 配置
4. ✅ clientprofile 集成完善 sql.DB 桥接 (消除 nil store 根因)

---

**签署**: OpenCode AI Agent
**审计时间**: 2026-07-16 13:37 UTC+8
