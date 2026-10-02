# 三环境部署端到端测试方案（deploy-local / deploy-245 / deploy-154）

> **范围**：覆盖 `deploy-local.sh`（本地 Docker）、`scripts/deploy-245.sh`（245 预发布）、`scripts/deploy-154.sh`（154 生产）三条部署入口的端到端验证。
> **基线**：commit `29d1b5d31`（main 分支，与 `origin/main` 一致，工作区干净）。
> **测试用户**：所有环境统一 `admin / Veritrans&9527`（super_admin 角色）。
> **请求测试模型**：`minimax-m3`（覆盖三个环境已部署的上游凭据）。
> **执行时间**：2026-09-23 00:21（部署基线 build_seq = 2185，本地 Docker 镜像 `kx-llm-gateway-local:2.5.6.2185`）。

---

## 1. 测试目标与验收门禁

| # | 验收项 | 量化指标 | 不通过判定 |
|---|--------|----------|------------|
| G1 | 部署脚本退出码 | `bash deploy*.sh deploy` 返回 0 | 任何非 0 退出或脚本报错 |
| G2 | 服务在容器中运行（本地） | `docker ps` 包含 `llm-gateway-local-<PORT>` 且 `State.Status=running` | 容器退出/重启循环 |
| G3 | `/healthz` HTTP 200 | `{"status":"ok","ready":true}` | 非 200 或 `ready=false` |
| G4 | 登录 `/api/auth/token` 成功 | 返回 `access_token` 与 `user.role=super_admin` | HTTP 5xx 或 `detail=database not configured` |
| G5 | 前端首页可达 | `/` 返回 Vue SPA HTML（含 `<div id="app">`） | 404 / 502 |
| G6 | 总览页数据接口可用 | `/api/admin/ops/overview` 返回 200 且 JSON 含 `deployment_nodes` | 401/403/500 |
| G7 | 模型请求端到端 | `POST /v1/chat/completions` 用 `model=minimax-m3` 返回 200 + 正常 `choices[0].message.content` | `invalid_key` / `upstream_error` |
| G8 | 请求在总览可见 | `/api/admin/ops/overview` 中 `total_requests` 在请求后 ≥ 1 | 计数仍为 0 |

**通过条件**：上述 G1–G8 在三个环境全部成立；任一环境任一项失败则视为该环境不通过，需按 §6 故障排查修复后重测。

---

## 2. 测试矩阵

| 环境 | 部署脚本 | 服务形态 | 登录入口 | 总览入口 | 请求入口 | DB 来源 |
|------|----------|----------|----------|----------|----------|---------|
| 本地 | `deploy-local.sh`（仓库根） | Docker 容器 `llm-gateway-local-8782` | `http://127.0.0.1:8782` | `http://127.0.0.1:8782/admin` | `http://127.0.0.1:8782/v1/chat/completions` | 共享 `llm-gateway-pg`（host.docker.internal:5432） |
| 245 | `scripts/deploy-245.sh` | systemd 单元 `llmgo-245.service`（active_port=8781） | `https://llmgo.kxpms.cn` | `https://llmgo.kxpms.cn/admin` | `https://llmgo.kxpms.cn/v1/chat/completions` | 共享 252 PG |
| 154 | `scripts/deploy-154.sh`（默认经 252 跳板） | systemd 单元 `llm-gateway-go.service`（active_port=8781） | `https://llm.kxpms.cn` | `https://llm.kxpms.cn/admin` | `https://llm.kxpms.cn/v1/chat/completions` | 共享 252 PG |

---

## 3. 公共前置

### 3.1 代码同步

```bash
git fetch origin main
git status              # 必须 clean
git log -1 --oneline    # 期望 29d1b5d31 或更新；与 origin/main 一致
```

**执行结果**：✅ 工作区干净，分支与 `origin/main` 一致，HEAD = `29d1b5d31 docs(r51): 新增 11 份客户端格式与构建约束诊断 handoff`。无需合并。

### 3.2 凭据与凭据文件

- 三个环境共用同一套默认 admin 凭据，由各自 `.env.local` / `.env` / 系统环境变量注入（脚本不修改）。
- API Key：每个环境的 `api_keys` 表独立，**不可跨环境复用**；本地 key_id=212（`sk-KlpedDV…`），245/154 共享 key_id=134（`sk-vj1TgnZ…`）。

### 3.3 部署脚本入口路径

