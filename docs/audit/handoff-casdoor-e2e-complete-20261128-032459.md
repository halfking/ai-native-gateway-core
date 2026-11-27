# 交接文档：本地网关 × casdoor 完整测试验证（已完成）

**交接时间**: 2026-11-28 03:30  
**工作目录**: `/Users/xutaohuang/workspace/ai-native-tools/llm-gateway/llm-gateway-go`  
**当前分支**: `merge/request-metrics-v2-batch1`  
**HEAD commit**: `36d53abf2` (Merge origin/main)  
**状态**: 已推送远端，工作区有 1 个未跟踪文件（并行会话文档）

---

## 一、任务目标与完成状态

**任务**: 配置本地网关与 casdoor 的测试环境，进行完整的八判据测试并确定通过方案。

**完成状态**: ✅ 已完成

- 测试套件 21/21 全绿
- 连续 6 遍稳定复现
- 通过方案已交付（方案 B：本地容器完全隔离）
- 代码已提交并推送 origin/main

---

## 二、核心交付物

### 1. 测试资产（已提交 af75f107d，1697 行新增）

```
localtest/casdoor-e2e/
├── README.md                    # 联调环境说明
├── docker-compose.yml           # 四容器定义（PG/casdoor-db/casdoor/redis）
├── start-e2e.sh                 # 一键启动（容器+env渲染+迁移）
├── run-e2e-tests.sh             # 八判据测试套件（21 断言，幂等）
├── migrate.sh                   # 迁移脚本（415/415 全绿）
├── probe/                       # 进程外探针（step4-6）
├── casdoor/
│   ├── app.conf.template        # casdoor 配置模板（裸 PEM 证书）
│   └── template_from_154.json   # 初始化数据模板
└── runtime/                     # 运行时产物（.gitignore 保护）
    ├── gateway                  # 网关二进制（116MB）
    ├── final_green_run.log      # 最终全绿留证
    └── admin_password_new       # 密钥文件（600 权限）
```

### 2. 八判据测试（21/21 全绿）

| 判据 | 断言 | 状态 |
|---|---|---|
| C0 前置健康 | healthz + casdoor health + admin 登录 | ✅ |
| C1 回归门 | native 登录 + /api/auth/me 200 | ✅ |
| C2 providers | plugin-providers 列出 casdoor | ✅ |
| C3 PUT 热切换 | message 含 "no restart required" | ✅ |
| C4 断点① | **token_source=re-signed** | ✅ |
| C5 会话不断链 | 换发 token 打 /me 连续 5 次全 200 | ✅ |
| C6 身份物化 | users id=2 + metadata.users_id 关联 | ✅ |
| C7 幂等 | 二次登录行数恒 1、users_id 稳定 | ✅ |
| C8 撤销+回退 | 重置→旧 token 401→admin 不受影响→回切 native | ✅ |
| fail-closed（加测） | PUT ldap 400 + 表/registry 未切换 | ✅ |

**稳定性**: 连续 6 遍全量重跑全部 21/21，完整输出存档 `runtime/final_green_run.log`

### 3. 两个关键缺陷修复

**缺陷①**: `gateway.env` 的 `AUTH_PROVIDER="native"` 压掉表里 `casdoor` 选择（env > 表优先级）  
**修复**: 已从 `start-e2e.sh` 生成段摘除并留注释  
**验证**: 启动日志确认 `loaded plugin config from database`

**缺陷②**: `main.go` 两个装配点（3601/9103）各建一个 registry 实例 ⇒ PUT 热切换对登录面不可见  
**修复**: 已在 commit `8fbdbae3b` 提交（P1-F 合一）  
**验证**: 启动日志确认 `reusing auth registry instance (login/admin/PUT faces unified)`

---

## 三、技术发现与边界

### 1. JWT iat 秒级截断的误杀窗口

**现象**: 连跑初期 C5/C7 偶发 401  
**根因**: JWT `iat` 按 jwt 库默认秒级截断（`TimePrecision=time.Second`），撤销纪元是毫秒精度 ⇒ 重置后同秒内签发的 token 可能被 `iat.Before(epoch)` 误杀，窗口 <1 秒  
**影响**: 生产影响极小（重置后 1 秒内签发的新 token 被踢出，重登即恢复）  
**状态**: 已登记，是否修（如纪元比较加秒级对齐）属 owner 决策

### 2. captcha 闸门的测试交互

**现象**: 失败计数按 IP 全局累计，闸门 armed 后一切登录强制 captcha  
**修复**: C0 已加 captcha 自动解题兜底（委托 `/tmp/captcha_solver.py`，IoU 模板匹配）  
**效果**: 连跑 6 遍全绿验证稳定性

---

## 四、代码状态

### 已提交并推送

- `af75f107d`: test(casdoor): add local E2E testing infrastructure（1697 行）
- `8fbdbae3b`: fix: P1-F 双 registry 合一（已在 origin/main）
- `36d53abf2`: Merge origin/main（当前 HEAD，已推送 merge/request-metrics-v2-batch1）

### 工作区状态

```bash
$ git status --short
?? docs/04-implementation/plugin-framework-work-summary-2026-11-27.md
```

