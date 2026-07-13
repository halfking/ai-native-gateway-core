# 无缝部署指南 (245 / 154 / Docker / k3s)

> 2026-07-14 · 基于 `scripts/deploy-seamless.sh` 实测固化
> 适用版本：build_seq ≥ 1010（154）/ ≥ 1009（245）

## TL;DR

| 场景 | 方案 | 停机窗口 | 回滚 | 命令 |
|---|---|---|---|---|
| **245/154 (裸 systemd)** | 原子符号链接切换 | ~4-8s (单次 restart) | 一条命令切回任意 verified 版本 | `deploy-seamless.sh deploy/rollback <target>` |
| **本地 Docker (compose)** | recreate (`up -d`) | ~0s (新容器先起) | `up -d` 旧 image tag | `docker compose up -d gateway` |
| **252/k3s 生产** | rolling update | **0s** (maxUnavailable:0 + readiness) | `kubectl rollout undo` | `kubectl set image ...` |
| **紧急回退 (systemd)** | 切 legacy | ~4s | — | `deploy-seamless.sh rollback 154` |

---

## 1. systemd 原子符号链接方案 (245 / 154)

### 1.1 布局

```
/opt/llm-gateway-go/
├── releases/
│   ├── 1010-995e6c4c/          # 每次部署一个自洽 bundle
│   │   ├── llm-gateway-go       #   (154) 或 gateway (245) — 可执行二进制
│   │   ├── web/                 #   展平的前端静态资源
│   │   ├── version.json         #   版本 SSOT
│   │   ├── SHA256SUMS           #   校验清单
│   │   └── deployment.json      #   {"verified":true,"verified_at":"..."}
│   ├── legacy-20260714-043401/  # adopt 时保留的旧版本 (verified=true)
│   └── ...
├── current → releases/1010-995e6c4c   # 原子切换点 (ln -sfn)
├── llm-gateway-go → current/llm-gateway-go   # systemd ExecStart 看到的路径
├── web → current/web
└── version.json → current/version.json
```

**核心思想**：所有 kernel 可见路径都是符号链接，指向 `current/`，`current` 再指向某个 `releases/<version>/`。切换 = `ln -sfn`（POSIX 原子 rename(2)），文件状态永不破损。

### 1.2 部署流程 (9 步)

```bash
# 245 (预发布)
bash scripts/deploy-seamless.sh deploy 245 --seq 1004

# 154 (生产，245 验证通过后)
bash scripts/deploy-seamless.sh deploy 154 --seq 1005
```

脚本内部流程：
1. **bump-version** — `version.json` build_seq +1（或 `--seq N` 强制，必须 > 当前）
2. **前端构建** — `cd web && npm run build` → `web/dist/`
3. **Go 交叉编译** — `CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build`
4. **stage bundle** — 打包到本地 `/tmp/seamless-release-<target>-<seq>/`（binary + web + version.json + SHA256SUMS + deployment.json）
5. **upload** — tar 管道 scp 到目标 `releases/<version>/`（单 ssh 连接）+ chown root
6. **verify** — 远端 `sha256sum -c SHA256SUMS --strict`
7. **adopt 检测** — 若无 `current` 符号链接（首次），把现有扁平二进制纳入 `releases/legacy-<ts>/`（verified=true），建符号链接
8. **atomic switch** — `ln -sfn releases/<version> current` + 重建 4 个符号链接 + `systemctl restart`
9. **wait healthy** — curl `/healthz` 循环（60s 超时）。**通过 → mark verified；失败 → 自动 rollback 到上一个 verified 版本**

### 1.3 回滚（一条命令）

```bash
bash scripts/deploy-seamless.sh rollback 245
bash scripts/deploy-seamless.sh rollback 154
```

自动选择最近一个 `verified=true` 且 ≠ 当前 active 的版本，原子切换 + restart。

**实测数据（2026-07-14 245）**：
- rollback 耗时：**4 秒**（符号链接切换 + restart + healthz 通过）
- 切回新版：56-78 秒（含编译 + 上传；纯切换部分 <10s）

### 1.4 查看状态

```bash
bash scripts/deploy-seamless.sh status 245
```
输出当前 `current` 指向、所有 releases 的 verified 状态、符号链接链。

