# Windows Host 安装

> 状态：NOT_RELEASE_READY
>
> 当前仓库中的 `scripts/deploy/windows/install-service.ps1` 仍包含 Maintain 项目残留（binary/env/log identity），尚未形成可验证的 Gateway Windows 服务契约。本页只记录阻塞和验收要求，不把该脚本作为客户生产安装入口。

## 当前支持边界

- Windows host 暂不列入 `RELEASE_READY`。
- Windows Docker/WSL 也没有统一、经过验收的官方客户入口。
- 不要执行文档中的旧一键安装命令，也不要将 Maintain binary 重命名为 Gateway binary。

## 必须完成后才能开放支持

1. 提供真正的 Gateway Windows amd64/arm64 artifact、`version.json`、web assets 和 checksum。
2. 修正 PowerShell 安装脚本的服务名、binary、环境变量、日志路径和配置加载方式。
3. 明确管理员权限、服务账户、`gateway.env` 权限、Windows Firewall 和事件日志策略。
4. 提供升级、失败回退、schema 向前兼容和 release identity 验证。
5. 在干净 Windows 主机执行安装、启动、`/healthz`、`/readyz`、`/version`、升级和回滚验收。
6. 补齐正式 `client-deploy.ps1` 或明确替代入口，并加入 artifact-content contract test。

## 目标契约（仅供实现/评审，不是当前操作指引）

| 项目 | 目标 |
| --- | --- |
| 进程监听 | 默认 `8781`，由 Gateway 配置明确设置 |
| 外部端口 | 由客户代理或防火墙策略决定；不能与 Docker `8080 -> 8781` 混写 |
| 健康检查 | `/healthz` liveness、`/readyz` DB+Redis readiness、`/version` release identity |
| 配置 | 使用 Gateway 环境变量和客户密钥管理，不写明文 secret |
| 服务 | 服务名、binary 和日志必须使用 Gateway identity |

在上述契约落地前，请使用 Linux/macOS host 或已验收的 Docker 路径，不要绕过状态标记。
