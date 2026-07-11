# ADR-001: 使用 credentials.plan_type 作为计费模式的单一真相源

**状态：** 已接受  
**日期：** 2026-07-03  
**决策者：** AI 运维团队  
**相关文档：** [计费模式标准化方案](./billing-mode-standardization.md)

---

## 背景

在 LLM Gateway 系统中，存在两个计费相关的字段：
- `credentials.plan_type` - 凭据级别的套餐类型
- `credential_model_bindings.billing_mode` - 模型绑定级别的计费模式

这导致了以下问题：
1. **数据不一致**：两个字段可能存储不同的值（例如 token vs per_token）
2. **维护复杂**：需要同步更新两个地方
3. **路由错误**：不一致导致凭据被错误排除在路由候选之外
4. **故障频发**：2026-07-03 发生了因计费模式不匹配导致的全局故障

## 决策

我们决定：
1. **`credentials.plan_type` 作为 SSOT（Single Source of Truth）**
2. **`credential_model_bindings.billing_mode` 作为派生字段**，从 plan_type 自动派生
3. **统一命名规范**：
   - `token` → `per_token`（标准化别名）
   - 其他值保持一致（token_plan, code_plan, agent_plan, monthly, free）

### 派生规则

```go
func DeriveBillingMode(planType string) string {
    if planType == "" || planType == "token" {
        return "per_token"  // 标准化名称
    }
    return planType  // 直通
}
```

## 理由

### 为什么选择 plan_type 作为 SSOT？

1. **语义清晰**：plan_type 描述的是凭据的本质属性（套餐类型）
2. **粒度合适**：凭据级别的属性，不应该在每个模型绑定上重复存储
3. **易于管理**：只需在凭据创建时设置一次
4. **业务对齐**：与财务和采购部门的套餐分类一致

### 为什么保留 billing_mode？

虽然是派生字段，但保留它有以下好处：
1. **查询性能**：避免每次路由查询都 JOIN credentials 表
2. **向后兼容**：现有视图和查询可以继续使用
3. **审计追踪**：通过 `plan_type_origin` 标记数据来源
4. **渐进迁移**：允许逐步废弃，不需要立即修改所有代码

## 后果

### 正面影响
- ✅ 数据一致性得到保证
- ✅ 减少维护负担
- ✅ 路由逻辑更可靠
- ✅ 故障排查更简单

### 负面影响
- ⚠️ 需要修改现有的模型发现逻辑
- ⚠️ 需要运行数据修正脚本
- ⚠️ 团队需要理解新的数据模型

### 风险缓解
1. **数据修正脚本**：已创建并测试（见 `docs/billing-mode-standardization.md`）
2. **监控告警**：添加数据一致性检查（见 `docs/monitoring-alerts.md`）
3. **文档完善**：提供详细的故障排查手册（见 `docs/troubleshooting-guide.md`）

## 实施

### 已完成
- [x] 添加 `credentials.plan_type` 列（迁移 327）
- [x] 添加 `credential_model_bindings.plan_type_origin` 列
- [x] 修正所有不一致的数据（1036 条记录）
- [x] 更新模型发现逻辑（`modelcatalog/upsert.go`）
- [x] 创建监控告警规则

### 待完成（长期）
- [ ] 废弃 `billing_mode` 列（6 个月后）
- [ ] 迁移所有查询使用 `plan_type`
- [ ] 简化视图定义

## 替代方案

### 方案 A：使用 billing_mode 作为 SSOT（已拒绝）
- **理由**：billing_mode 是技术细节，plan_type 是业务概念
- **缺点**：与财务系统不对齐，语义不清晰

### 方案 B：完全删除 billing_mode（已拒绝）
- **理由**：短期内改动太大，风险高
- **缺点**：需要修改大量查询和视图，影响范围广

### 方案 C：引入新的计费配置表（已拒绝）
- **理由**：过度设计，当前数据量不需要
- **缺点**：增加系统复杂度

## 参考资料

- [计费模式标准化方案](./billing-mode-standardization.md)
- [故障报告 2026-07-03](./incident-report-20260703.md)
- [迁移文件 327](../migrations/327_credential_plan_type_full.sql)
- [模型发现代码](../modelcatalog/upsert.go)

