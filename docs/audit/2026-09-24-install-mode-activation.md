# 审计轮：installer 安装模式选择 + 自动注册激活 + launcher 实例信息展示

- 日期：2026-09-24
- 分支：`feature/audit-installer-r62`（基底 a6f035227，已合并 origin/main 2e52a7ee5）
- 范围：installer 模块（独立 Go module）staged 改动集（并行会话已提交为 aa1160746，21 文件 +2503/−86）+ 本轮修复
- 约束遵守：未在 154/245/252 做任何部署；未跑 PGSCHEMA 迁移；`scripts/apply-db-revision-sequence_test.sh` 仅补登 744（已由并行会话 b31229ab1 收口）

## 一、需求与交付

1. **安装模式选择**：wizard 让用户选存储模式，`lite` = SQLite + 本地文件，`full` = PG17 + Redis，默认 `full`。
2. **自动注册激活**：install 末尾自动向 `https://llm.kxpms.cn` 注册激活，拿到 device_code，状态落 `{installDir}/state/activation.json`，凭据落 `state/instance.token`。
3. **后台展示**：launcher Web UI + `/launcher/api/status` 展示实例的存储模式 / IP / device_code / 激活状态。

### 实现落点

| 需求 | 文件 |
|---|---|
| 1 | `installer/internal/prompt/wizard.go`（步骤 2 存储模式，TTY 默认 full）、`cmd/llm-gw-installer/main.go`（`--mode` 覆盖、`buildLiteComposeYAML` 剥离 PG/Redis、跳过 schema 初始化、`.env` 写 `LLM_GATEWAY_STORAGE_MODE`）、主仓 `cmd/gateway/storage_mode_init.go`（运行时读取，既有） |
| 2 | `installer/internal/activation/auto_activate.go` + `enrollment/`（register 心跳 token 读取双路径）、`cmd/llm-launcher/main.go` |
| 3 | `installer/internal/instancemeta/meta.go`（读 activation.json，三态语义）、`internal/launcher/api/api.go`（status 合并 InstanceMeta）、`launcher/api/web/{index.html,app.js,style.css}`（实例元信息卡片 + 掩码） |

## 二、审计发现（子代理批判复审 + 主代理逐条直读复核）

### P0 —— 新设备自动注册对真实主控 100% 失败（已修）

- **证据**：installer 发送 `LicenseKeyHash: hardwareHash`（原 auto_activate.go:189）；主控 `cmd/license-authority/register_handler.go` 新设备分支把 `license_key_hash` 当**完整 license key** 查库（旧版安装器兼容路径），查不到返回 404 `license not found`。hardwareHash 是 `sha256(hostname|arch|mac)` 截断，不可能是 license key。
- **加重**：register 失败即 `status=failed` 提前 return，**即使设置了 `INSTALL_LICENSE_KEY`，后续激活分支也永远执行不到**。
- **加重**：installer 的 `ActivateOnline` 指向 `POST /api/v1/license/activate`，该端点在主控不存在（admin licensing 路由只有 /licenses CRUD；`licensing/customer_api.go` 的 activate 未被任何路由挂载）。
- **掩盖**：原 mock master 对 register 无条件返回 200，测试全绿但契约全错。
- **修复**（见 §三.1）。

### P1 —— device_code = instance_token JWT，经 /status 接口外泄（已修）

- **证据**：主控 register 响应仅 `instance_token/refresh_token/server_public_key/expires_at`（register_handler.go:167-172），无 device_code 概念；原实现 `st.DeviceCode = regResp.InstanceToken` 把 7 天有效的心跳 JWT（`Authorization: Bearer`，主控 instanceTokenGroup 强校验）当作"设备码"写进 activation.json，并经 `instancemeta.Load` → `/launcher/api/status` **明文**返回（app.js 的 maskDeviceCode 只是显示层掩码，抓包即得全量）。
- **修复**（见 §三.2）。

### P2（已修 3 项 + 测试补口）

| # | 发现 | 证据 | 处置 |
|---|---|---|---|
| P2-1 | `--config` 只认 `STORAGE_MODE`，与写出的 `.env` 键名 `LLM_GATEWAY_STORAGE_MODE` 不一致 → 拿已有 .env 重装时 lite 被静默重置为 full | wizard.go:160 vs main.go envEntries | LoadFromEnvFile 双键名兼容 |
| P2-2 | installer-ci.yml 交叉编译矩阵未注入 GOOS/GOARCH，6 个 job 产出 6 个相同的宿主平台二进制 | workflow build step | step 加 `env: GOOS/GOARCH`；顺带修文件末尾缺换行、activation-tests 扩为全模块 `go test ./...` |
| P2-3 | 注册成功但写 token 失败的分支 + License/Trial 同给优先级无测试 | auto_activate_test.go | 补 `TestRunAutoActivate_TokenWriteFailure_RecordsError` / `TestRunAutoActivate_LicenseKeyWinsOverTrial`（trial 端点故意 500 反证未被调用） |
| P2-4 | `PublicKey: "placeholder-ed25519-base64"` 假公钥过闸并被主控持久化（主控仅校验非空） | register_handler.go:54 | 真实 ed25519 keypair：私钥 seed 落 `state/instance.ed25519`（0600，重复注册公钥稳定），公钥随 register 提交 |

