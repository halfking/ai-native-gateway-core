// responses_durable_verdict.go — 持久 Responses 结论的解析（2026-10-02）。
//
// 背景：native Responses 的能力位有两个存储面。
//
//	· Redis node-state（llmgw:cred_fp_node:<cred>:<model> 的 capabilities）——
//	  请求期闸门读的那一份，TTL 3600s，由探针与实时请求写入；
//	· SQL credential_model_capabilities —— 绑定级持久行，由
//	  bg/capability_backfill 回填，routing 期由 provider/client.go 投影成
//	  cand.SupportsNativeResponses / cand.SupportsNativeResponsesKnown。
//
// 此前两个闸门在**读错误**时都走同一条路：记一条 debug 日志，然后按「无结论」
// 处理。第三轮审计已经把这个失效形态钉死过一次——`slide_window:{}` 的解码
// bug 让 GetSupportsResponses 对每个节点都报错，能力位「一直在正确写入、
// 从未被读回」，两个方向同时静默失效。
//
// 这里的解析规则是：**读错误不是默认值**。Redis 读不出来时回落 SQL 侧的持久
// 结论，而不是回落「无结论」。
//
// 一处必须说清的限界（否则这段注释就是误导）：在今天两个闸门的前置条件下，
// 「回落 SQL」与「无结论」在路由结果上**恰好一致**——
//
//	· 闸门 1（降级）只在 nativeNonStream || nativeStream 时进入，而
//	  nativeNonStream 由 cand.SupportsNativeResponses 推出 ⇒ SQL 必为 true ⇒
//	  回落 true 与不动，结论同为「不降级」；
//	· 闸门 2（开闸）只在 !nativeNonStream && !nativeStream 时进入 ⇒ SQL 必为
//	  false ⇒ 回落 false 与不动，结论同为「不开」。
//
// 所以本文件目前是**契约**而不是行为差：它把「读错误 ⇒ 查另一个存储面」写成
// 一条可测的判据，而不是依赖两个存储面碰巧一致这一隐式事实。它承重的场景是
// ① Redis 键丢失/重启后闸门不再静默失效，② 后续任何放宽闸门前置条件的改动
// （例如让持久结论无视 SQL 标志开闸）自动获得正确的降级来源。
package executors

// durableVerdict 是一次持久结论读取的结果。ReadErr 与 Known 是三态的两半：
// 「读不出来」和「读出来但没有结论」不是同一件事。
type durableVerdict struct {
	Supported bool
	Known     bool
	ReadErr   error
}

// resolveDurableResponsesVerdict 合成最终结论。
//
//	读错误        → SQL 持久结论（degraded=true）
//	Redis 有结论  → Redis 结论
//	Redis 无结论  → SQL 持久结论（键从未写入 / TTL 过期 / 键丢失）
//
// 返回 degraded=true 表示最终结论来自 SQL 而非 Redis，调用方据此把它抬到
// warn 级别 —— 一个读不出来却仍在按持久结论路由的节点，是需要被人看见的。
func resolveDurableResponsesVerdict(
	read durableVerdict,
	sqlSupported, sqlKnown bool,
) (supported, known, degraded bool) {
	if read.ReadErr != nil {
		return sqlSupported, sqlKnown, true
	}
	if read.Known {
		return read.Supported, true, false
	}
	return sqlSupported, sqlKnown, false
}