---

## 更新历史

| 日期 | 版本 | 变更 | 作者 |
|------|------|------|------|
| 2026-07-03 | 1.0 | 初始版本 | AI 运维团队 |

---

# ADR-002: 计费模式命名标准化

**状态：** 已接受  
**日期：** 2026-07-03  
**决策者：** AI 运维团队  
**依赖：** ADR-001

---

## 背景

系统中存在多种计费模式的名称变体：
- `token` vs `per_token`
- `token_plan` vs `tokenplan`
- 不同代码模块使用不同的名称

## 决策

统一使用以下标准名称：

| 标准名称 | 含义 | 别名（废弃） |
|---------|------|-------------|
| `per_token` | 按 token 计费 | token |
| `token_plan` | 令牌包套餐 | tokenplan |
| `code_plan` | 代码包套餐 | codeplan |
| `agent_plan` | Agent 包套餐 | agentplan |
| `monthly` | 月付费 | - |
| `free` | 免费池 | - |

### 命名规则
1. 使用 snake_case（下划线分隔）
2. 避免缩写（除非是行业标准，如 token）
3. 描述性优先（per_token 比 token 更清晰）

## 理由

1. **一致性**：减少混淆，提高代码可读性
2. **可维护性**：统一命名方便搜索和替换
3. **可扩展性**：为未来新增计费模式预留清晰的命名空间

## 实施

- [x] 数据库标准化：所有 `token` 别名已统一为 `per_token`
- [x] 代码标准化：`DeriveBillingMode` 函数已更新
- [ ] 文档标准化：更新所有文档使用标准名称

---

# ADR-003: 凭据类型自动检测与验证

**状态：** 提议中  
**日期：** 2026-07-03  
**决策者：** 待定  

---

## 背景

2026-07-03 故障中发现，credential_id=11 的 plan_type 配置错误：
- 配置为：`token`
- 实际为：`code_plan`（火山方舟代码计划凭据）

上游 API 返回错误："不支持代码计划功能"，但系统无法自动检测和修正。

## 问题

1. 凭据类型依赖人工配置，容易出错
2. 配置错误只在运行时被发现，影响用户
3. 没有自动验证机制

## 提议

实现凭据类型自动检测和验证机制：

### 方案 1：探测端点（Recommended）
```go
// 在凭据创建/更新时自动探测
func DetectPlanType(credential Credential) (string, error) {
    // 1. 调用上游 /v1/models 端点
    models, err := fetchModels(credential)
    if err != nil {
        return "", err
    }
    
    // 2. 发送测试请求
    testReq := createTestRequest()
    resp, err := sendRequest(credential, testReq)
    
    // 3. 根据响应判断类型
    if strings.Contains(resp.Error, "coding plan") {
        return "code_plan", nil
    } else if strings.Contains(resp.Error, "token plan") {
        return "token_plan", nil
    }
    
    return "token", nil  // 默认为按量付费
}
```

### 方案 2：错误学习
```go
// 根据运行时错误自动修正
func LearnFromError(credential Credential, error APIError) {
    if strings.Contains(error.Message, "does not support the coding plan") {
        // 自动标记凭据为 code_plan
        updateCredentialPlanType(credential.ID, "code_plan")
        logAudit("auto_corrected_plan_type", credential.ID, "code_plan")
    }
}
```

## 优缺点

### 方案 1：探测端点
- ✅ 在凭据创建时就能发现问题
- ✅ 避免影响用户请求
- ❌ 增加凭据创建时间
- ❌ 需要消耗 API 配额

### 方案 2：错误学习
- ✅ 零额外成本
- ✅ 实现简单
- ❌ 第一批用户请求会失败
- ❌ 依赖错误信息格式

## 决策

**待定** - 需要进一步讨论权衡

## 后续步骤

1. 团队讨论优先级
2. 设计详细的实现方案
3. 在测试环境验证
4. 编写测试用例

---

# ADR-004: llm.kxpms.cn — NPS 接管 + certbot 自动续期

**状态：** 已接受
**日期：** 2026-07-12
**决策者：** AI 运维团队
**相关代码：** `deploy-llm-kxpms-cert.sh`、`scripts/sync-to-154.sh`、`scripts/sync-llm-kxpms-cert.sh`