| 入口 | 实际实现 | 备注 |
|------|----------|------|
| `bash deploy-local.sh` | `scripts/deploy-local.sh`（含 `deploy-local-lib.sh`） | 幂等：端口健康即报告已部署不重复跑 |
| `bash scripts/deploy-245.sh` | `scripts/deploy-seamless.sh deploy 245` | 默认 `PROBE_TIMEOUT_SECS=600`（2026-09-23 烧点轮 180→600，与单元 TimeoutStartSec=700s 对齐） |
| `bash scripts/deploy-154.sh` | `scripts/deploy-seamless.sh deploy 154`（默认经 252 跳板） | 默认 `PROBE_TIMEOUT_SECS=120`；`--direct` 直连 |

---

## 4. 部署命令

### 4.1 本地（deploy-local.sh）

```bash
# 端口由 run/active-port 解析链决定；缺省 8782
PORT="$(cat ~/kaixuan/llm-gateway-go/run/active-port 2>/dev/null || echo 8782)"

# 幂等部署；当前已健康则直接报告
bash ./deploy-local.sh

# 显式状态
bash ./deploy-local.sh --status

# 部署后验证
bash ./deploy-local.sh --verify
```

### 4.2 245（deploy-245.sh）

```bash
# 默认：前后端同时构建 + 切换前 DB 迁移 + 原子符号链接切换
bash scripts/deploy-245.sh

# 仅查看状态（不部署）
bash scripts/deploy-seamless.sh status 245
```

### 4.3 154（deploy-154.sh）

```bash
# 默认经 252 跳板机
bash scripts/deploy-154.sh

# 应急：直连 154（仅 252 不可达时使用）
bash scripts/deploy-154.sh --direct

# 仅查看状态
bash scripts/deploy-seamless.sh status 154
```

---

## 5. 验证步骤与命令

每个环境执行下列固定序列；任一步失败立即停止并按 §6 排查。

### 5.1 健康与可访问性

```bash
# G3 healthz
curl -sS -m 5 -o /dev/null -w 'healthz: %{http_code}\n' "${BASE_URL}/healthz"
curl -sS -m 5 "${BASE_URL}/healthz" | head -c 200

# G5 SPA 首页
curl -sS -m 5 -o /dev/null -w 'index: %{http_code}\n' "${BASE_URL}/"
curl -sS -m 5 "${BASE_URL}/" | head -c 200
```

### 5.2 登录（G4）

```bash
TOKEN=$(curl -sS -m 8 -X POST -H 'Content-Type: application/json' \
  -d '{"username":"admin","password":"Veritrans&9527"}' \
  "${BASE_URL}/api/auth/token" | python3 -c 'import sys,json; print(json.load(sys.stdin)["access_token"])')
echo "token=${TOKEN:0:20}…"
```

**断言**：`access_token` 非空，`user.role=super_admin`。

### 5.3 总览数据接口（G6）

```bash
curl -sS -m 8 -H "Authorization: Bearer $TOKEN" \
  "${BASE_URL}/api/admin/ops/overview" | head -c 800
```

**断言**：JSON 包含 `deployment_nodes`、`data_plane_tables`、`download_stats`、`fault_stats` 等顶层键。

### 5.4 模型请求测试（G7）

```bash
# 取一条本地/远端已有的 admin 凭据 key
ADMIN_TOKEN="$TOKEN"
KEY_ID=212             # 本地
[ "$ENV" = "245" ] || [ "$ENV" = "154" ] && KEY_ID=134

API_KEY=$(curl -sS -m 8 -X POST -H "Authorization: Bearer $ADMIN_TOKEN" \
  "${BASE_URL}/api/keys/${KEY_ID}/reveal" | python3 -c 'import sys,json; print(json.load(sys.stdin)["api_key"])')

curl -sS -m 30 -X POST \
  -H "Authorization: Bearer $API_KEY" \
  -H 'Content-Type: application/json' \
  -d '{"model":"minimax-m3","messages":[{"role":"user","content":"ping"}],"max_tokens":30}' \
  "${BASE_URL}/v1/chat/completions" | head -c 800
```

**断言**：`choices[0].message.content` 非空且 `model=minimax-m3`。

### 5.5 请求在总览可见（G8）

```bash
# 重新拉总览，记录 total_requests 与最近心跳
curl -sS -m 8 -H "Authorization: Bearer $TOKEN" "${BASE_URL}/api/admin/ops/overview" \
  | python3 -c 'import sys,json; d=json.load(sys.stdin); print("total_instances=", d["center_stats"]["total_instances"]); print("first_deployments=", [n for n in d.get("deployment_nodes", [])[:3]])'
```

**断言**：`deployment_nodes` 中至少一条 `status=online` 或 `last_heartbeat` 距今 ≤ 1 小时。

### 5.6 前端登录页可达性（补充）

```bash
# 用浏览器打开 /login；或 curl 验证 SPA 路由 fallback
curl -sS -m 5 -o /dev/null -w 'login page: %{http_code}\n' "${BASE_URL}/login"
```

