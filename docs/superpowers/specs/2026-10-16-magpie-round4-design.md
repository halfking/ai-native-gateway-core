# Magpie 第四轮：工具契约与错误外显设计

用户已授权按顺序执行、并行子代理、提交合并推送、本地部署和完整验收；沿用此前研究中的P1-C/D/F方向。
本设计将该方向具体化，不另要求同主题审批。baseline为`39c2d6428ad0a35be293e5f7c532826140cf3d97`，
参考Magpie为`591ed58b536b17162a0715aa7210d9b7d0db4c09`；本地起始2661/76e185e7、active8782。

## 选择与边界

采用既有执行器/IR/协议错误出口的局部扩展。仅补末端不能恢复IR已删的工具结果；全量重新序列化又会破坏原字节，
故选择“共享纯配对内核+raw适配+必要IR修正”。offered名称从真实请求工具定义重建，挂在请求scope context，
不新增持久化字段，不从模型输出、历史调用或tool_ids字符串授予权限。错误渲染复用errorsx和实际dispatch失败证据，
不引入第二套处罚/重试政策。无数据库迁移、产品UI改版或154/245部署。

## C：散文工具调用只在明确offer与完整语法下纠正

1. 从Chat function.name及Messages/Responses的真实工具定义提取不可变名称集，精确、区分大小写匹配。
   没有有效定义、未知名、无名、非法参数、未闭合结构均保留原文本，不合成固定name=tool。
2. 原结构化tool_calls不经此纠正；失败MiniMax工具候选解包保持原文，后置vendorstrip也不能再次破坏它；完整已知XML/Hermes调用保留真实参数与周边文本。普通非工具vendor清理和reasoning清理仍以既有契约为准，不能无证据整段撤除。
3. 流式按choice隔离缓冲；DONE、finish、EOF及overflow时释放未完成的原文本，不能静默吞字。
   总hold限64KiB、活跃choice限64；超界只在本响应保留文本并停止新增纠正。原始物理行复用既有SSE配置上限，不能在后置读行器之前无限缓冲；合法待释放文本按UTF-8分成小帧。取消/Close正常传播，非终态调用帧不制造finish。
4. 新WithNames API作为唯一可授权纠正点，旧bool-only API安全no-op；全量生产调用迁移，不能留下bool绕过。
   非流hook带context；streamReaderContext合并实际上游reader的取消语义和本次offer。
5. 三客户端入口到既有Chat-shaped纠正点的流/非流均验证。原生Anthropic不新增XML纠正，能力边界如实记录。

## D：缺失结果按调用顺序补齐，保留真实历史

1. 共享planner按本轮call batch匹配，下一轮同ID不替前轮消债；连续assistant calls可属同批，结果开始后的新assistant关闭旧批。
2. partial batch按call顺序输出已有真实result或明确interrupted synthetic；已有空result也是已回答，不能伪造成功。
   Messages synthetic带is_error=true，IR往返保持此属性。
3. 混合user文本、image与resize notice等内容保留彼此顺序，排在该批结果之后；不能遇user就删除其后的有效结果。
4. 完整、相邻、有效的exchange（含可被协议接受的结果乱序）原buffer byte no-op；需要修复时仅替换messages/input数组，
   原call/result的未知字段、精确整数及转义以RawMessage保存。再次repair幂等。
5. 同批重复call ID或重复匹配答案无法无歧义配对时，typed ErrAmbiguousHistory拒绝请求，不静默删call、不改名洗白；
   无call/非法JSON交回既有验证，不抢Response previous_response_id片段的孤儿结果语义。
6. 既有IR清理器必须在其删除点前修正。Chat/native Responses首发、Chat转Responses重试和Messages最终出站都要接到，
   不能仅靠转换器单测代表真实发送。既有34份完整黄金不更新即通过。

## F：三入口保留同一失败事实，各自按协议输出

前两项实现并验证后再改本项。追到typed upstream/dispatch证据，避免在exhausted阶段把context/auth/client类归成transient。
相同失败在Chat、Messages、Responses的code/status/重试真假一致，外壳、SSE事件和终态遵循对应协议。
上下文超长使用context_length_exceeded，真实模型不存在与普通上游故障分开；鉴权、内容拒绝、请求形状不提示无效重试。
限流/配额/恢复等待使用有界Retry-After，优先实际上游/最早恢复证据；X-Should-Retry与最终归责一致。
头已提交的流不能再声称改了HTTP状态或header，改由明确失败终态携带code、retryable、retry_after_seconds。
survival和普通exhausted出口共用同一语义决策，已有attempts/等待预算/取消语义保留；不把请求错误拿去罚节点。

### 客户端重试投影与恢复hint

普通与survival最终均消费FinalAttempt真实kind的既有客户端投影：rate保留既有Chat429的true特例，其他族复用EffectiveRetryable；连接内TaskAction/IsRetryable仍保留原恢复政策，不能把同候选即时重放与客户端稍后新请求混为一谈。
client cancel/未知fail-closed为false；survival context的主code必须是context_length_exceeded，旧gateway_survival_*保留为survival_code。
调用者key错误401与上游凭据错误502分开；context400（不受上游413/422漂移），独立body字节上限仍413。
quotaHeld保留429、真实已尝试quota保留既有502；按已有普通策略不让SDK立即热重试，但可附真实reset的RA。
rate429、concurrent/overload503、timeout504、network/down502沿用errorsx语义；未知模型与已知模型暂时无候选分别外显。
客户端恢复hint向上取整并限[1,7200]秒，不能复用10秒in-flight sleep夹值。仅聚合同一终态族的真实提示，
跨候选最早恢复必须由实际attempt元数据携带；每候选以最新观测覆盖旧记录并按真实时间老化，不能用最后一个hint冒称全体最早。最新同族候选已过期或无hint时，不得忽略它而宣称另一未来reset为全体最早；走明确标注的默认backoff。新异族观测使该候选旧同族记录失效。真实recovery/upstream证据优先于陈旧响应预置头，精确admission backoff正控保留。无真实hint的可恢复临时族默认5秒、routing默认30秒，
须标明是backoff而非reset；不可恢复且无真实hint不凭空写RA。头已提交仅体提示，用真实Response.Result头判据防假绿。
early shared-key rate三入口统一429并保留scope，Anthropic invalid_request规范为invalid_request_error，Responses仅已知参数才填param。
真实handler负控覆盖上述分支与已渲染terminal sentinel，不能仅以一个context修补代替三入口归一。

## 证据与发布

每项先真实红测再最小修复，独立规格审查后代码/Go审查；去保护变异需在正确语义断言红，编译错不算。
完整门使用Go1.27.2、SDK26.5、GOMAXPROCS=1/GOFLAGS=-p=1；installer独立模块另跑，含真实Redis/PG与两前端。
新HTTP判据在真实handler→dispatch→Executor→HTTP边界证明offer、调用、终态和顺序均在场。
本地发布固定审查SHA，核active-port/bin-current/version/readyz/healthz，执行实际协议请求与browser-use。
保留并发main改动，完整inventory与Task Stop Summary验证后小步commit→no-ff merge→push；部署前保存一行回滚。
已知ACC token缺失，不能声称session.append/pin/archive成功；仓内审计和私有证据保留实际结果。