---

## 背景

2026-07-11 收到运营任务：把 `llm.kxpms.cn` 的域名 + 证书从 154 (老网关, 47.97.111.154) 接管到 252 (NPS 服务器, 115.29.212.252)，
通过内网把请求转回 154 后端的 llm-gateway-go:8781 (172.16.2.209:8781)。

约束：
1. 必须使用 154 已有的 LE 证书（含 llm.kxpms.cn SAN），不重复申请
2. 154 必须仍能 serve，便于 DNS 切回（兜底）
3. NPS "协助更新 SSL 证书" — 续期必须自动化，避免 49 天后 cert 过期

## 当前拓扑

```text
                    DNS @223.5.5.5
                           │
              llm.kxpms.cn ──────────┐
                                      ▼
                  ┌───────────────────────────────────┐
                  │ Aliyun 252 (115.29.212.252)      │
                  │ nps + nginx (kxpms-on-252.conf)  │
                  └────────────┬──────────────────────┘
                               │ HTTPS terminate
                               │ (cert: /etc/letsencrypt/live/kxpms.cn/fullchain.pem)
                               │ 252 nginx :9443 server { server_name llm.kxpms.cn; }
                               ▼
                  upstream kxpms_llm_backend → http://172.16.2.209:8781
                               │
                               ▼
                  ┌───────────────────────────────────┐
                  │ Aliyun 154 (47.97.111.154)        │
                  │ nginx 1.26.1 + llm-gateway-go    │
                  │ :443 llm.kxpms.cn (its own path) │
                  │ :8781 llm-gateway-go native       │
                  └───────────────────────────────────┘
                               │
                               ▼
                       LLM providers (anthropic / openai / etc)
```

## 续期链路（核心）

```text
certbot-renew.timer (systemd, ~12h cadence)
  └─> certbot renew --noninteractive
       ├─ 检测 /etc/letsencrypt/renewal/*.conf 中 < 30d 过期的证书
       ├─ 对 kxpms.cn 触发 webroot ACME challenge
       │  • nginx 252 :80 explicit block 服务 /.well-known/acme-challenge/ 从
       │    /var/www/certbot 本地 webroot（proven pattern, 与 *.itestu.cn 同款）
       │  • LE 验证 HTTP-01 成功
       ├─ 新 cert 写入 /etc/letsencrypt/live/kxpms.cn/fullchain.pem
       └─ $DEPLOY_HOOK: /etc/letsencrypt/renewal-hooks/deploy/sync-to-154.sh
            ├─ scp cert + key 到 154 (/root/.ssh/cert-sync-252-154 ed25519)
            ├─ ssh 154 nginx -s reload (154 仍能 serve 兜底流量)
            └─ 252 nginx -s reload (主入口拿到新 cert)
```

## 决策

### D1: certbot 直接在 252（不在 154）

- **252 是主入口**（DNS 已 flip → 252），LE challenge 必然命中 252。
- 252 已有 certbot 1.22.0 + `/var/www/certbot` webroot + 多套 *.itestu.cn 证书工作流。
- 154 不再负责 kxpms.cn 续期，仅作 fallback（它的 cert 通过 deploy hook 自动同步）。
- 这样 LE 挑战无需跨服务器 proxy，避免 154 nginx `server-level return 301` 抢 ACME 路径的坑。

### D2: nginx 配置：单 server block 复用 9443

- 252 已配置 stream.d SNI 路由（9443 端口），由 nginx kxpms-on-252.conf 终止 TLS。
- llm.kxpms.cn server block 的 `ssl_certificate` 指向 **certbot 标准路径**
  (`/etc/letsencrypt/live/kxpms.cn/fullchain.pem`)，自动跟随续期。
- 取消 252 上半过时的 `/etc/nps/conf/certs/hosts/llm.kxpms.cn/` 中转目录 — 凭空多一份拷贝，不利于 certbot 单一真相。

### D3: 154 不动 nginx config，纯 mirror

- 154 nginx `:443` 完全不动，server { server_name llm.kxpms.cn; } 保留作为 DNS flip 兜底。
- 154 的 cert 自动更新靠 `sync-to-154.sh` deploy hook（每次 252 certbot 续期后 scp 过去）。
- 这样如果在 `lem 252` 的 deploy hook 失败，154 仍然能 serve（虽然用的是上次续期时的 cert，过期前都有效）。

