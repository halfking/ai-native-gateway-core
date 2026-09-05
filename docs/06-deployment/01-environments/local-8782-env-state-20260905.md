# 本地环境状态快照：llm-gateway-local-8781/8782（2026-09-05）

> 状态：CURRENT（环境侧复验完成日快照）
> 最后核对：2026-09-05 07:00 (+08)
> 规范环境名：`local`；本页记录 8782 本地蓝绿部署的拓扑、验证方法与遗留事项。
> 关联事故：`docs/audit/2026-09-05-stale-binary-deploy.md`

## 1. 拓扑与依赖

| 组件 | 值 | 备注 |
|---|---|---|
| active 容器 | `llm-gateway-local-8782`，镜像 `kx-llm-gateway-local:<release>` | `127.0.0.1:8782->8782`，restart=unless-stopped |
| candidate 容器 | `llm-gateway-local-8781` | 蓝绿预热口，晋升后保留/互换 |
| 网络 | `shared-infra`（external） | PG/Redis 按容器名直连 |
| PostgreSQL | 容器 `llm-gateway-pg`（kx-citus-pg17），DB `llm_gateway` | 容器网内 `llm-gateway-pg:5432`；宿主机 `127.0.0.1:5432`；数据挂载 `~/kaixuan/postgres`（共享目录，勿重建） |
| Redis | 容器 `nbjl-redis`（7.4.x） | `nbjl-redis:6379`，`LLM_GATEWAY_REDIS_DB=5` |
| 安装根 | `~/kaixuan/llm-gateway-go` | `bin/<release>` 不可变束 + `bin/current` 符号链接；`run/{active-version,active-port,gateway.build,llm-gateway-local-878{1,2}.env}` |
| 状态目录挂载 | attachments / logs / raw-logs / backups → `~/kaixuan/llm-gateway-go/<同名>` | `cbf6de062` 起生效（此前只写容器可写层） |

源码 checkout（PROJECT_ROOT）在本仓库，与安装根分离；部署入口
`bash scripts/deploy-local.sh deploy`（先 `--dry-run` 预检）。

## 2. 部署与验证合同（本环境特有要点）

1. **构建**：脚本固定 `CGO_ENABLED=0 GOOS=linux go build ./cmd/gateway`。依赖图含
   cgo-only 包即断（已修，见 `storage/sqlite/driver_{cgo,nocgo}.go`）。
2. **身份核验（必做）**：晋升后
   `go version -m ~/kaixuan/llm-gateway-go/bin/<release>/gateway | grep vcs.revision`
   必须与该 release 的 `version.json` git_sha 一致；且 `run/gateway.build` mtime 应为
   本次部署时间。版本号来自 version.json，**不能单独作为代码身份证据**。
3. **健康三件**：`/healthz`、`/readyz`（含 DB/Redis 连通）、`/version`。
4. **migrate**：宿主机直跑 `gateway migrate` 需把连接串 `host.docker.internal` 改为
   `127.0.0.1`；容器内/部署流程内执行则原样。

## 3. 关键代码事实（验证时易踩坑）

- **请求日志写侧是热表**：业务请求 INSERT 到 `request_logs_hot`（与
  `usage_ledger_hot` 同事务）。`SELECT ... FROM request_logs`（父表/月分区）看不到
  新行**不是故障**；验证落库请查 `request_logs_hot`。
- **admin API 本地鉴权**：容器 env 的 `LLM_GATEWAY_ADMIN_USER`/`LLM_GATEWAY_ADMIN_PASSWORD`
  当前为空值，`POST /api/auth/token` 登录不可用。替代路径（验证用，非入侵）：
  以容器 env `LLM_GATEWAY_SECRET_KEY` 按 `admin/jwt.go` 的 JWTClaims
  （HS256，`iss=llm-gateway`，`aud=llm-gateway-api`，claims：user_id/tenant_id/username/role）
  本地签发 JWT，`Authorization: Bearer` 调 `/api/*`。本地 super_admin 为
  users 表 id=41 `admin`（tenant `default`）。