### 1.5 adopt（首次迁移）

245/154 原先是**扁平布局**（`gateway` 实文件 + `gateway.bak.*`）。首次无缝部署时，`do_adopt` 自动执行：
1. 把当前运行的二进制 + web 复制到 `releases/legacy-<ts>/`
2. 写 `deployment.json`（verified=true）— 作为回滚兜底
3. 备份原扁平文件为 `*.pre-adopt-<ts>`（保留为终极 fallback，不删除）
4. 建 `current` + 4 个符号链接

adopt 是**幂等**的：只要 `current` 符号链接已存在，后续部署跳过 adopt。

---

## 2. Docker 无缝切换方案

### 2.1 docker compose recreate（本地/单机）

```bash
docker compose -f docker-compose.local-r112.yml up -d gateway
```

**行为**：Compose v5+ 先创建新容器（用新 image），健康检查通过后才停旧容器。

**实测（2026-07-14 r112）**：
- `up -d`（image 无变化）：0s 停机（no-op）
- `up -d --force-recreate`：**0s 停机**（新容器先起，旧容器重叠服务）
- 总耗时：~42s（含构建）

**限制**：
- 停机取决于 healthcheck 时机——新容器必须通过 `wget /healthz` 才被认为就绪
- `docker-compose.persistent.yml` 的 healthcheck 指向 `/health`（旧路径），当前 binary 注册的是 `/healthz`——需修正

### 2.2 docker compose scale（蓝绿，受限）

```bash
docker compose up -d --scale gateway=2 gateway
```

**实测**：**失败**。`ports: "8781:8781"` 映射导致第二个容器无法绑定同一端口。

**正确做法**（如需蓝绿）：
1. 移除 `ports:` 映射，改用内部网络
2. 前置 nginx/traefik 做探活切流
3. 或用 `docker compose --compatibility` + `deploy.replicas`（需 Swarm 模式）

**结论**：单机 Docker 蓝绿不如直接用 k3s（见 §3）。

### 2.3 Docker image 增量更新

| 场景 | 用哪个 Dockerfile | 说明 |
|---|---|---|
| 全量构建 | `Dockerfile` | multi-stage：go build + npm build → runtime image |
| 仅二进制 | `Dockerfile.incremental` | `FROM kx-llm-gateway-go:latest` + cp 新 binary |
| 仅前端 | `Dockerfile.web-patch` | `FROM <base>` + cp `web/dist` |

```bash
# 增量更新二进制（快）
docker build -f Dockerfile.incremental -t kx-llm-gateway-go:patched .
docker compose -f docker-compose.local-r112.yml up -d gateway  # recreate
```

---

## 3. k3s 生产零停机 (252)

252 的 gateway 以 k3s Deployment 运行，**真正零停机**：

```yaml
# deploy/k8s/llm-gateway-go-deployment.yaml
spec:
  strategy:
    type: RollingUpdate
    rollingUpdate:
      maxSurge: 1          # 滚动时先起 1 个新 pod
      maxUnavailable: 0    # 不允许减少可用 pod（零停机保证）
  containers:
    - readinessProbe:       # 新 pod 必须通过才接流量
        httpGet: { path: /healthz, port: 8781 }
```

```bash
# 部署
docker build -t registry.itestu.cn/kx-llm-gateway-go:latest .
docker push registry.itestu.cn/kx-llm-gateway-go:latest
kubectl set image deployment/llm-gateway-go-deployment \
  llm-gateway-go=registry.itestu.cn/kx-llm-gateway-go:latest
kubectl rollout status deployment/llm-gateway-go-deployment

# 回滚
kubectl rollout undo deployment/llm-gateway-go-deployment
```

---

## 4. 决策矩阵

| 你的场景 | 推荐方案 | 理由 |
|---|---|---|
| 245/154 单机 systemd | **deploy-seamless.sh** | 原子符号链接，4s 回滚，无需引入容器 |
| 本地开发 | docker compose recreate | 开箱即用，0s 停机（v5+） |
| 需要真正零停机 + 弹性 | **k3s rolling update** | maxUnavailable:0 + readiness probe |
| 紧急回退 (systemd) | `rollback` 一条命令 | 切回 legacy，4s |
| 紧急回退 (k3s) | `rollout undo` | 切回上一个 ReplicaSet |

