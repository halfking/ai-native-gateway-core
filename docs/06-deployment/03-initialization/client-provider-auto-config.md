# 客户端模型供应商自动配置

> 更新：2026-10-12。配置文件与下载脚本包含 API Key，应按凭据保管；不提交生成结果、密钥或数据库凭据。

## 接入方式

在 **接入指南 → 自动配置** 中选择客户端、系统、API Key、模型范围和供应商名称。默认名称为 `itestu`，可以填写 `itestuu` 或其他名称；仅接受 1–64 个字母、数字、下划线或连字符，首字符须为字母或数字。默认 API 地址为当前站点 origin 加 `/v1`；本任务地址为 `https://llm.itestu.cn/v1`。地址仅接受 HTTP(S)，不能包含账号、查询参数或片段。

| 客户端 | 配置位置 / 入口 | 生成方式 |
|---|---|---|
| ZCode | macOS/Linux：`~/.zcode/v2/config.json`；Windows：`%APPDATA%/zcode/v2/config.json` | `provider` 映射、UUID ID、`kind: openai-compatible`，支持增量脚本。 |
| MiniMax Code | `~/.minimax/config.yaml`；Windows：`%USERPROFILE%/.minimax/config.yaml` | `custom_provider` 映射、`api: openai-completions`，支持增量脚本。 |
| CC Switch | MiniMax Code 页面内的添加/导入操作 | 有独立供应商数据库；写入 MiniMax YAML 不等于完成 CC Switch 导入，需在应用内确认名称、地址、密钥和模型。 |
| Cursor | Settings → Models → API Keys | 手工设置 OpenAI Key、Override OpenAI Base URL 和 Custom Model。 |
| OpenCode v1 | `~/.config/opencode/opencode.json`，遵守 `XDG_CONFIG_HOME` | `provider` 映射，使用 `npm: @ai-sdk/openai-compatible`；支持增量脚本。检测到 JSONC、自定义路径或 v2 `providers` 时停止，提示手动处理。 |
| Roo Code | 扩展设置 → OpenAI Compatible | 手工填写 Base URL、API Key 和一个实际 Model ID；不写 VS Code 的伪配置字段。 |
| Cherry Studio | 设置 → 模型服务 → 添加自定义服务商 | 手工填写参数；生成的 JSON 是参数清单，不是已确认的客户端导入格式。 |

