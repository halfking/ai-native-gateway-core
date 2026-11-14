# Plugin Runtime Config

配置加载器通过 fsnotify 监听 YAML 配置，并串行应用重载。读取方通过 atomic.Value 获取完整配置快照。

## 重载与回滚

流程为：文件变化 → 100ms 防抖 → 解析与验证 → 依次应用回调 → 发布新配置。

任何回调失败时，配置仍保持旧值。加载器从失败回调开始，以反向顺序、交换后的新旧参数调用所有已尝试回调，恢复旧状态；失败回调也必须能够撤销部分执行结果。回滚使用独立的 10 秒超时上下文，原始错误和回滚错误一并返回。回调应幂等、支持应用任一配置，并响应上下文取消。

父目录监听覆盖 Write、Create、Rename、Remove，因此编辑器和部署脚本通过原子重命名替换配置文件后仍可继续重载。手动 Reload 与文件监听串行执行；Stop 取消正在执行的重载，等待监听和回调退出。多个 Stop 调用等待同一停止结果。

## 使用

```go
reloader, err := config.NewConfigReloader(initialConfig, config.NewValidator())
if err != nil {
    return err
}
defer reloader.Stop()
if err := reloader.OnReload(func(ctx context.Context, old, next *config.Config) error {
    return applyConfig(ctx, next) // 必须也支持重新应用 old 配置
}); err != nil {
    return err
}
if err := reloader.Watch(ctx, "/etc/llm-gateway/gateway.yaml"); err != nil {
    return err
}
current := reloader.GetConfig() // 调用方不可修改已发布快照
```

OnReload 必须在 Watch 之前完成注册。GetConfig 仍返回旧快照，直到全部回调成功；回调期间外部服务的短暂变化由业务层管理。

配置示例：

```yaml
server:
  host: 0.0.0.0
  port: 8080
plugins:
  directory: /etc/llm-gateway/plugins
  enabled: [auth, rate-limit]
security:
  tls_enabled: false
```

Validator 校验 host、1–65535 范围内的端口、插件目录，以及启用 TLS 时的证书与密钥文件。加载器本身不创建监听端口或 TLS 服务。网关生产接入见 `../controller/README.md`：当前支持热重载插件启用列表，端口、安全配置和插件目录变化需要重启。

## 验证

```bash
go test -race -count=1 -cover ./...
go vet ./...
```

回归覆盖回调失败不发布、逆序回滚、原子文件替换、停止时取消以及并发停止等待。
