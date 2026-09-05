# scripts/deploy — 服务注册单元模板

存放 install-service.sh 注册 init 系统服务时所用的单元模板。
脚本内通过 `sed` 把 `@PREFIX@` `@CONFIG_FILE@` `@LOG_DIR@` `@SERVICE_NAME@` `@SERVICE_USER@` `@SERVICE_GROUP@` 替换为真实值。

| 文件 | 平台 | init 系统 | 路径（安装后） |
| --- | --- | --- | --- |
| `systemd/llm-gateway-go.service` | Linux | systemd | `/etc/systemd/system/llm-gateway-go.service` |
| `launchd/com.kaixuan.llm-gateway-go.plist` | macOS | launchd | `/Library/LaunchDaemons/com.kaixuan.llm-gateway-go.plist` |
| `windows/install-service.ps1` | Windows | NSSM / sc.exe | `HKLM\...\LLM-Gateway-Go` |

**SOURCE_OF_TRUTH**: `~/workspace/ai-native-tools/llm-gateway/ai-native-maintain/deploy/{systemd,launchd,windows}/`，同步策略见每个文件顶部注释。

**何时改这里**:
- 加新安全限制（如 ProtectKernelTunables）→ 改 systemd unit + 同步 maintain
- 改日志路径或二进制目录 → 改全部 3 个 unit
- 加 Windows 支持（如 sc.exe 替代 NSSM）→ 改 windows/install-service.ps1

**何时不要改这里**:
- 业务身份（service name / user / group）— 由 `lifecycle/install-service.sh` 的 `--service-name` 等参数控制
- 二进制文件名（必须是 `gateway`）— `lifecycle/install-service.sh` line 69 `cp -p "$binary" "$destination/bin/gateway"`
