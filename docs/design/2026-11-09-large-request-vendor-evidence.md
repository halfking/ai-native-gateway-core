# 100M 大请求方案：官方协议与实现证据

核查日期：2026-11-09（Asia/Shanghai）。用户已明确入口100MB，上游通常最多500K tokens且不超过桶模型上下文50%。本文件仅核查一手资料；不是已经实现或已经压测的声明。

## 1. Go：Reader 接口不等于有界解析

| 事实 | 一手来源 | 对方案的约束 |
| --- | --- | --- |
| `http.MaxBytesReader` 在读取越界时返回 `*MaxBytesError`；`io.LimitReader` 只限制读取范围，不能单独表达“请求超限” | [net/http.MaxBytesReader](https://pkg.go.dev/net/http#MaxBytesReader) | 入口应以实际读取量执行硬上限；不能只检查 Content-Length。解压输入还要单独限制解压后的量。 |
| `json.Decoder.Decode` 会先将整个 JSON value 读入内部缓冲，再反序列化；源码 `stream.go` 64–69 行 | [Go encoding/json 源码](https://go.dev/src/encoding/json/stream.go) | 把 `ReadAll+Unmarshal` 改成 `Decode(&wholeRequest)` 仍可能把整个100M对象保留在堆上。 |
| `Decoder.Token` 的标量路径仍调用 `Decode`（源码448–469行），字符串会作为完整 Go string 返回 | [同一官方源码](https://go.dev/src/encoding/json/stream.go) | 按消息逐个 Decode 只能将上界降到最大消息；要接收单条超大字符串、base64或tool结果，还需支持字符串分片的扫描器或受控外置机制。 |
| `io.CopyBuffer` 可以使用给定的缓冲区；若两端实现 `WriterTo/ReaderFrom` 则采用它们的实现 | [io.CopyBuffer](https://pkg.go.dev/io#CopyBuffer) | 可用有界复制完成落盘，但不能仅凭调用此API断言整个链路已达到固定内存上界；还需审查读写端和后续解析。 |

推论：先落盘再整文件 `os.ReadFile`、`Decode(&request)`、构造完整 IR，仍不满足“内存只保存局部”的目标。应验证解析、索引、摘要器、日志和缓存的每一条读回路径。

## 2. Nginx：入口上限、缓冲和持久会话目录是三件事

[核心模块官方文档](https://nginx.org/en/docs/http/ngx_http_core_module.html#client_body_buffer_size)分别定义：

- `client_max_body_size` 控制允许的请求体大小，超限返回413。
- `client_body_buffer_size` 控制读取缓冲；超出时可将全部或部分正文写入临时文件。
- `client_body_temp_path` 是请求临时文件目录。
- `client_body_in_file_only on` 会留下临时文件，`clean` 才在请求处理后清理。
- `client_body_timeout` 是连续两次读取之间的超时，并非整个上传的总时限。

[proxy_request_buffering 官方文档](https://nginx.org/en/docs/http/ngx_http_proxy_module.html#proxy_request_buffering)说明：开启时先接收整个请求，关闭时边收边发；已经发送请求体后，不能再把该请求转给 next upstream。chunked输入是否仍被缓冲还受代理HTTP版本影响。

推论：Nginx临时文件不能直接当会话原文的长期事实源。网关仍应创建自己的原子提交文件、会话清单及清理策略。若网关已负责落盘，可评估关闭代理请求缓冲以避免重复磁盘写入；必须验证真实部署拓扑及版本，不应直接套用到所有location。

## 3. 各供应商：字节上限不能替代token预算

| API/范围 | 本轮查到的事实 | 限制与适用范围 |
| --- | --- | --- |
| 首方 Claude API | 英文当前页列出 Messages API、Token Counting API均128 MB；Batch 256 MB；Files 500 MB；超限413 | [官方错误文档](https://platform.claude.com/docs/en/api/errors)。这是首方、分端点限制，不能直接用于Bedrock/Foundry/第三方兼容入口。旧资料的32 MB不应当作本轮现值。 |
| Claude 模型上下文 | system、messages、工具定义及当前输出/思考均占上下文；输入token分为普通、cache-read、cache-creation时，这三部分都占窗口 | [官方上下文文档](https://platform.claude.com/docs/en/build-with-claude/context-windows)。具体窗口按模型，不能从JSON字节数推出。签名thinking块存在协议完整性约束，不能任意改写。 |
| OpenAI Responses/Chat Completions | 上下文容量包含输入、输出及适用模型的reasoning；`previous_response_id`为会话状态机制，历史链仍涉及输入token计费 | [官方会话状态指南](https://developers.openai.com/api/docs/guides/conversation-state)。状态引用能改变传输方式，不代表历史上下文免费或无限。 |
| OpenAI file inputs | 文档规定每文件小于50 MB，单请求内所有文件合计50 MB；文件类型支持随API不同 | [官方文件输入指南](https://developers.openai.com/api/docs/guides/file-inputs)。这是文件输入限制，不是所有JSON推理请求的统一字节上限，也不是Files上传接口通用上限。 |
| Gemini 文件输入 | Google于2026-01-12宣布inline上限从20 MB提高到100 MB（base64编码，按数据类型有不同限制）；输入指南另列PDF等例外 | [Google官方公告](https://blog.google/innovation-and-ai/technology/developers-tools/gemini-api-new-file-limits/)、[官方输入方式指南](https://ai.google.dev/gemini-api/docs/file-input-methods)。不能继续无条件引用旧20 MB值，也不能把100 MB推广为每类媒体和每个API上限。 |

本轮未从所核查的OpenAI官方资料得到“所有JSON推理端点统一请求体字节上限”的证据；应以具体桶的模型、API、账号/平台和中间代理的已验证规格登记，未知则保守处理，不编造统一供应商上限。

## 4. 缓存的三种语义必须区分

| 机制 | 能否因此省略发送历史 | 能否因此绕过上下文上限 |
| --- | --- | --- |
| 网关本地L1/L2/L3 | 只缓存本地加工结果时，不能据此认定上游拥有历史 | 不能 |
| OpenAI/Claude prompt caching | 普通隐式/前缀缓存复用模型计算；请求仍需按对应API发送内容。不能只发本地hash | 不能 |
| Gemini explicit cachedContent、OpenAI previous_response_id等协议级引用 | 对支持且有作用域绑定的API可以减少后续请求传输；建立引用、过期恢复及跨桶切换另有成本 | 不能；对应历史/缓存内容仍参与上下文约束 |

证据：

- [OpenAI prompt caching](https://developers.openai.com/api/docs/guides/prompt-caching)：缓存复用相同渲染前缀的KV状态；改变此前内容或相关设置会影响命中。
- [Claude prompt caching](https://platform.claude.com/docs/en/build-with-claude/prompt-caching)：缓存前缀按tools→system→messages构成；跨轮示例仍携带历史内容；前缀中途改写影响后续哈希。
- [Gemini GenerateContent caching指南](https://ai.google.dev/gemini-api/docs/generate-content/caching)：explicit caching允许先建立缓存再以名称引用；cached tokens仍计入token上限。
- [Gemini缓存API](https://ai.google.dev/api/caching)：缓存内容只能用于建立该缓存时指定的模型。

Google缓存页本轮网页直接打开遇到`text/markdown`抓取错误；上面缓存事实由同一官方页面的搜索索引正文及官方缓存API交叉核查，未做实时API请求。

## 5. 压缩能力的可行边界

1. **存储压缩**（例如zstd/gzip）缩小落盘字节；不能因此声称减少模型上下文。
2. **HTTP Content-Encoding**缩小编码表示；接收端仍需解码成原媒体类型。供应商是否接受请求压缩必须逐API验证；客户端请求的`Accept-Encoding`主要声明可接收的响应编码，不能当作服务端支持压缩请求的证明。[RFC 9110 §8.4、§12.5.3](https://www.rfc-editor.org/rfc/rfc9110.html#section-8.4)
3. **语义压缩/检索**才减少模型实际看到的内容；这是有损选择，需要保留原文、引用定位和质量门。
4. 任意100M输入无法保证无损变成500K；高熵或全量精确任务尤其不成立。这是容量/信息量推论，不是供应商API承诺。无法在预算内保留任务必要内容时，应给出明确不可压缩结果，或进入用户可感知的分块任务模式。
5. 摘要器本身也是一次LLM供应商请求。分块摘要、合并摘要、重试、缓存建立和最终生成都必须各自通过字节与token门；不能把原始100M交给摘要模型后宣称“最终调用小于500K”。

建议采用两个独立判据：**最终适配完成、Content-Encoding编码前JSON实际字节数**与**目标桶模型的总上下文token预算**。用户确认500K为token，因此另设独立字节门，不将它误当500KB。50%为运营安全策略，不是上述官方API的共同要求。
