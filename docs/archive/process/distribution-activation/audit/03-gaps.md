# 缺口分析 · 2026-07-14

> 把 `02-current-state.md` 中标 ❌ / 🟡 的能力展开为可执行项。
> 每项：缺口描述 / 用户痛点 / 风险等级 / 关联用户需求。

## 1. 客户侧缺口

### G-C1 🟡 浏览器侧激活契约与 UI 不完整
- **现状**：`licensing/customer_api.go` 已实现并由 `cmd/gateway/main.go` 挂载，覆盖 `status/info/activate/offline-activate/offline-request/heartbeat`；CLI 也有对应能力。试用端点和 UI 仍未闭环。
- **用户痛点**：客户打开 `/setup` 后仍需 CLI 或手工调用 API，无法在浏览器完成试用、密钥和离线激活向导
- **影响**：所有非 Linux 客户（Windows/Mac GUI 用户）走不通；试用转化率被 CLI 门槛压低
- **关联**：C-4 / C-5
- **风险**：**高** — 试用转化直接受损

### G-C2 🟡 升级推送 Banner + UpgradePanel UI 缺失
- **现状**：`autoupdate/customer_api.go` 已由 `cmd/gateway/main.go` 挂载 `/api/system/upgrade/*`；首页 Banner / `/settings/upgrade` 面板没有，中心主动 push 也没有
- **用户痛点**：客户必须 SSH 上机器跑 `installer upgrade check` 才能看到升级提示；99% 客户不知新版已发
- **影响**：升级覆盖率 < 30%（按 11-实施路线图假设）
- **关联**：C-8
- **风险**：**高** — 安全补丁无法快速触达客户

### G-C3 🟡 独立 kx-gateway-rollback CLI 缺失
- **现状**：`installer upgrade rollback` 等价，但 11-路线图承诺的 `scripts/kx-gateway-rollback` 独立脚本没做
- **用户痛点**：升级失败后必须 SSH + 记忆路径找二进制，运维门槛高
- **影响**：平均回退耗时 > 1 分钟
- **关联**：C-9
- **风险**：**中** — 升级失败时延

### G-C4 🟡 Telemetry 设置 UI 缺失
- **现状**：`domains/hooks/observability/telemetry/client.go` 已写，但 `/api/system/telemetry/pref` + UI 没做
- **用户痛点**：用户开启/关闭采集需 SSH 上机器 + 改配置 + 重启
- **影响**：GDPR/合规风险（用户不能即时撤销）
- **关联**：C-7 / K-6
- **风险**：**中** — 合规

### G-C5 🟡 License 详情 UI 缺失
- **现状**：`licensing/restricted_mode.go` 显示"License 异常"，但 `/settings/license` 面板（customer/expiry/devices/audit）没做
- **用户痛点**：用户看不到到期日 / 设备数 / 续费入口
- **影响**：续费率被影响
- **关联**：C-4 / C-5
- **风险**：**中**

### G-C6 ❌ 浏览器侧离线激活 UI 缺失
- **现状**：`installer activate --mode offline-request --output activation.req` 可生成请求文件，但浏览器页面没有"上传 activation.req → 显示 activation.resp"的步骤
- **用户痛点**：离线客户需下载请求文件 → 邮件 → 等待人工审批 → 上传响应，所有步骤在 CLI 操作
- **影响**：政府/军工客户体验差
- **关联**：C-6
- **风险**：**低**（小众）

## 2. 中心侧缺口

### G-S1 ❌ 远程协助命令通道缺失
- **现状**：`center.NewAdminAPI.IssueCommand` 下发命令到客户端 `/api/center/command`，**但客户端接收端不存在**
- **用户痛点**：客户报故障时，运维无法远程抓日志 / 改配置 / 重启服务
- **影响**：所有 P1 故障响应 ≥ 30 分钟
- **关联**：S-7
- **风险**：**高** — 生产事故延展

### G-S2 ❌ 采集数据接收端缺失
- **现状**：客户端 `/api/v1/collect/runtime` 上报设计在 07 §四，但 cmd/license-authority 没注册这个端点
- **用户痛点**：即使客户端代码写了采集器（实际上 G-K1），主控端也没法收
- **影响**：运维仪表盘瞎
- **关联**：K-6 / S-5
- **风险**：**高** — 监控盲区

### G-S3 🟡 告警系统缺失
- **现状**：CPU>95/disk>95 阈值规则在 07 §4.1 stub，`alerts` 表 schema 文档承诺但迁移未做
- **用户痛点**：CPU 跑满 / 磁盘爆满没人知道
- **影响**：客户机器静默挂掉
- **关联**：S-6
- **风险**：**高** — 运维

### G-S4 🟡 下载请求埋点缺失
- **现状**：CDN `download.kxpms.cn` 上没埋点
- **用户痛点**：不知道哪个版本下载最多 / 哪个平台最受欢迎
- **影响**：分发优化无数据
- **关联**：S-3
- **风险**：**低**