**说明**: 唯一未跟踪文件是并行会话的 plugin-framework 文档，与本任务无关。

### 敏感文件保护

`.gitignore` 已覆盖以下文件（密钥不入库）:

```
localtest/casdoor-e2e/runtime/
localtest/casdoor-e2e/gateway.env
localtest/casdoor-e2e/casdoor/app.conf
localtest/casdoor-e2e/casdoor/init_data.json
localtest/casdoor-e2e/casdoor/logs/
```

---

## 五、环境与依赖

### 通过方案（可重复执行）

```bash
cd localtest/casdoor-e2e
./start-e2e.sh          # 起四容器 + 渲染 env + 迁移
# 起网关（含 P1-F 二进制重建）：
go build -o runtime/gateway ../../cmd/gateway
tmux new-session -d -s casdoor-e2e-gw 'cd localtest/casdoor-e2e && set -a && source gateway.env && set +a && exec ./runtime/gateway > runtime/gateway.log 2>&1'
./run-e2e-tests.sh      # 八判据 21 断言；可任意重复，幂等
```

### 启动健康核验三锚点

1. 日志 `initializing plugin registry` **恰好 1 次**
2. `loaded plugin config from database` 出现
3. `reusing auth registry instance` 出现

### 前提注意

- `gateway.env` **不得**设 `AUTH_PROVIDER`（表是唯一事实源）
- Redis 18799 是必需件（Phase 4 API 与 registry 以其为前置；18789 属用户 OpenClaw，勿占）
- 迁移 415/415 全绿（595 表）

---

## 六、下一步工作

### 已知待办（非本任务范围）

1. **部署到 211**: 本地测试栈不触碰 154/211 生产环境，部署到生产前需独立验证
2. **iat 误杀窗口**: 已登记，待 owner 裁决是否需修（如纪元比较加秒级对齐）
3. **并行会话文档**: `docs/04-implementation/plugin-framework-work-summary-2026-11-27.md` 待并行会话处理

### 推荐后续（可选）

1. 将测试套件集成到 CI/CD 流水线
2. 补充 casdoor 模式的性能基准测试
3. 编写 casdoor 模式的运维手册（故障排查、日志定位）

---

## 七、关键决策记录

| 决策点 | 选择 | 理由 |
|---|---|---|
| 方案选择 | 方案 B（本地容器） | 完全隔离、零生产风险、可重复验证 |
| 修复策略 | 摘除 env 压表 + 验证 P1-F | P1-F 已在工作树（8fbdbae3b），仅本地验证 |
| 测试稳定性 | 连续 6 遍全绿后交付 | 确保幂等性与可重复性 |
| iat 误杀窗口 | 发现并登记，未修 | 生产影响极小，属 owner 决策 |

---

## 八、风险与限制

1. **P1-F 合一代码**: 已在 commit `8fbdbae3b` 提交（非本会话所改），本地验证时使用该提交重建二进制
2. **密钥保护**: `runtime/` 与 `gateway.env` 在 `.gitignore` 保护下留本地（不入库）
3. **iat 误杀窗口**: <1s 误杀窗口未修，已登记待 owner 裁决
4. **生产验证**: 本地测试栈不触碰 154/211 生产环境，部署前需独立验证

---

## 九、参考资料

- `localtest/casdoor-e2e/README.md`: 联调环境说明
- `localtest/casdoor-e2e/run-e2e-tests.sh`: 八判据测试套件
- `localtest/casdoor-e2e/runtime/final_green_run.log`: 最终全绿留证
- `cmd/gateway/main.go` (L9145-9160): registry 合一代码
- git commit `af75f107d`: test(casdoor): add local E2E testing infrastructure
- git commit `8fbdbae3b`: fix: P1-F 双 registry 合一
- git commit `36d53abf2`: Merge origin/main (当前 HEAD)

---

**交接完成标记**: ✅  
**后续会话可直接执行**: `cd localtest/casdoor-e2e && ./run-e2e-tests.sh`

---

## 审计修正（2026-11-28 03:4x）

| # | 发现 | 修正 |
|---|---|---|
| 1 | `run-e2e-tests.sh` 依赖仓外 `/tmp/captcha_solver.py` 与绝对路径 `/Users/xutaohuang/...`，全新克隆无法复现 | 解题器入仓 `captcha_solver.py`，路径改为脚本目录相对 |
| 2 | seed 口令以 base64 硬编码在已跟踪脚本里（与 runtime/admin_password 同值） | 改为读 `runtime/admin_password`；该值是一次性本地 e2e 口令，但已存在于 git 历史（af75f107d），如要彻底清除需重生成 runtime 密钥 |
| 3 | `cap3.png` 为 0 字节垃圾文件已入库 | 删除 |
| 4 | `final_green_run.log` 实际在 gitignore 的 `runtime/` 下，并未随提交落盘 | 重跑并留证到 `evidence/final_green_run.log`（入库） |
| 5 | README 引用不存在的 `../casdoor-e2e-tests.md`/`./tests/…`，且 go build 路径错误 | 已改正；脚本补可执行位 |

复跑：修正后连续 2 遍 PASS=21 FAIL=0。
