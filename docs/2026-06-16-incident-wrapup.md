# llm-gateway-go 事故复盘与收尾（2026-06-16）

## 背景

本次收尾覆盖 4 类问题：请求链路 403、管理接口 500、71/184 版本漂移、部署链路镜像获取失败。目标是把根因、修复、验证和防复发动作一次性固化，避免同类事故重复发生。

## 事故时间线（简版）

- 发现 `/v1/chat/completions` 在部分流量下返回 403，错误码为 `SESSION_FORBIDDEN`。
- 排查确认：业务 `session_id` 被误当成 `X-Gw-Session-Id` 网关会话头进入鉴权分支。
- 发现 `/api/keys/:id/reveal` 在个别历史数据下返回 500。
- 排查确认：存在脏数据 `key_ciphertext=test-cipher`，不符合可解密密文格式，触发解密异常。
- 发现 71 节点前端显示版本异常（`build_seq=999`）且与 184 不一致。
- 排查确认：同一 tag 镜像被覆盖，导致 71/184 在“同 tag”下运行了不同产物。
- 部署过程中遇到 buildx/base image 拉取 403，触发离线补丁路径（save/scp/load）保障可部署性。

## 根因分析

### 1) `/v1/chat/completions` 403 (`SESSION_FORBIDDEN`)

- **直接原因**：调用方把业务会话字段错误映射到 `X-Gw-Session-Id`。
- **系统性原因**：网关会话头与业务会话字段语义边界不清，缺少显式前缀约束时，误用会直接进入会话授权逻辑。

### 2) `/api/keys/:id/reveal` 500

- **直接原因**：历史脏数据 `key_ciphertext=test-cipher` 不是合法密文，解密失败抛错。
- **系统性原因**：历史写入阶段未做密文格式保护，读取阶段又把“不可解密数据”当成 500 内部错误处理。

### 3) 71 节点版本漂移 / `build_seq=999`

- **直接原因**：同 tag 镜像在仓库或节点侧被覆盖，71/184 拉取到不同内容。
- **系统性原因**：部署时只校验“tag 是否一致”，未强制校验跨节点的产物一致性（digest/deploy_seq）。

### 4) buildx/base image 403

- **直接原因**：构建环境在拉取基础镜像时被 403/网络策略拦截。
- **系统性原因**：默认在线拉取路径缺少兜底，离线镜像补丁流程未固化成标准步骤。

## 修复动作

### 已执行

- 对会话头语义进行约束：网关会话头仅接受 `gw_` 前缀（防止业务 `session_id` 误入）。
- `reveal` 接口错误语义调整：不可解密数据按冲突态返回（409），不再误报 500。
- 部署脚本增强：`scripts/deploy-llm-gateway-go-71.sh` 增加 71/184 一致性校验：
  - tag 一致性（强校验）
  - `deploy_seq` 一致性（强校验）
  - digest 一致性（best-effort，拿不到元数据时告警）
- buildx/base image 异常时使用离线补丁链路（184 导出、经本机中转、71 导入）。

### 脚本改动（本次）

- 文件：`scripts/deploy-llm-gateway-go-71.sh`
- 新增阶段：`Cross-node consistency check (71 vs 184)`
- 失败策略：一旦发现 tag 或 `deploy_seq` 漂移，立即 `FATAL` 退出，阻断错误版本继续对外。

## 验证结果

- 文档落库：本文件已纳入 `services/llm-gateway-go/docs/`。
- 脚本语法：`bash -n scripts/deploy-llm-gateway-go-71.sh` 通过。
- 关键校验逻辑静态检查：
  - 已包含 `tag parity`、`deploy_seq`、`digest parity` 三段输出与异常分支。
  - 保持最小改动，不影响既有部署主路径。

## 防复发措施（强制）

### A. Session Header 约束

- 网关只认可 `gw_` 前缀的 `X-Gw-Session-Id`。
- 业务侧 `session_id` 与网关会话头必须明确分层，禁止互相复用字段。

### B. Reveal 错误语义

- `key_ciphertext` 不可解密时统一返回 409（数据冲突/历史脏数据），并输出可观测错误码。
- 禁止再把该类问题归类为 500。

### C. 71/184 产物一致性

- 部署后必须满足：
  - 同 tag
  - 同 `deploy_seq`
  - 能取到 digest 时同 digest
- 任一强校验失败即阻断发布。

### D. 部署后 Checklist

1. `healthz`：71 本地 + 公网域名均为 200。
2. 71/184 一致性校验通过（脚本输出无 `FATAL`）。
3. 抽样调用 `/v1/chat/completions`，确认无误判 `SESSION_FORBIDDEN`。
4. 抽样调用 `/api/keys/:id/reveal`，历史脏数据场景返回 409。
5. 记录发布产物：tag、`deploy_seq`、digest（可用时）到当次 checkpoint/发布记录。

## 经验总结

- “同 tag”不等于“同产物”，发布系统要对内容一致性负责，而不是只看标签。
- 历史脏数据是长期资产风险，读取链路必须有明确降级语义，避免把数据问题升级为平台故障。
- 部署链路必须默认具备离线兜底能力，避免在线依赖异常导致不可恢复的发布阻塞。