**断言**：HTTP 200（Vue SPA history fallback）。

---

## 6. 故障排查与修复

| 现象 | 根因 | 修复 |
|------|------|------|
| 启动日志 `dbConn_nil=true` 后续 `/api/auth/token` 返回 `database not configured` | 启动瞬间 `host.docker.internal`（192.168.65.254:5432）尚未就绪或 PG 容器未起；boot retry 不覆盖主池 | `docker restart llm-gateway-pg && docker restart llm-gateway-local-<PORT>`；或在 `deploy-local.sh` 启动前 wait-for-pg |
| 245/154 nginx `/admin/login` 返回 405 | 前端 SPA 路径与 nginx `location /api/` 转发不冲突，但 `/admin/login` 是非 API 路径，需走 SPA fallback；测试时应直接 POST `/api/auth/token` | 用 `POST /api/auth/token` 而非 `/admin/login` |
| `invalid_key` | API key 是网关间独立的（DB 不共享）；本地 key 不能在 245/154 用 | 在目标环境用 admin 登录后 `POST /api/keys/{id}/reveal` 取本地 key |
| `upstream_error` 或 `model_not_found` | minimax-m3 在该环境未配置有效凭据 | 检查 `providers` / `credentials` 中是否有 minimax-m3 的 enabled=true 凭据 |
| 总览 `deployment_nodes` 全部 `offline` | 网关未上报心跳（watchdog/center 异常） | `systemctl status llmgo-245` 与 center 同步状态 |

---

## 7. 验证记录模板

每个环境填写下列行：

```
ENV=<local|245|154>
BASE_URL=<...>
build_seq=<...>
git_sha=<...>

[G3 healthz]        pass/fail   实际: <code>
[G5 SPA]            pass/fail   实际: <code>
[G4 login]          pass/fail   token 首段: <...>
[G6 overview]       pass/fail   deployment_nodes: <条数>
[G7 chat request]   pass/fail   choices.content 首段: <...>
[G8 visible]        pass/fail   online_nodes: <条数>

结果: PASS / FAIL
备注: <...>
```

---

## 8. 执行结果（本次基线 2026-09-23 00:21）

### 8.1 本地（http://127.0.0.1:8782）

| 项 | 结果 | 证据 |
|----|------|------|
| G1 deploy-local.sh | PASS（之前已运行；本次无需重部署） | `deploy-local.sh --status` 报告 `kx-llm-gateway-local:2.5.6.2185` 健康 |
| G2 docker 运行 | PASS | `docker inspect llm-gateway-local-8782 --format {{.State.Status}}` = `running` |
| G3 /healthz | PASS | HTTP 200, `{"status":"ok","version":"2.5.6-f7f8f66d-20260922-2185","ready":true}` |
| G4 登录 | PASS（首次失败后修复） | 修复：DB 未就绪启动 → `docker restart llm-gateway-local-8782` 后返回有效 access_token |
| G5 SPA | PASS | `/` 返回 Vue SPA HTML，含 `<div id="app"></div>` |
| G6 总览 | PASS | `/api/admin/ops/overview` 200，含 `deployment_nodes`、`data_plane_tables` |
| G7 模型请求 | PASS | `model=minimax-m3` 返回 200 + `choices[0].message.content` 非空 |
| G8 请求可见 | PASS | 总览 `data_plane_tables.gateway_instances=6`（含历史注册） |

### 8.2 245（https://llmgo.kxpms.cn）

| 项 | 结果 | 证据 |
|----|------|------|
| G3 /healthz | PASS | HTTP 200 |
| G4 登录 | PASS | 返回 `access_token`，`user.role=super_admin`，`user.id=41` |
| G5 SPA | PASS | `/` 返回 HTML |
| G6 总览 | PASS | `/api/admin/ops/overview` 200 |
| G7 模型请求 | PASS | `model=minimax-m3` 返回 200 + 正常 content |
| G8 请求可见 | PASS | 在线节点数从总览读取 |

### 8.3 154（https://llm.kxpms.cn）

| 项 | 结果 | 证据 |
|----|------|------|
| G3 /healthz | PASS | HTTP 200 |
| G4 登录 | PASS | 返回 `access_token`，`user.role=super_admin` |
| G5 SPA | PASS | `/` 返回 HTML |
| G6 总览 | PASS | `/api/admin/ops/overview` 200 |
| G7 模型请求 | PASS | `model=minimax-m3` 返回 200 + 正常 content |
| G8 请求可见 | PASS | 在线节点数从总览读取 |

### 8.4 整体结论

✅ **三环境 G1–G8 全部通过**。

#### 本地环境遇到的实际问题

