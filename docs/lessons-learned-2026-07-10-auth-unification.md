# 经验总结：统一认证方案 (2026-07-10)

本文档记录了 2026-07-10 统一 admin 认证到 JWT 方案（commit 9b58404e + c43c3999）过程中积累的实战经验教训，供后续参考。

## 1. 背景

### 1.1 旧认证体系（混乱）
代码库存在 **7 套不同的 auth 路径**：
1. Global data-plane API key (`LLM_GATEWAY_API_KEY`) — `middleware/auth_mw.go`
2. Admin JWT (HttpOnly cookie 或 Bearer) — `admin/auth.go:AdminMiddleware`
3. Admin legacy sk-* API key（DB lookup）— `admin/auth.go:verifyAdminAuth`（已删除）
4. Admin super-admin role — `admin/auth.go:SuperAdminMiddleware`
5. Static ops token (`LLM_GATEWAY_ADMIN_API_KEY`) — `middleware/admin_token_mw.go`
6. Data-plane sk-* API key — `domains/authentication/KeyVerifier`
7. Login rate limiter + legacy admin user/password — `admin/auth.go:handleLogin`

前端 `authBearer()` 对 cookie 用户返回空字符串，导致：
- 后端 `?full=true` 路径 inline `Bearer` 前缀检查 401
- `/api/models/name-mapping` 等 admin 端点 cookie 用户拿到 401
- `App.vue:loadVersion` 发送字面量 `Authorization: Bearer cookie`

### 1.2 新认证体系（统一）
- **后端**：只接受 JWT (HS256, 通过 cookie 或 Bearer header)
- **前端**：JWT 持久化到 localStorage `llmgw_jwt`，`authBearer()` 返回真实 JWT
- **删除**：admin sk-* API key 路径
- **保留**：ops sk-* (`/healthz/full`, `/metrics`)、data-plane sk-* (`/v1/*`)

---

## 2. 关键经验教训

### 2.1 ⚠️ CRITICAL — `nohup` 启动会丢失 systemd env (踩坑)
**症状**：binary panic `CORS origins must be explicitly configured`。
**原因**：`nohup ./llm-gateway-go` 启动的进程**没有 systemd 注入的 `LLM_GATEWAY_*` env vars**。
**解决**：**永远用 systemd**：`systemctl restart llm-gateway-go.service`。
**systemd unit** (`/etc/systemd/system/llm-gateway-go.service`) 通过 `EnvironmentFile=/etc/llm-gateway-go/env` 加载所有 env。
**deploy 脚本必须用 systemd restart**，不能 nohup。

### 2.2 ⚠️ CRITICAL — Postgres 连接 60s 后被禁用（v326+）
**症状**：日志 `postgres connected` → 60s 后 `postgres disabled: timeout: context deadline exceeded`。
**影响**：所有 admin/DB-backed 端点返回 503 `database not configured`。
**临时缓解**：定期 `systemctl restart llm-gateway-go.service`（不应是常态）。
**根因**：binary 启动时的 PG keepalive 配置问题（已存在的，与本次改动无关）。
**跟踪**：观察是否需要调小 pgpool 的连接超时配置或加 tcp_keepalives_idle。

### 2.3 ⚠️ CRITICAL — `handleAuthMe` 必须 nil-check `h.db`
**症状**：v327 部署后所有 `/api/auth/me` 调用 panic。
**原因**：当 DB 不可用时 `h.db` 为 nil，但 `h.db.QueryRow` 没有 nil 守卫。
**修复** (`admin/users.go`)：
```go
if h.db == nil {
    writeError(w, http.StatusServiceUnavailable, "database not configured")
    return
}
```
**教训**：所有用 `h.db` 的 handler 都已加 nil check（`audit_log.go`, `catalog.go` 等），**handleAuthMe 之前漏了**。新写 handler 必须 nil-check。

### 2.4 ⚠️ CRITICAL — 改了文件就一定要 build 验证 dist chunk
**症状**：v325 → v327 部署后浏览器报 `ReferenceError: authBearer is not defined`。
**原因**：`App.vue` 在 9b58404e commit 中 `loadVersion` 改成 `authBearer()` 调用，但 **import 没更新**。TypeScript / Vite 在 build 时**不会**对未导入的自由变量报错（被 rollup tree-shaking 误处理）。
**修复** (`web/src/App.vue`)：
```typescript
import { store, clearAll, clearJwt, clearMustChangePasswordFlag,
         isSuperAdmin as checkSuperAdmin, isPlatformOpsView as checkPlatformOps,
         markAuthHydrated, setJwtToken, setUserInfo, authBearer } from './store'
//                                                                        ^^^^^^^^^^^ 加上
```
**教训**：每次 commit 之前**先 build dist，强制浏览器加载新 chunk**，发现运行时错误。

