# 现状能力矩阵 · 2026-07-14

> 按"客户-中心-内核"三端 + 升级推送共 27 项能力盘点。
> 状态：✅ 已落地 / 🟡 部分落地 / ❌ 缺失

## 1. 客户侧（9 项）

| # | 能力 | 文档 | 状态 | 落地位 | 备注 |
|---|------|------|------|--------|------|
| C-1 | 下载 release 包 | 09 §六 | 🟡 | `installer/cmd/llm-gw-installer/main.go` | **下载门户缺失**，目前只有 installer 二进制 |
| C-2 | 校验 SHA256 + 签名 | 09 §三 | ✅ | `autoupdate/downloader_gpg.go` | GPG/SHA256SUMS 完整 |
| C-3 | 本机一键安装 | 03 §M1-M5 | ✅ | `installer/cmd/llm-gw-installer/install.go` | 5 种模式（离线/在线/K8s/Docker/DB 独立）|
| C-4 | 试用激活 | 05 §2.1 | 🟡 | `licensing/customer_api.go` + `installer/cmd/llm-gw-installer/activate.go` | **License API 已挂载；试用语义、错误码和浏览器 UI 仍需统一** |
| C-5 | License Key 激活 | 05 §2.2 | 🟡 | 同上 | 同上 |
| C-6 | 离线激活 | 05 §2.4 | ✅ | 同上 | offline-request + offline-import 完整 |
| C-7 | 心跳上报 | 05 §5.2 | ✅ | `installer/cmd/llm-gw-installer/heartbeat.go` | 60s 心跳到 `/api/v1/instances/heartbeat` |
| C-8 | 自动升级 | 06 §四 | ✅ | `installer/internal/upgrader/apply.go` | 备份+原子替换+healthz；rollback 已实现 |
| C-9 | 紧急回退 | 06 §六 | 🟡 | `installer/cmd/llm-gw-installer/upgrade.go::rollback` | **CLI 等价；独立 kx-gateway-rollback CLI 缺失** |

**客户侧总分：7.5/9 (83%)** — License/升级 API 已有，浏览器 UI、试用契约和主动推送补偿仍缺失

## 2. 中心侧（8 项）

| # | 能力 | 文档 | 状态 | 落地位 | 备注 |
|---|------|------|------|--------|------|
| S-1 | 上传多版本安装包 | 09 §五 | 🟡 | `autoupdate.Store` 已有 | **upload UI 在 admin panel 缺失**，数据层已有 |
| S-2 | 自动更新版本信息 | 09 §四 | ✅ | `version.json` SSOT + `scripts/bump-version.sh` | bump-version 自动递增 build_seq |
| S-3 | 收集下载请求 | 09 §二 | ❌ | 无 | **无埋点 + 统计** |
| S-4 | 查看激活信息 | 05 §三 + 11 §四 | ✅ | `licensing/admin_api.go::RegisterRoutes` | `/api/admin/licenses/{id}/devices` 等 9 个端点 |
| S-5 | 查看节点情况 | 07 §四 | ✅ | `center.NewAdminAPI` + `cmd/license-authority/heartbeat_handler.go` | 234 在线 / 12 离线 / 3 降级 |
| S-6 | 预警 | 07 §4.1 | 🟡 | `licensing/admin_api.go` partial | **CPU>95/disk>95 阈值规则已 stub，alerts 表缺失** |
| S-7 | 远程协助 | 14 v2 §02 | ❌ | 无 | **无 SSH reverse / 命令通道** |
| S-8 | 分析 | 07 §4.2 | 🟡 | `web/src/views/ops/CenterOpsView.vue` | **UI 在，聚合/导出未做** |

**中心侧总分：3.5/8 (44%)** — License + 节点监控完整；下载统计、远程协助、告警系统缺失

## 3. 内核侧（7 项）

| # | 能力 | 文档 | 状态 | 落地位 | 备注 |
|---|------|------|------|--------|------|
| K-1 | 防盗版（9 类攻击）| 08 §二 | 🟡 | `licensing/{crypto,fingerprint,clock}.go` | 已实现：篡改/未授权/一码多用/时钟回拨；缺失：调试/反篡改/重放（antidebug/antitamper/nonce）|
| K-2 | 设备端信息获取 | 04 §五 | ✅ | `licensing/fingerprint.go` | machineid/cpu/hostid/mac/disk/bios |
| K-3 | License 计算（签名）| 04 §四 | ✅ | `licensing/crypto.go` | RSA-2048 + AES-256-GCM |
| K-4 | 激活计算 | 04 + 05 | ✅ | `licensing/activator.go` + `cmd/license-authority/{register,activate}` | 在线 + 离线 + 试用三入口 |
| K-5 | 使用时长管理 | 02 §6 + 04 §六 | ✅ | `licensing/clock.go` + `token_refresh.go` | 7 天 grace + 24h refresh |
| K-6 | 采集 API | 07 §三 | ❌ | 无 | **`gateway/internal/collector/` 整目录缺失** |
| K-7 | 数据管理（采集/审计）| 07 | 🟡 | telemetry middleware 已写 | **runtime_metrics / instance_info / telemetry_prefs 三张表缺失** |

**内核侧总分：4.5/7 (64%)** — 算法完整；采集器代码 + 高级防盗版缺失

## 4. 升级推送专题（4 项）

| # | 能力 | 文档 | 状态 | 落地位 | 备注 |
|---|------|------|------|--------|------|
| U-1 | 客户端轮询检查更新 | 06 §3.6 | ✅ | `installer/cmd/llm-gw-installer/upgrade.go::check` | 6h 间隔 |
| U-2 | 主控端下发升级命令 | 06 §3.2 | 🟡 | `cmd/license-authority/update_handler.go::latest` | **API 在，但仅"manifest 查询"，无 push 通道** |
| U-3 | 主动推送 upgrade 通知 | 14 v2 §04 | ❌ | 现有 `/api/system/upgrade/*` 为客户侧查询/执行 API | **缺失：主控端主动 push、长连接和命令队列** |
| U-4 | 灰度发布 | 11 §M2 | 🟡 | `autoupdate.Release.Gray*` 字段已预留 | **UI + 规则引擎缺失** |

**升级推送总分：1.5/4 (38%)** — 客户拉取在；主控主动推送能力**几乎空白**

## 5. 维度评分（5 维 × 10 分）

| 维度 | 当前 | 主要扣分项 |
|------|------|-----------|
| 功能完整性 | 6.0 | 浏览器侧 API + 远程协助 + 采集器代码 |
| 代码成熟度 | 7.5 | 已实现路径覆盖测试好；新加模块需测试 |
| 集成度 | 5.5 | 三端能跑通主路径，但采集+预警+推送未串通 |
| 运维性 | 4.5 | 无远程命令通道 + 告警系统缺失 |
| 防盗版与安全 | 6.5 | 基础 4/9 类完整；高级 3 类 + HSM 缺失 |

**总分：30/50 (60%)**

## 6. 一句话总结

**主链路通，扩展能力空。** 客户→中心的 license 流程已 production-ready；
中心→客户的"主动推送 / 远程协助 / 告警"几乎全空；内核的"采集器代码 + 高级防盗版"待补。

完整方案见 `v2/`（下节）。
