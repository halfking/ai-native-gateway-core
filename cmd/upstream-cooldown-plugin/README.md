# PL1 错误处置建议插件

`upstream-cooldown` 从错误处理手册后台运行。它按租户、供应商和已关闭的 UTC 五分钟窗口汇总 `candidate_failure_logs_hot`。同一组至少有三个不同的请求收到真实的上游 HTTP 429 或 502 时，才请求建议。429 与 502 可共同构成一组。同一请求的多次重试只计为一个不同请求。

采集器在候选失败日志中标记真实状态的来源。试点摄取带该标记的新日志，并忽略网关转换的 502 和缺少状态来源的历史行。每轮回看 15–65 分钟，最多向插件提交 16 组。聚合查询、单次调用和整轮的上限分别为 2 秒、300 毫秒和 5 秒。

插件通过受 Controller 管理的私有 Unix 套接字接收 v1 聚合信号。载荷只包含窗口起点、429 次数、502 次数和不同请求数。请求和响应各限 1 KiB。网关校验 v2 manifest 的能力和权限声明、握手能力、就绪进程、响应模板版本、建议枚举和冷却时长。

当前模板建议冷却 300 秒（429）或 120 秒（502），混合组取较长时长。建议使用 modelbudget 同系列模型降级阶梯的语义，提示操作员评估下一档。建议保存在现有执行记录中，状态为 `awaiting_approval`，没有可执行步骤。记录按窗口、供应商、租户和模板版本去重。

## SSE 断流与配置漂移

`sse-break-analysis` 复用候选失败日志的结构化 `stream_reason`，沿用五分钟窗口、租户与供应商隔离、三个不同请求的门槛。客户端主动断开、普通 HTTP 失败和无法分类的原因不计入。模板以 60% 为主导证据门槛，对上游明确错误、传输中断或空闲超时提出核查方向。空闲超时同时提示查看反代超时和上游刷新间隔。证据混合时按时间关联各方记录。

`config-drift` 的监测范围是 `LLM_GATEWAY_PLUGIN_RUNTIME_CONFIG` 指向的 YAML。Controller 对完成拒收的文件核对 SHA-256，磁盘仍对应这次拒收时才请求建议。解析或校验失败提示对照上一份已接受配置。可解析但未能热应用的更改提示核对批准版本。配置内容、路径和校验和均留在网关，插件只收到拒收类别。审核记录以快照对的摘要和模板版本去重。可解析配置按解析后的值去重，注释不会产生第二条记录。恢复或成功热加载会清除当前拒收快照，已有建议留作历史记录。拒收快照来自当前进程的实际重载，适用范围为最多 1 MiB 的可读配置。

## 构建和启用

从 Go 仓库根目录构建预置包。网关和插件二进制须使用相同的目标操作系统和架构。

```sh
for plugin in upstream-cooldown sse-break-analysis config-drift; do
  bundle="$PLUGINS_DIR/$plugin/1.0.0"
  install -d -m 0750 "$bundle"
  go build -o "$bundle/$plugin" "./cmd/$plugin-plugin"
  install -m 0644 "cmd/$plugin-plugin/plugin-manifest.json" "$bundle/"
  install -m 0644 "cmd/$plugin-plugin/template.yaml" "$bundle/"
done
```

开启签名校验的环境使用现有的 Ed25519 发布流程，将原始签名保存为包内的 `plugin-manifest.json.sig`，并设置 `LLM_GATEWAY_PLUGIN_SIGNING_PUBKEY` 为十六进制公钥。由部署账户管理二进制、模板和清单所在目录。首次发布时，在文件准备完毕后创建版本链接。

```sh
for plugin in upstream-cooldown sse-break-analysis config-drift; do
  ln -s 1.0.0 "$PLUGINS_DIR/$plugin/current"
done
```

为网关设置 `LLM_GATEWAY_PLUGIN_RUNTIME_CONFIG`，使配置的监听地址与网关一致。以下是已启用试点的配置示例。

```yaml
server:
  host: localhost
  port: 8080
plugins:
  directory: /opt/gateway/plugins
  enabled: [upstream-cooldown, sse-break-analysis, config-drift]
```

