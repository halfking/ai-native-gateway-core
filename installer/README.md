# llm-gateway-go 一键安装器（Installer）

> Go 单二进制跨平台安装器，支持 Windows / Linux / macOS / 国产 OS / 国产 CPU。
> 内置 4 层镜像源兜底：离线包 → 内部 registry → 国内 mirror → 官方源。

## 目录结构

```
installer/
├── cmd/llm-gw-installer/
│   ├── main.go              # Cobra CLI 入口
│   └── embeddata/           # go:embed 资源（compose.yml / SQL / 模板）
├── internal/
│   ├── envdetect/           # OS/arch/docker/网络探测
│   ├── imgsrc/              # 4 层镜像源 fallback
│   ├── prompt/              # 11 步交互向导
│   ├── secrets/             # 随机密码 + .env 写入
│   ├── dockerutil/          # compose 封装 + 健康检查
│   ├── dbinit/              # SQL schema 应用
│   └── report/              # 部署报告生成
├── templates/               # 模板源文件（同步到 embeddata/）
├── sql/                     # 复用 deploy/sql/
├── go.mod
└── README.md
```

## 快速开发

```bash
# 编译当前平台
GOPROXY=https://goproxy.cn,direct go build -o /tmp/llm-gw-installer ./cmd/llm-gw-installer/

# 测试
/tmp/llm-gw-installer doctor
/tmp/llm-gw-installer install

# 跨平台编译
make cross-compile   # 见下方 Makefile（可选）
```

## 子命令

```
llm-gw-installer doctor      # 检测环境（OS/docker/网络/端口）
llm-gw-installer install     # 一键安装并部署
llm-gw-installer uninstall   # 卸载（--purge 彻底清理）
```

## 跨平台编译

```bash
# Linux/macOS
GOOS=linux  GOARCH=amd64 go build -o dist/llm-gw-installer-linux-amd64 ./cmd/llm-gw-installer/
GOOS=linux  GOARCH=arm64 go build -o dist/llm-gw-installer-linux-arm64 ./cmd/llm-gw-installer/
GOOS=linux  GOARCH=loong64 go build -o dist/llm-gw-installer-linux-loong64 ./cmd/llm-gw-installer/
GOOS=darwin GOARCH=amd64 go build -o dist/llm-gw-installer-darwin-amd64 ./cmd/llm-gw-installer/
GOOS=darwin GOARCH=arm64 go build -o dist/llm-gw-installer-darwin-arm64 ./cmd/llm-gw-installer/
GOOS=windows GOARCH=amd64 go build -o dist/llm-gw-installer-windows-amd64.exe ./cmd/llm-gw-installer/
GOOS=windows GOARCH=arm64 go build -o dist/llm-gw-installer-windows-arm64.exe ./cmd/llm-gw-installer/
```

## 环境变量

| 变量 | 默认值 | 用途 |
|---|---|---|
| `KX_REGISTRY` | `registry.kxpms.cn` | 自定义内部 registry |
| `KX_REGISTRY_USERNAME` | 空 | 自定义 registry 用户名 |
| `KX_REGISTRY_PASSWORD` | 空 | 自定义 registry 密码 |
| `KX_REGISTRY_INSECURE` | `false` | 允许 HTTP |
| `APP_IMAGE_TAG` | 从 MANIFEST 读 | 应用镜像 tag 覆盖 |
| `GOPROXY` | `https://goproxy.cn,direct` | go module 代理 |

## 镜像源 fallback 链

```
[1] 离线包 images/*.tar.gz（最高优先级）
    ↓ 失败
[2] registry.kxpms.cn（内部 registry）
    ↓ 失败
[3] registry.cn-hangzhou.aliyuncs.com（阿里云 mirror）
    ↓ 失败
[4] registry-1.docker.io（官方 docker hub）
    ↓ 全部失败
❌ 清晰报错
```

## 单元测试

```bash
go test ./...
```

## 嵌入资源

所有 SQL 文件、compose 模板、报告模板都通过 `go:embed` 内嵌到二进制中：

```go
//go:embed embeddata/compose.yml
var composeYAML []byte

//go:embed embeddata/00-prereqs.sql
var sqlPrereqs []byte
// ...
```

修改 templates 后需同步到 `cmd/llm-gw-installer/embeddata/`。

## 集成测试

端到端测试需要真实 docker 环境。建议在 CI 中跑：

```bash
make test-e2e
```

测试场景：
1. 场景 A：离线包完整 → 直接 load 成功
2. 场景 B：离线包损坏 + 内网通 → 从 registry.kxpms.cn 拉取
3. 场景 C：内网不通 + 公网通 → 从 aliyun mirror 拉取
4. 场景 D：完全断网 → 清晰报错
5. 国产 OS：自动装 docker + 全流程

## 已知限制

- **HarmonyOS NEXT**：不支持（没有 Linux 容器支持）
- **macOS**：需要用户手动装 OrbStack 或 Docker Desktop
- **Windows**：需要用户手动装 Docker Desktop + WSL2