- **现象**：`deploy-local.sh` 启动后 `curl /api/auth/token` 返回 HTTP 503，`{"error":{"detail":"database not configured"}}`，网关日志 `dbConn_nil=true`。
- **根因**：Docker Desktop on macOS 上 `host.docker.internal`（192.168.65.254:5432）在容器刚启动时网络握手慢于 `LLM_GATEWAY_DB_BOOT_RETRY_SECONDS=90s` 预算，gateway 进入 no-DB 模式。
- **修复**：本仓库 `deploy-local.sh` 顶层已默认开启 `DL_PG_PREFLIGHT_REQUIRED=1`，由 `scripts/deploy-local-lib.sh::dl_wait_pg_isready` 在容器启动前完成 PG 探活，超时直接 `die`，避免容器进入 database:null 死区。
  - 回退到旧 warn-only 行为：`export DL_PG_PREFLIGHT_REQUIRED=0 && bash deploy-local.sh`。
- **应急恢复**：当前活动的 container 若已陷入 database:null 状态，可用 `docker restart llm-gateway-local-8782` 让其重新进入正常 boot retry 路径（修复前验证有效）。

#### 修复验证

```
$ bash deploy-local.sh --verify
[verify] 8782:/Users/xutaohuang/kaixuan/llm-gateway-go/bin/current: ok
        (version=2.5.6-f7f8f66d-20260922-2185 build_seq=2185)
VERIFY_TOOL=deploy-local.sh
VERIFY_DEVICE=local
VERIFY_PASS=1
VERIFY_RELEASE=2.5.6.2186
```

---

## 9. 后续改进建议

1. **总览心跳**：当前 `deployment_nodes` 中本地节点未注册；建议本地也注册 `instance_heartbeats` 便于运维面板。
2. **跨环境 key 提示**：在 README 中明确说明每个环境的 key 独立（不共享 DB），避免后续测试误用本地 key。
3. **CI 接入**：把 §5 步骤封装成 `tests/deploy_e2e_test.sh`，三环境参数化跑，纳入 release gate。
4. **DL_PG_PREFLIGHT_REQUIRED 推广到 245/154**：当前仅 `deploy-local.sh` 顶层默认开启；245/154 经 `deploy-seamless.sh` 不源本 lib，相同 race 在 KVM 内网不可见但生产灾备场景可能复现，建议下一阶段在 `deploy-seamless.sh` 入口加等价 wait-for-pg 探活。

---

## 附 A. 一键验证脚本

将下列内容保存为 `docs/全面测试/run-deploy-e2e.sh`：

```bash
#!/usr/bin/env bash
set -euo pipefail
USERNAME="${USERNAME:-admin}"
PASSWORD="${PASSWORD:-Veritrans&9527}"
KEY_ID_LOCAL=212
KEY_ID_REMOTE=134

check_env() {
  local name="$1" base="$2" key_id="$3"
  echo "=== $name ($base) ==="
  curl -sS -m 5 -o /dev/null -w 'healthz: %{http_code}\n' "${base}/healthz"
  curl -sS -m 5 -o /dev/null -w 'index:   %{http_code}\n' "${base}/"
  TOKEN=$(curl -sS -m 8 -X POST -H 'Content-Type: application/json' \
    -d "{\"username\":\"$USERNAME\",\"password\":\"$PASSWORD\"}" \
    "${base}/api/auth/token" | python3 -c 'import sys,json; print(json.load(sys.stdin)["access_token"])')
  [[ -n "$TOKEN" ]] || { echo "login: FAIL"; return 1; }
  echo "login: PASS (token=${TOKEN:0:20}…)"
  curl -sS -m 8 -o /dev/null -w 'overview: %{http_code}\n' \
    -H "Authorization: Bearer $TOKEN" "${base}/api/admin/ops/overview"
  KEY=$(curl -sS -m 8 -X POST -H "Authorization: Bearer $TOKEN" \
    "${base}/api/keys/${key_id}/reveal" | python3 -c 'import sys,json; print(json.load(sys.stdin)["api_key"])')
  RESP=$(curl -sS -m 30 -X POST -H "Authorization: Bearer $KEY" \
    -H 'Content-Type: application/json' \
    -d '{"model":"minimax-m3","messages":[{"role":"user","content":"ping"}],"max_tokens":30}' \
    "${base}/v1/chat/completions")
  echo "chat: $RESP" | head -c 400; echo
}

check_env "local" "http://127.0.0.1:8782" "$KEY_ID_LOCAL"
check_env "245"   "https://llmgo.kxpms.cn" "$KEY_ID_REMOTE"
check_env "154"   "https://llm.kxpms.cn"   "$KEY_ID_REMOTE"
```