### 2.5 ⚠️ MEDIUM — `handleAuthMe` 应返回 access_token 给 SPA
**场景**：用户用 cookie 登录（之前版本），新版本用 localStorage JWT 持久化。
**问题**：旧 cookie 仍然有效，但 SPA 没 JWT 在 localStorage，每次 `getAuthMe` 后**丢失 mustChangePassword 强制**，且 `authBearer()` 返回空，导致 `Authorization` header 缺失。
**修复** (`admin/users.go:handleAuthMe`)：在响应里返回 `access_token` 和 `expires_at`，让 SPA 持久化。
```go
writeJSON(w, http.StatusOK, map[string]any{
    "access_token": token,
    "expires_at":   expiresAt.Format(time.RFC3339),
    "user":         u,
})
```
SPA 端 (`App.vue:onMounted`)：
```ts
const me = await getAuthMe()
const meAny = me as any
if (meAny?.access_token) setJwtToken(meAny.access_token)
setUserInfo(me)
```

### 2.6 ⚠️ LOW — 删除 auth 路径时也要更新 LoginModal 等 UI
**问题**：`AdminMiddleware` 删除 sk-* 回退后，旧的 LoginModal `setApiKey(resp.api_key)` 路径会**静默失败**——SPA 存了 `apiKey` 但所有 admin 端点 401。
**修复** (`LoginModal.vue`)：显示明确错误
```ts
} else if (resp.api_key) {
  error.value = '此账号不再支持 API key 登录，请使用用户名/密码登录。'
}
```

### 2.7 ⚠️ LOW — `isAdminProtectedPath` 白名单
**问题**：把 `/api/auth/me` 加到 `isAdminProtectedPath` 让 401 自动 clear + 重定向，避免 App.vue 手工 catch 的脆弱性。
**但**：保留 catch 在 App.vue 的好处是更显式。
**建议**：加白名单 + 在 App.vue 也保留 catch 作为双重保险。

---

## 3. 部署 Checklist（deploy-154.sh 必须做的）

1. ✅ **Pre-deploy**: `git diff --quiet HEAD` 强制工作区干净
2. ✅ **Build dist** (pnpm/npm build)，**确认 dist 包含所有 chunk 引用**，强制浏览器刷新
3. ✅ **Compile binary**: `GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build` + `-trimpath` + `-ldflags` 注入版本
4. ✅ **Stop service** (避免 mmap 锁)
5. ✅ **Upload via systemd restart** — **不要 nohup**（参考 §2.1）
6. ✅ **Verify env-file** — `LLM_GATEWAY_DATABASE_URL`, `LLM_GATEWAY_SECRET_KEY`, `LLM_GATEWAY_ADMIN_API_KEY` 必须有
7. ✅ **Smoke test**:
   - `GET /healthz` → 200
   - `GET /healthz?full=true` (无 auth) → 401
   - `POST /api/auth/token` (admin 凭据) → 200 + access_token
   - `GET /api/auth/me` (Bearer JWT) → 200 + 包含 access_token
   - `GET /api/auth/me` (Bearer sk-*) → 401 (sk-* admin 路径已删除)
   - `GET /api/models/name-mapping` (Bearer JWT) → 200
8. ✅ **Browser 强制刷新** (用户) — 新 chunk hash 改变

---

## 4. 验证后端 binary 包含新路由的快速方法

```bash
# 在远程服务器上
strings /opt/llm-gateway-go/llm-gateway-go.v328.linux.amd64 | grep -E "name-mapping|/api/models"
```

如果没看到 `name-mapping` 字符串，说明 binary 是旧的，需要重新部署。

---

## 5. 回滚步骤

如果新部署后 panic 或 502：

```bash
# 1. SSH to 154
ssh -p 25022 root@47.97.111.154

# 2. 查看可用备份
ls -lt /opt/llm-gateway-go/llm-gateway-go.bak-* | head -5

# 3. 回滚 symlink 指向旧 binary
ln -sf /opt/llm-gateway-go/llm-gateway-go.bak-YYYYMMDD_HHMMSS /opt/llm-gateway-go/llm-gateway-go
systemctl restart llm-gateway-go.service

# 4. 也需要回滚 web dist
rsync -avz --delete /opt/llm-gateway-go/web.bak.YYYYMMDD_HHMMSS/ /opt/llm-gateway-go/web/
```

git 端：
```bash
git revert c43c3999  # 撤销 audit fixes
git revert 9b58404e  # 撤销 unified auth
git push origin main
```

---

## 6. 部署脚本的硬门禁要求

deploy-154.sh 必须检查：
- [ ] `env-injector inject aliyun-gateway-154` 已运行（4-KEY 都已注入）
- [ ] `SSHPASS` 已 export
- [ ] Git 工作区 clean
- [ ] SSH 可达
- [ ] 编译后 binary 是 Linux AMD64
- [ ] systemd unit 存在且 active
- [ ] env-file 在 `/etc/llm-gateway-go/env` (mode 600)
- [ ] 部署后立刻 smoke test `/healthz` + `/api/auth/me`

---

## 7. ⚠️ CRITICAL — SPA 前端静态文件部署必做 (2026-07-10 新增)

### 7.1 症状：浏览器打开首页显示 JSON 版本信息而非 SPA

**现象**：
```json
{"service":"llm-gateway-go","version":"2.4.2-8eba7cea-20260710-964","git_sha":"f467d54b","build_seq":"964"}
```

