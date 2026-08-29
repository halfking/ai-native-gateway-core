# Windows Host 安装（NSSM / sc.exe）

## 快速上手

```powershell
# 1. 一行式（生产，PowerShell）
irm https://llmgo.kxpms.cn/maintain-api/distribution/install-scripts/llm-gateway-host.ps1 | iex

# 2. 本地脚本（开发）
cd services/llm-gateway-go/scripts/deploy/windows
powershell -ExecutionPolicy Bypass -File .\install-service.ps1 -Action install `
  -ReleaseDir 'D:\kaixuan\llm-gateway\releases\2.4.7.1795' `
  -ConfirmAction
```

## 默认路径

- `D:\kaixuan\llm-gateway\`（D: 可写时；不可写回退 `C:\llm-gateway\`）
- `%ProgramFiles%\LLM-Gateway-Go\gateway.exe`（service binary）
- `%ProgramData%\LLM-Gateway-Go\gateway.env`（mode 0600 等效）
- 服务名：`LLM-Gateway-Go`

## 服务注册

`install-service.ps1 -Action install`：

1. 复制 `gateway.exe` 到 `%InstallRoot%`
2. 写 `%ConfigFile%`（首次拷 `gateway.env.example`）
3. `sc.exe create LLM-Gateway-Go binPath= "%InstallRoot%\gateway.exe" start= auto` 或 NSSM `nssm install LLM-Gateway-Go ...`
4. `sc.exe start LLM-Gateway-Go`

**注意**：sc.exe / NSSM 在 Windows 上**不支持** `@PREFIX@` / `@CONFIG_FILE@` sed 占位符——参数在 install-service.ps1 内已硬编码。

## 升级

```powershell
# 1. 停服务
Stop-Service LLM-Gateway-Go

# 2. 备份当前
Copy-Item D:\kaixuan\llm-gateway\releases\<old> D:\kaixuan\llm-gateway\releases\<old>.bak

# 3. 解压新 bundle
Expand-Archive .\llm-gateway-go-<v>-windows-amd64.zip -DestinationPath D:\kaixuan\llm-gateway\releases\<new>

# 4. 更新 current symlink (需 admin)
New-Item -ItemType SymbolicLink -Path D:\kaixuan\llm-gateway\current -Target D:\kaixuan\llm-gateway\releases\<new>

# 5. 启服务
Start-Service LLM-Gateway-Go

# 6. preflight (从 Linux/macOS 机器)
curl http://<host>:8080/healthz | ConvertFrom-Json
curl http://<host>:8080/readyz
curl http://<host>:8080/version | ConvertFrom-Json
```

> **计划中**：PowerShell 版的 `upgrade-host.ps1` 会把这套流程化（与 macOS/Linux 的 `upgrade.sh` 对齐）。当前 PR 范围只覆盖 install + service 注册。

## 回退（人工）

```powershell
Stop-Service LLM-Gateway-Go
New-Item -ItemType SymbolicLink -Path D:\kaixuan\llm-gateway\current -Target D:\kaixuan\llm-gateway\releases\<old> -Force
Start-Service LLM-Gateway-Go
```

## 故障排查

```powershell
# 服务状态
Get-Service LLM-Gateway-Go
sc.exe query LLM-Gateway-Go

# 实时日志
Get-Content "$env:ProgramData\LLM-Gateway-Go\logs\gateway.stdout.log" -Wait

# eventlog
Get-EventLog -LogName Application -Source LLM-Gateway-Go -Newest 50

# 健康检查（gateway 默认 listen 0.0.0.0:8080）
curl http://127.0.0.1:8080/healthz | ConvertFrom-Json
curl http://127.0.0.1:8080/readyz
curl http://127.0.0.1:8080/version | ConvertFrom-Json

# Windows firewall（首次安装后需放行 8080）
New-NetFirewallRule -DisplayName "LLM-Gateway-Go" -Direction Inbound -Protocol TCP -LocalPort 8080 -Action Allow
```