### P3（已修 6 项）

- wizard 步数文案过期（"共 11 步"实为 13 步，含包注释与 README 目录行）——已改 13。
- heartbeat_test.go X-Signature 注释误写 "Ed25519"，实为 HMAC-SHA256(key=SHA256(token))——已改。
- README 三处与实现不符：`.env` 写入路径（`~/.env` → `{installDir}/.env`）、lite 行为表"多三个键"（full 同样写入）、健康检查"N/A"（实现为强制置 true，报告渲染 true）——已改；顺带清除"子代理 B"内部措辞。
- lite compose 输出头部注释残留全栈 PG/Redis 描述——`buildLiteComposeYAML` 现在重写头注释（lite 标题 + 去掉 db/redis 目录行）。
- `state/` 目录 0755 → 0700 收紧（activation.json / instance.token / instance.ed25519 均 0600）。
- `embeddata/env.template` 为死资源：`//go:embed` 进二进制后全仓无引用，`.env` 实际由 `envEntries` 生成，本次更新其占位符纯属制造漂移——已删除 embed、var 与文件。

### 查无问题

- lite 剥离逻辑（对真实 embeddata/compose.yml 实圆验证：citus/redis 服务、depends_on、DATABASE_URL、REDIS_* 全剥离，env_file/healthcheck/ports 保留）；
- state 文件权限（WriteFile 0600 + tmp/rename + 显式 chmod 双保险）与 install-report 模板无 token/secret 泄漏；
- `/launcher/api/*` 统一 X-Launcher-Token 常量时间校验，token 恒非空（24 字节随机 hex 0600 落盘），无匿名读取面；
- `scripts/apply-db-revision-sequence_test.sh` 补登 744 与 `sql/migrations/startup/744_sql_audit_partial_indexes.sql` 一致，实跑 exit 0；
- 四个新测试文件均为真断言（字段级状态机校验、权限位、JSON key 缺席契约、HMAC 参考实现比对、合并优先级、omitempty 缺席检查）。

## 三、修复内容（本轮提交）

1. **auto_activate.go 重写**（P0 + P1 + P2-4 + state 0700）：
   - register 即激活：`license_key_hash` 携带完整 license key（主控兼容路径，查得即 `ActivateDevice` 同调用激活）；同机重装走主控 hardware_hash 已注册分支。
   - 流程重排：skip → 无 key 无 trial 直接 skipped（不发起必然 404 的注册噪音）→ trial 先行换 key → register → token 落盘。
   - 删除对不存在端点 `/api/v1/license/activate` 的调用（online.go 整文件删除）；`activate` 子命令 key 模式同步改走 register=激活，不再写 license.dat（在线模式 DB 权威，license.dat 仅离线路径产物，见 cmd/gateway/main.go:644-669）。
   - `DeriveDeviceCode`：`GW-` + sha256(instance_id) 前 12 hex，稳定、非凭据、可展示；instance_token JWT **只**落 `state/instance.token`（0600），activation.json 不再含该字段（instancemeta 保留旧文件读兼容但不映射进 Meta）。
2. **测试基建重写**：mock master 对齐真实契约（未知 key → 404），P0/P1 修成回归钉桩（`register must carry full license key`、`instance_token MUST NOT be present in activation.json`、device_code 非派生码即红）；补 token 写失败 / 优先级 / trial 4xx / 真公钥落盘 0600 / state 目录 0700 用例。
3. P2-1/2/3/4 与 P3 六项（见 §二表）。

## 四、验证

- `installer` 模块：`go build ./...` / `go vet ./...` / `go test -count=1 ./...` 全绿（activation / enrollment / instancemeta / launcher api / e2e / upgrader 等 13 包）。
- `bash scripts/apply-db-revision-sequence_test.sh` → exit 0。
- 需求裁决：需求 1 **covered**（.env → compose env_file → 容器 `LLM_GATEWAY_STORAGE_MODE` 链路闭合）；需求 2 **covered**（修复后对本仓主控契约成立）；需求 3 **covered**（status 合并 + 卡片 + 掩码，device_code 已是非凭据短码）。

## 五、残留与建议

- `PublicKey` 的下游消费（主控挑战签名）尚未建设：installer 侧已提交真实公钥并保留私钥，待主控后续启用时无需再改装机侧。
- 同机重装且不携带 license key 时仍会 skipped（不尝试 hardware-hash 复注册）：对重装友好性有轻微损失，换取"无 key 必不发请求"的确定性，后续可按需放开。
- 线上 `llm.kxpms.cn` 若部署版本落后于本仓 register_handler（如仍接受 hash-as-key 之外的语义），行为以其部署版为准；本审计按仓内源码契约判定。
- 并行会话注记：aa1160746 在本审计进行中由并行会话提交，比 staged 清单多携带 1 个声明外文件（`docs/design/2026-09-24-jev-task-decision-nodes-plan.md`，174 行，docs/design 惯例内、无危害）；744 补登由并行会话先行提交为 b31229ab1。