- **数据面 key**：本地静态 `LLM_GATEWAY_API_KEY` 不被数据面接受（实测 missing_key）；
  用 admin API `GET /api/keys/<id>/reveal` 取真实 key 调 `/v1/chat/completions`。
  模型可用性：`gpt-5.6-terra` 实测可用；`gpt-4o-mini`/`minimax-m2.7` 无健康凭据（503）。
- **快照端点形态**：`GET /api/admin/sessions/<id>/snapshot` 对 `public.sessions`
  不存在的会话返回设计内 2 字段回退（HTTP 200），不是错误。全库 31871 会话的
  `task_type`/`user_tags` 等新列均无数据，当前最大可观测形态为 14~15 字段；
  23 字段结构由 `TestSessionSnapshotV2FieldCount` 锁定，加字段需同步 wantFields。

## 4. 当前运行版本（快照时点）

> 2026-09-05 16:05 (+08) 本地收敛重部署更新（B1，主代理会话）。

| 项 | 值 |
|---|---|
| 8782 运行版本 | **2.5.0.1943**（vcs.revision=`c087914eb` = 当时 origin/main tip，精确构建无漂移） |
| 8781 | 同 1943（当前代次候选） |
| 身份核验 | bundle `go version -m` vcs.revision=`c087914eb…` == bundle version.json git_sha=`c087914e`；`run/gateway.build` mtime=2026-09-05 16:05（本次部署时间） |
| 特性复验证据 | 2.5.0.1943 上端到端三连全绿（2026-09-05 16:10）：`GET /api/logs/<request_id>` 200（65 字段、outbound_body 完整）、`?omit_body=1` 200（body 字段正确省略）、`GET /api/admin/sessions/<gw_session_id>/snapshot` 200（14 字段设计内形态）；健康三件 200/200/`2.5.0-c087914e` active。注：详情端点路径参数是 `request_id` 字符串，非热表数字 id |
| 历史 | 此前 2.5.0.1935（vcs.revision=`b08ea0607`）端到端三连证据见 `docs/FEATURE-REQ-session-snapshot-and-request-detail.md` §4；1938/1942 为并行会话部署的多 clone 中间态，已被本次收敛覆盖 |

## 5. 遗留事项（下一阶段执行输入）

1. **本地收敛**（已完成，2026-09-05 16:05）：8782 已从同步 origin/main 的本仓库重部署为
   2.5.0.1943（vcs.revision=`c087914eb`），身份核验 + 端到端三连全绿，见 §4。
   注意：首次部署尝试因 PG 瞬时 `can't assign requested address`（EADDRNOTAVAIL）在
   migrate 步 fail-closed 退出、未晋升（合同正确行为），PG 恢复后重试成功。
2. **同源漏洞排查**（已完成，2026-09-05）：`deploy-245.sh`/`deploy-154.sh`/`deploy-seamless.sh`/
   `deploy-local-lib.sh` 的吞错模式与 CGO=0 断裂排查已完成修复（含远端旧产物复用
   窗口），落地产物见事故文档 `docs/audit/2026-09-05-stale-binary-deploy.md` §5
   与提交说明。
3. **回归测试固化**（已完成，2026-09-05）：`CGO_ENABLED=0 go build ./cmd/gateway` 已进
   deploy 测试组合：`tests/deploy_nocgo_build_test.sh`（正向构建 + 反向回归 +
   `go list -deps` 静态检查）。
4. **晋升 245 预发**（未完成，待执行）：特性已在 local `LOCAL_VERIFIED`；245 需 env-injector 注入
   `aliyun-frontend-245` 凭据 + `scripts/deploy-245.sh --dry-run` + 步骤 9.2 凭据解密门。
   154 生产不自动晋升，需人工放行。
5. **多 clone 纪律**（未完成，流程约定）：`llm-gateway-go-2` 等 clone HEAD 落后；发布构建一律在已
   `pull --ff-only` 同步的 clone 进行。
