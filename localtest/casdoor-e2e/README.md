# casdoor-e2e 本地联调（方案 B：全本地隔离）

本地起一套完整的「网关 + casdoor」联调环境，验证 2026-11-27 L135 轮的 casdoor 模式闭环（登录换发 + 身份物化 + 双 registry 合一 + PUT 热切换）。不触碰 154 在役 casdoor（auth.kxpms.cn）与 211 PG。

## 快速开始

```bash
cd localtest/casdoor-e2e
./start-e2e.sh                 # 生成密钥/配置 + 起栈（PG×2 + casdoor）
./migrate.sh                   # 重建 gateway_e2e 库 + 基线三件套 + 全量 startup 迁移
set -a; source gateway.env; set +a
go build -o runtime/gateway ../../cmd/gateway
tmux new-session -d -s casdoor-e2e-gw 'cd '"$PWD"' && set -a && source gateway.env && set +a && exec ./runtime/gateway > runtime/gateway.log 2>&1'
curl -s http://127.0.0.1:18781/healthz
bash ./run-e2e-tests.sh        # 八判据 21 断言，幂等，期望 PASS=21 FAIL=0
```

测试脚本 `run-e2e-tests.sh`（判据 C1–C8 写在文件头注释）；其 captcha 自动解题依赖同目录 `captcha_solver.py`（读 `admin/auth_captcha.go` 的字形表）。留证见 `evidence/final_green_run.txt`。

## 拓扑

| 组件 | 地址 | 说明 |
|---|---|---|
| 网关 PG17 | 127.0.0.1:15433 / gateway_e2e | 网关数据（users/auth_id_mapping/plugin_config 等） |
| casdoor PG17 | 内网 pg:5432 / casdoor | casdoor 自己的元数据（不入宿主端口） |
| casdoor | http://127.0.0.1:18000 | 本地 casdoor 容器（casbin/casdoor:latest） |
| 网关 | http://127.0.0.1:18781 | 本地编译二进制，env 来自 gateway.env |

## 凭据（启动时生成，gitignore 保护）

全部在 `runtime/`（0600）与渲染产物 `casdoor/app.conf`、`gateway.env` 里：

- casdoor 用户：org=kaixuan-e2e / 用户名 gwuser / 密码 runtime/casdoor_user_password
- 网关 native 种子 admin：admin / LLM_GATEWAY_SEED_ADMIN_PASSWORD（gateway.env）
- casdoor OAuth：clientId=e2e-client-id / secret=runtime/casdoor_client_secret
- JWT 链：LLM_GATEWAY_JWT_SECRET（未设）→ LLM_GATEWAY_SECRET_KEY=gateway.env 里的值（native 签发与 casdoor 换发同链）

## 目录

```
casdoor-e2e/
├── start-e2e.sh          # 密钥/配置生成 + compose up
├── migrate.sh            # gateway_e2e 库重建 + 全量迁移（r112 范式裁剪）
├── run-e2e-tests.sh      # 八判据 21 断言测试套件
├── captcha_solver.py     # 测试用 captcha 解题器（依赖 admin/auth_captcha.go 字形表）
├── evidence/             # 全绿运行留证
├── docker-compose.yml    # pg + casdoor-db + casdoor
├── gateway.env           # 生成物（网关 source），0600，gitignore
├── casdoor/
│   ├── app.conf.template # @PG_PASSWORD@ 占位，渲染成 app.conf
│   ├── app.conf          # 生成物，0600，gitignore
│   ├── init_data.json    # 生成物（含内嵌证书块），gitignore
│   ├── template_from_154.json  # 154 在役 init_data 的脱敏结构模板（只读参考）
│   └── logs/
└── runtime/              # 密钥与网关二进制/日志，gitignore
```

## 排障速查

| 症状 | 首查项 | 修正 |
|---|---|---|
| casdoor 容器重启循环 | `docker logs casdoor-e2e` | 看 app.conf 渲染/DB 连通；常见是 init_data.json 证书块格式错 |
| password grant 401 | `SELECT enable_password, grant_types FROM application WHERE name='llm-gateway-e2e'`（casdoor-db 容器内） | application 的 grantTypes 必须含 "password"；enablePassword=true |
| 登录换发后 401 | 网关日志里 casdoor provider 注册行 | CASDOOR_CERTIFICATE 必须是 PEM（与 init_data 里证书块的 publicKey 同钥） |
| 网关起不来 | runtime/gateway.log | DATABASE_URL 端口 15433；迁移是否跑过 |

## 已知窗口与已接受风险（owner 裁决留痕，2026-11-28）

> 以下两条由自动超时选定的保守答案登记，owner 随时可改。

1. **iat 误杀窗口（不修）**：JWT `iat` 按秒截断，改密撤销纪元（`admin/auth_revocation.go` 的 `settings_kv` 时间戳）是毫秒精度；改密后**同一秒内**重新签发的新 token 会被判为已撤销（偏保守，宁可误杀不漏杀）。若要修：比较时把纪元截断到秒（代价：改密同秒内签发的旧 token 多存活 <1s），并在 `run-e2e-tests.sh` 增判据。
2. **历史口令留痕（不轮换）**：提交 `af75f107d` 含 base64 编码的一次性本地 e2e 口令，仅本机栈使用、不触生产；现行脚本已改读 `runtime/admin_password`。未重写 git 历史。若要轮换：重跑 `start-e2e.sh` 重新生成 `runtime/` 并回归 `bash ./run-e2e-tests.sh`（期望 PASS=21 FAIL=0）。