Cursor 的 Key 和 Base URL 是全局覆盖项，不能作为独立命名供应商使用。自定义 Key 用于标准聊天模型，Tab 补全等专用功能继续使用 Cursor 服务。与内置模型同名时可能拒绝添加；模型出现在选择器中不足以证明它经自定义 Base URL 请求。配置后需分别检查 Ask/Agent。参见 [Cursor API Keys 文档](https://docs.cursor.com/settings/api-keys)。

## 模型来源

`GET /api/client-configs/featured?key_id=<ID>` 根据选定活动 Key 的租户，聚合该租户 **全部** `routing_policy.featured_models`，去空白并去重。tenant_admin 不能查询其他租户的 Key，super_admin 可选择其有权限管理的 Key；无特色模型的租户返回空集合，不回退到 default 租户。失效 Key 和数据库错误会明确报错，生成器不会沿用旧清单。

2026-10-12 生产数据库有 4 条策略、一个配置特色模型的租户（default），共 28 个 ID。当前第一条策略恰好也包含这 28 个；聚合修复用于防止后续拆分策略时丢失模型。

```text
claude-fable-5-1
claude-opus-4-7
claude-opus-4-8
claude-opus-5-5
claude-sonnet-4-6
claude-sonnet-5
deepseek-v4-flash
deepseek-v4-pro
doubao-1-5-pro-32k
doubao-seed-2-0-code
doubao-seed-2-0-code-preview
gemini-3-flash-preview
gemini-3.5-flash
glm-5.3
glm-5.3-flash
gpt-5.5
gpt-5.6-luna
gpt-5.6-sol
gpt-5.6-terra
gpt-6-astra
gpt-6-luna
gpt-6-sol
gpt-image-2
grok-4.7
kimi-k3
minimax-m2.7
minimax-m2.7-highspeed
minimax-m3
```

`doubao-seed-2-0-code` 是活动别名，没有同名 canonical 项。生成器保留其特色 ID，并通过活动 `model_aliases` 获取 canonical 上下文窗口；废弃别名不作为元数据来源。没有窗口/输出上限时不虚构值。OpenCode v1 要求完整的 context/output 组合，缺一则省略整个 limit；ZCode/MiniMax 可写入已知 context。数据库目前将 `gpt-image-2` 标记为 text，本轮保留特色列表原值，未据名称擅自修改模型数据。模型目录数量会动态变化，不作为成功判据。

## 脚本执行与保护

1. ZCode/OpenCode 脚本需要 Python 3；MiniMax 还需要 PyYAML。生成器不自动安装依赖。macOS/Linux 执行下载的 `.sh`；Windows 使用 PowerShell `.ps1`。Windows 脚本构造已检查，尚无真实 Windows 运行验收。
2. 同名且同地址时复用现有 ID，递归合并选项与模型，保留默认模型引用、旧模型和无关设置。不同地址的同名项、重复同名项、重复 JSON/YAML 键、非法文件和 MiniMax 块格式冲突会停止，不覆盖原文件。
3. 检查 MiniMax `config.yaml.lock`；JSON 使用自动配置专属锁。拒绝符号链接目标，写入前再次比较原文件内容，避免覆盖并发修改。MiniMax 自定义目录/profile 环境变量会触发手工处理提示。
4. 原配置备份为同目录的 `config.json.backup.<随机后缀>` 或 `config.yaml.backup.<随机后缀>`。临时文件和备份自创建起为 `0600`，刷新落盘后原子替换。POSIX 权限不能替代 Windows ACL，Windows 用户仍需限制配置目录访问。
5. 下载脚本用十六进制携带配置，防止内容进入 shell/Python 语法；**这不是加密**。文件预览遮蔽密钥，下载和复制的真实配置包含明文密钥。关闭面板或更改 Key、地址、名称、模型范围时清除旧结果。
6. Cursor/Roo/Cherry 手工步骤不额外读取 Key 明文；用户在 Key 管理中取得选定密钥后填入客户端。审计记录包含工具、系统、Key ID、模型数量和范围，不包含密钥值。

## 实测记录与边界

| 位置 | 当前证据 |
|---|---|
| 本机 ZCode、MiniMax Code、CC Switch | `itestu` 均有 28 个模型，Base URL 正确；分别使用配置中的 Key 顺序请求 `/models` 和 `minimax-m2.7` 聊天，均 HTTP 200，最终文本 OK。旧 `itestuu` 保留，CC Switch 的多余同名项已按用户选择清理，仅一条。配置检查和直接 API 请求不等同于应用 UI 验收。 |
| 本机 Cursor | 持久设置中的 `openAIBaseUrl` 正确，`useOpenAIKey=true`，Key 已保存。此前在 `doubao-seed-2-0-code` 的新 Ask/Agent 会话返回 OK；本次浏览器/原生控制工具超时，尚未重做 UI 请求。 |
| 192.168.31.40（原 .29，同一台主机） | ZCode/MiniMax 的 `itestuu` 配置各 28 模型、URL 正确；旧 provider 保留。网关部署和配置请求验证见下方审计报告。域名解析回自身 NetBird peer，直连 443 与经系统代理请求是不同路径，需分别检查。 |
| 192.168.31.30 | 前次配置和 API 验证成功。本次 SSH 主机密钥改变，等待可信终端指纹核实；不能将前次成功记作本轮复验通过。用户公钥不能代替 SSH 服务器主机密钥。 |

前次 ZCode UI 曾使用 `kaixuan/minimax-m2.7` 返回 OK，这只证明该 provider 的会话成功，不作为 `itestu` UI 验收证据。历史发布 2545 已由本轮审计发布替代。最新部署版本、请求和回归证据见 [2026-10-12 审计报告](../../audit/2026-10-12-client-provider-auto-config-audit.md)。

## 回滚与复验

- 客户端：退出配置编辑，确认备份内容后恢复到原路径并保持私有权限，再启动客户端检查原默认模型。
- 网关：通过标准部署脚本的 rollback 子命令选择保留的完整发布包；不要手工改活动指针或运行单个旧二进制。
- 先检查配置，再用该配置中的 Key 逐个验证 `/v1/models`、聊天和需要的工具调用。Cursor 必须另做 Ask/Agent UI 验证。
- SSH 使用 `huangxt@192.168.31.40`；252 使用配置别名 `ssh 252`。`.29` 是旧地址，NetBird peer 名仍可能包含 29。
- 遇到 `401 invalid_key` 需核对实际响应实例和配置中的原始 Key；Key 的状态或 `key_prefix` 元数据不能替代实际认证结果。不要以修改数据库 hash 作为探测手段。