**根因**：
1. **后端 binary 部署了，但 `web/dist/` 前端文件未部署**
2. **systemd service 缺少 `LLM_GATEWAY_STATIC_DIR` 环境变量**
3. `NewStaticHandler(cfg.StaticDir)` 返回 `nil` → 走 fallback JSON 响应

### 7.2 完整部署 Checklist

部署 binary 时**必须同时部署 web 前端**：

```bash
# 1. 本地构建 SPA
cd web
pnpm install
pnpm build  # 生成 dist/

# 2. 上传 dist/ 到服务器
scp -r web/dist/* root@SERVER:/opt/llm-gateway-go/web/

# 3. 确认 systemd service 包含 STATIC_DIR
grep STATIC_DIR /etc/systemd/system/llm-gateway-go.service
# 应该有：Environment=LLM_GATEWAY_STATIC_DIR=/opt/llm-gateway-go/web

# 4. 如果没有，添加并 reload
echo 'Environment=LLM_GATEWAY_STATIC_DIR=/opt/llm-gateway-go/web' >> /etc/systemd/system/llm-gateway-go.service
systemctl daemon-reload
systemctl restart llm-gateway-go

# 5. 验证
curl -fsS http://127.0.0.1:8780/ | head -c 200
# 应该返回 HTML: <!DOCTYPE html>
```

### 7.3 为什么这个问题反复出现？

**历史原因**：
- 早期部署时只关注 binary，web SPA 是后来加的
- 154/252 的 systemd service 是手工创建的，`STATIC_DIR` 未写入模板
- deploy 脚本只上传 binary，没有 `web/` 部署步骤

**解决方案**：
- ✅ `scripts/deploy-154.sh` / `deploy-252.sh` 必须包含 web 上传步骤
- ✅ systemd service 模板统一加 `STATIC_DIR` (参考 `packaging/systemd/llm-gateway-go.service.template`)
- ✅ smoke test 加上 `curl -I / | grep -q "text/html"` 验证 SPA 可用

### 7.4 日志判断 STATIC_DIR 是否生效

```bash
journalctl -u llm-gateway-go -n 100 | grep -i "serving Vue SPA"
```

- ✅ 看到 `serving Vue SPA dir=/opt/llm-gateway-go/web` → 正常
- ❌ 没有这条日志 → `NewStaticHandler` 返回 nil，检查目录权限和路径

### 7.5 快速修复脚本 (应急)

```bash
#!/bin/bash
# 应急修复：给已部署的 252/154 补上 web
cd /path/to/llm-gateway-go-cursor
pnpm --prefix web build
scp -r web/dist/* root@SERVER:/opt/llm-gateway-go/web/
ssh root@SERVER "grep -q STATIC_DIR /etc/systemd/system/llm-gateway-go.service || \
  sed -i '/LLM_GATEWAY_CORS_ORIGINS/a Environment=LLM_GATEWAY_STATIC_DIR=/opt/llm-gateway-go/web' \
  /etc/systemd/system/llm-gateway-go.service && \
  systemctl daemon-reload && systemctl restart llm-gateway-go"
```

---

## 8. 经验汇总：完整部署流程 (2026-07-10 最终版)

```bash
# === Phase 1: 本地构建 ===
pnpm --prefix web build
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o bin/llm-gateway-go.linux.amd64 ./cmd/gateway

# === Phase 2: 上传 ===
scp bin/llm-gateway-go.linux.amd64 root@SERVER:/opt/llm-gateway-go/llm-gateway-go.new
scp -r web/dist/* root@SERVER:/opt/llm-gateway-go/web/

# === Phase 3: 原子切换 ===
ssh root@SERVER '
  systemctl stop llm-gateway-go
  mv /opt/llm-gateway-go/llm-gateway-go /opt/llm-gateway-go/llm-gateway-go.bak-$(date +%Y%m%d_%H%M%S)
  mv /opt/llm-gateway-go/llm-gateway-go.new /opt/llm-gateway-go/llm-gateway-go
  chmod +x /opt/llm-gateway-go/llm-gateway-go
  
  # 确保 STATIC_DIR 存在
  grep -q STATIC_DIR /etc/systemd/system/llm-gateway-go.service || \
    sed -i "/LLM_GATEWAY_CORS_ORIGINS/a Environment=LLM_GATEWAY_STATIC_DIR=/opt/llm-gateway-go/web" \
    /etc/systemd/system/llm-gateway-go.service
  
  systemctl daemon-reload
  systemctl start llm-gateway-go
  sleep 5
  
  # Smoke test
  curl -fsS http://127.0.0.1:8780/healthz
  curl -I http://127.0.0.1:8780/ | grep -q "text/html"
'
```

**关键点**：
1. ✅ **Binary + Web 同时部署**（缺一不可）
2. ✅ **systemd service 必须有 STATIC_DIR**
3. ✅ **Smoke test 验证 SPA 返回 HTML**
4. ✅ **nginx 配置 `location /` proxy_pass 到正确的 gateway 端口**