### D4: 跨服务器同步走专用 SSH key，不复用 sshpass

- 252 上 `/root/.ssh/cert-sync-252-154` ed25519 key，公钥已添加到 154 `/root/.ssh/authorized_keys`。
- 优势：cron / certbot hook 不需要交互式 sshpass；NOPASSWD 的 SSH key 在 certbot 重启 nginx 场景下天然安全。
- 部署脚本支持 `sshpass` 兼容（如果用户没生成 key，可临时用 SSHPASS）。

### D5: 显式 webroot_map 注入

- 首次 `certbot certonly --webroot` 不会自动写 `[[webroot_map]]` 段（certbot 1.22.0 行为）。
- 必须手动补 `kxpms.cn = /var/www/certbot` 和 `llm.kxpms.cn = /var/www/certbot`，
  否则 `certbot renew` 报 "renewal config file missing required file reference"。
- deploy-llm-kxpms-cert.sh 在 step_setup_certbot 里自动注入。

## 不做的事（以及原因）

| 选项 | 原因不选 |
|---|---|
| 用 DNS-01 续期 | 需要 Aliyun DNS API token + certbot-dns-alidns 插件（pip 安装需要外网），操作复杂度高。HTTP-01 工作得足够好。 |
| 保留 154→252 daily cron | 已被 certbot 续期取代；保留会双重复制证书可能引入竞态。脚本会清理 cron job。 |
| 在 252 用 NPS client 拉 154 | 用户明确说"不需要 NPC"，改用 nginx proxy 直接 forward，更简单且调试方便。 |
| 在 154 上跑 certbot 续期 | DNS 已指向 252，154 certbot 的 HTTP-01 必然失败；跨 proxy 复杂。 |

## 已知限制

1. **www.kxpms.cn 不在续期覆盖范围** — DNS 仍指向 154，需手动加 `www.kxpms.cn = /var/www/certbot` 到 webroot_map。
2. **force-push 到 main** — 另一个 worktree 占用了 main checkout，无法做标准 `git merge`，
   本 ADR 记录在案，未来可复原。
3. **LE staging 缓存** — `certbot renew --force-renewal` 经常会从 staging 返回相同的 cert，
   deploy hook 因此空跑（无新 cert 文件 cp）。生产环境真实续期行为会正常触发。

## 验证

- `curl https://llm.kxpms.cn/v1/models` via 252 — HTTP 200 + cert SAN match ✓
- `curl https://llm.kxpms.cn/v1/models` via 154 — HTTP 200 + cert valid ✓
- LLM chat completion 双端返回正常响应 ✓
- certbot 证书 SHA 在 252 与 154 一致：8e0987d8...edf5 ✓
- certbot-renew.timer active, systemd enabled, 12h cadence ✓
- sync-to-154.sh deploy hook 手动触发成功（ssh 154 nginx reload + 本地 reload）✓
- `git log --grep="llm.kxpms" origin/main` 包含 commits 1e86e679e, 92ebd146b, 8edcc9ead ✓

## 关键文件索引

| 路径 | 作用 |
|---|---|
| `deploy-llm-kxpms-cert.sh` | 一键部署 / 回滚 / 同步 / 强制续期 |
| `scripts/sync-llm-kxpms-cert.sh` | （旧）154→252 daily cron；已退役但仓库保留作为参考 |
| `scripts/sync-to-154.sh` | certbot deploy hook，252→154 续期同步 |
| `docs/architecture-decisions.md` | 本 ADR |

## 后续

1. 真正 LE 续期 (90 天后) 观察 deploy hook 实际触发日志
2. 检查 `www.kxpms.cn` 是否需纳入 (用户 DNS 决策)
3. 把 `deploy-llm-kxpms-cert.sh` 与 `scripts/sync-to-154.sh` 加入 env-injector 的 deploy skill catalog
4. `184` / `71` 镜像机器若承担 *.kxpms.cn 入口，复刻相同模式

---

**ADR 索引维护者：** AI 运维团队
**更新频率：** 有重大架构决策时更新