---

## 5. 故障 Runbook

### 5.1 healthz 失败（部署后服务不健康）

**自动处理**：`deploy-seamless.sh` 在 healthz 超时后自动 rollback 到上一个 verified 版本。

**手动处理**（自动回滚也失败时）：
```bash
# 查看可用回滚版本
bash scripts/deploy-seamless.sh status 154

# 手动回滚
bash scripts/deploy-seamless.sh rollback 154
```

### 5.2 符号链接断裂（current 指向不存在的目录）

```bash
# 检查
ssh root@154 'readlink /opt/llm-gateway-go/current; ls -la /opt/llm-gateway-go/current/'

# 修复：指向一个存在的 verified 版本
ssh root@154 'cd /opt/llm-gateway-go && ln -sfn releases/<version> current && systemctl restart llm-gateway-go.service'
```

### 5.3 adopt 失败（首次迁移卡住）

症状：`current` 符号链接不存在，但扁平二进制仍在。

```bash
# 检查扁平文件是否还在
ssh root@245 'ls -la /opt/llm-gateway-go/gateway* /opt/llm-gateway-go/web/index.html'

# 用老脚本兜底部署（不依赖 releases/ 布局）
bash scripts/deploy-245.sh    # 或 deploy-154.sh
```

### 5.4 SSH 超时（154 公网抖动）

154 是公网 IP，偶发连接超时。脚本已设置 `ConnectTimeout=20 + ServerAliveInterval=5`。

若持续超时：
```bash
# 用密码模式重试
SSH_KEY_FILE="" SSHPASS='Kaixuan2026&#*9527' bash scripts/deploy-seamless.sh status 154
```

### 5.5 回到老脚本（终极 fallback）

`deploy-seamless.sh` 与 `deploy-245.sh`/`deploy-154.sh` **并存**。若无缝方案出问题，随时切回老脚本（stop→mv→scp→start）。老脚本的扁平布局与无缝方案的符号链接布局互不干扰（符号链接指向的 releases/ 目录是独立的）。

---

## 6. 验证清单（每次部署后必跑）

```bash
TARGET=154  # 或 245
sshpass -e ssh -p 25022 root@<ip> '
echo "[1] service: $(systemctl is-active llm-gateway-go.service)"
echo "[2] version: $(curl -fsS http://localhost:8781/api/system/version)"
echo "[3] postgres disabled: $(journalctl -u llm-gateway-go --since "3min ago" --no-pager -o cat | grep -c "postgres disabled")"
echo "[4] background-tasks: $(curl -s -o /dev/null -w "%{http_code}" http://localhost:8781/api/system/background-tasks)"
TOKEN=$(curl -fsS -X POST http://localhost:8781/api/auth/token -H "Content-Type: application/json" -d "{\"username\":\"admin\",\"password\":\"Veritrans&9527\"}" | python3 -c "import json,sys;print(json.load(sys.stdin).get(\"access_token\",\"\"))")
echo "[5] admin login: $([ -n "$TOKEN" ] && echo OK || echo FAIL)"
'
```

**通过标准**：
- [1] `active`
- [2] `build_seq` > 上次部署值
- [3] `0`（无 postgres disabled）
- [4] `401`（非 503）
- [5] `OK`

---

## 7. 参考文件

| 文件 | 作用 |
|---|---|
| `scripts/deploy-seamless.sh` | **本指南的主角** — 无缝部署/回滚/状态 |
| `scripts/deploy-lib/host.sh` | 共享原子切换库（stage/verify/switch/wait/prune） |
| `scripts/deploy-lib/targets.sh` | 目标契约（service_name/binary_path/health_url） |
| `scripts/bump-version.sh` | 版本号 SSOT 管理（version.json + 3 镜像） |
| `scripts/deploy-245.sh` / `deploy-154.sh` | 老脚本（fallback） |
| `deploy/k8s/llm-gateway-go-deployment.yaml` | k3s 零停机 Deployment 配置 |
| `docker-compose.local-r112.yml` | 本地 Docker 开发栈 |