### G-S5 🟡 多版本发布 UI 缺失
- **现状**：`autoupdate.Release` 数据层 + `NewAdminAPI.RegisterRoutes` 有，但 admin panel 没有"上传 + 发布 + 灰度" 3 步 UI
- **用户痛点**：Release manager 走 SQL 手插
- **影响**：发布效率低（半天 → 30 秒）
- **关联**：S-1
- **风险**：**中**

### G-S6 🟡 灰度规则引擎缺失
- **现状**：`autoupdate.Release.Gray*` 字段预留，无 canary/batch_1/2/3/full 5 阶段规则
- **用户痛点**：推 100% 客户承担全量风险
- **影响**：故障爆炸半径
- **关联**：U-4
- **风险**：**高** — 升级爆炸半径

## 3. 内核侧缺口

### G-K1 ❌ 采集器代码缺失
- **现状**：`gateway/internal/collector/` 整目录无文件。07 §三的 `collector.go` / `metrics.go` / `reporter.go` 全是 design doc，无代码
- **用户痛点**：客户机器运行情况主控端看不见
- **影响**：与 G-S2 共同导致监控盲区
- **关联**：K-6
- **风险**：**高** — 监控

### G-K2 ❌ 二进制自校验缺失
- **现状**：`licensing/antitamper.go` 不存在（08 §2.6 标 🔄 待补）
- **用户痛点**：客户用 IDA patch 掉 license 检查后能无限用
- **影响**：盗版损失（实际渗透路径）
- **关联**：K-1
- **风险**：**中** — 商业

### G-K3 ❌ 反调试检测缺失
- **现状**：`licensing/antidebug.go` 不存在（08 §2.5 标 🔄）
- **用户痛点**：客户用 gdb 在 license 校验函数下断点绕过
- **影响**：同上
- **关联**：K-1
- **风险**：**中**

### G-K4 ❌ nonce 防重放缺失
- **现状**：`licensing/nonce.go` 不存在。但 cmd/license-authority 的 `middleware/redis_nonce.go` 已实现 **服务端** nonce
- **用户痛点**：截获合法离线激活请求可重放
- **影响**：离线激活可被滥用（需配合 G-C6 才有意义）
- **关联**：K-1
- **风险**：**低**

### G-K5 🟡 增强指纹未实施
- **现状**：04 §5.3 描述的 Disk Serial / BIOS UUID / VirtType / CloudVendor 字段没在 `licensing/enhanced_fingerprint.go` 实现
- **用户痛点**：KVM 克隆的客户把整个镜像 copy 一份装到另一台机器（同一 fingerprint），照样能用
- **影响**：与 K-1 同
- **关联**：K-1
- **风险**：**中**

## 4. 升级推送缺口（重点）

### G-U1 ❌ 主控端主动 push 通道缺失
- **现状**：现状是**客户端 poll**：`installer upgrade check` 每 6h 拉 `/api/v1/updates/latest`。`cmd/license-authority/update_handler.go` 是被动的查询 API
- **用户痛点**：
  - 安全补丁推出去 → 平均要 6h 才有 50% 客户拉到
  - 紧急回退命令 → 平均 6h 才执行
  - 与"用户 6 类关键诉求"中的"我们对现有的部署的节点进行升级推送"目标差距大
- **影响**：响应时间从"分钟级"退化到"天级"
- **关联**：U-3 / U-4 / S-6
- **风险**：**极高** — 安全事件延展 + 用户体验

### G-U2 ❌ 命令审批工作流缺失
- **现状**：下发"立即升级"等敏感命令的二次确认（双人复核 + OTP）流程没设计
- **用户痛点**：主控账号被盗则全网停摆
- **影响**：安全风险
- **关联**：U-3
- **风险**：**极高** — 安全

### G-U3 ❌ 客户端 push 接收器缺失
- **现状**：客户端 `/api/center/command` 接收端**不存在**
- **用户痛点**：主控端无法 push 命令
- **影响**：与 G-U1 同
- **关联**：G-S1 / U-3
- **风险**：**极高**

## 5. 优先级总览

按 P0/P1/P2 排：

| 优先级 | 数量 | 关键项 |
|--------|------|--------|
| **P0（必须 12 周内）** | 6 | G-C1 / G-C2 / G-K1 / G-S2 / G-U1 / G-U3 |
| **P1（12-24 周）** | 8 | G-C3 / G-C4 / G-C5 / G-S1 / G-S3 / G-S6 / G-K2 / G-K5 |
| **P2（24+ 周）** | 5 | G-C6 / G-S4 / G-S5 / G-K3 / G-K4 / G-U2 |

详细工作量 + 分配见 `04-priorities.md` + `v2/05-implementation.md`。