另外启用 `error_playbook.enabled`（环境变量回退为 `LLM_GATEWAY_ERROR_PLAYBOOK_ENABLED=true`）。试点由两个开关共同控制。错误手册的自动执行和 LLM 分析开关控制各自的既有流程。停用某个模板时，从 `plugins.enabled` 移除对应 ID。

本地网关已有数据库等运行配置时，可以通过以下命令启用试点。

```sh
LLM_GATEWAY_PLUGIN_RUNTIME_CONFIG="$PWD/gateway.yaml" \
  LLM_GATEWAY_ERROR_PLAYBOOK_ENABLED=true go run ./cmd/gateway
```

平台超级管理员可在现有错误处理手册页查看最近 20 条建议，也可分页查询执行记录 API。

```sh
curl -fsS -H "Authorization: Bearer $PLATFORM_TOKEN" \
  "$GATEWAY_URL/api/admin/error-playbook/executions?recommendations=only&status=awaiting_approval"
```

升级时把新版文件放入独立版本目录，更新 manifest 的 `plugin_version` 和 `build_seq`，签名后原子切换 `current`。Controller 会探测新版就绪状态，并在失败时恢复之前的运行快照。进程切换期间后台建议可能暂时不可用。推荐保留上一版包，以便显式切回。

## 本地验收

定向集成测试构建真实插件、生成一次性签名并启动 Controller。它在专用 PostgreSQL 中建立和清理自己的 schema，通过真实候选失败写入器和错误处理手册管理 API 核对结果。HTTP/SSE 对拍使用本地 v2 dispatch 路由和替身上游，校验状态码、内容类型、响应体和 SSE 首帧实时刷新。

在已有的隔离 PostgreSQL 17 上可直接设置 `TEST_UPSTREAM_COOLDOWN_DSN`。下面的示例会启动仅监听本机的临时实例。验收使用了项目本地镜像 `kx-citus-pg17:offline-arm64`。

```sh
db_container="$(docker run --rm -d -e POSTGRES_HOST_AUTH_METHOD=trust \
  -p 127.0.0.1::5432 "${COOLDOWN_TEST_PG_IMAGE:-postgres:17}")"
trap 'docker stop "$db_container" >/dev/null' EXIT
until docker exec "$db_container" pg_isready -U postgres -h 127.0.0.1 >/dev/null; do sleep 1; done
docker exec "$db_container" psql -U postgres -v ON_ERROR_STOP=1 \
  -c 'CREATE ROLE cooldown_test LOGIN NOSUPERUSER NOBYPASSRLS'
docker exec "$db_container" psql -U postgres -v ON_ERROR_STOP=1 \
  -c 'CREATE DATABASE cooldown_test OWNER cooldown_test'
db_address="$(docker port "$db_container" 5432/tcp)"
TEST_UPSTREAM_COOLDOWN_DSN="postgres://cooldown_test@$db_address/cooldown_test?sslmode=disable" \
  go test ./cmd/gateway -run '^(TestUpstreamCooldown(Pilot|MissingAtStartup)|TestRemediationTemplates)$' -count=1 -v
go test ./extpoints ./plugin-runtime ./plugin-runtime/controller ./domains/errorplaybook
make test-plugin-runtime-modules
```

断言结果为禁用时 0 条、首版 3 条、重复回放仍为 3 条。单独的 429、502 和混合组各产生一条建议，同一供应商的不同租户独立计数。并发升级回放后共有 6 条，两次升级失败、坏结果、超时、崩溃和停用后的计数均保持为 6 条。

SSE 验收注入四类聚合，核对上游、空闲超时、传输和混合建议各一条。重复请求、主动断开、跨租户凑数和未来窗口均保持 0 条。配置验收追加两条，分别为热加载拒收和无效 YAML。只改注释、重复拒收、恢复有效配置及停用后的拒收均保持计数。缺少一个预置包时，另外两个已配置模板仍能启动。周期协调中，损坏版本回滚到原包，健康的可选模板可继续更新。
