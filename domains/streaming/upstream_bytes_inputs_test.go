package streaming

// The unknown object deliberately combines every JSON value kind, an integer
// above float64's exact range, and escaped/Unicode strings. It is an independent
// semantic oracle: golden updates are never allowed to bless a rounded value.
const upstreamBytesExtras = `,"unknown_field":{"kept":[1,2,{"x":null}],"big":9007199254740993,"escaped":"é 漢字 😀 \\/ \\\"quote\\\"\n\ttab"},"unknown_array":[null,{"n":9007199254740993}],"unknown_null":null,"prompt_cache_key":"cache-prefix-unique","prompt_cache_retention":"24h"`

const upstreamBytesChat = `{"model":"m","messages":[{"role":"system","content":"system-prefix é"},{"role":"user","content":"read it"},{"role":"assistant","content":null,"tool_calls":[{"id":"call_1","type":"function","function":{"name":"Read","arguments":"{\"path\":\"/a b/漢字\",\"n\":9007199254740993}"}}]},{"role":"tool","tool_call_id":"call_1","content":"tool-result \\\"quoted\\\"\nline"},{"role":"user","content":"follow-up 😀"}],"tools":[{"type":"function","function":{"name":"Read","description":"Reads file","parameters":{"type":"object","properties":{"path":{"type":"string"},"n":{"type":"integer"}}}}}],"tool_choice":"auto","stream_options":{"include_usage":true},"reasoning_effort":"medium","max_completion_tokens":128,"response_format":{"type":"text"}` + upstreamBytesExtras + `}`

const upstreamBytesResponses = `{"model":"m","instructions":"system-prefix é","input":[{"type":"message","role":"user","content":"read it"},{"type":"function_call","call_id":"call_1","name":"Read","arguments":"{\"path\":\"/a b/漢字\",\"n\":9007199254740993}"},{"type":"function_call_output","call_id":"call_1","output":"tool-result \\\"quoted\\\"\nline"},{"type":"message","role":"user","content":"follow-up 😀"}],"tools":[{"type":"function","name":"Read","description":"Reads file","parameters":{"type":"object","properties":{"path":{"type":"string"},"n":{"type":"integer"}}}}],"tool_choice":"auto","stream_options":{"include_usage":true},"reasoning_effort":"medium","max_completion_tokens":128,"response_format":{"type":"text"}` + upstreamBytesExtras + `}`

const upstreamBytesMessages = `{"model":"m","max_tokens":128,"system":[{"type":"text","text":"system-prefix é","cache_control":{"type":"ephemeral"}}],"messages":[{"role":"user","content":"read it"},{"role":"assistant","content":[{"type":"tool_use","id":"call_1","name":"Read","input":{"path":"/a b/漢字","n":9007199254740993}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"call_1","content":"tool-result \\\"quoted\\\"\nline"},{"type":"text","text":"follow-up 😀"}]}],"tools":[{"name":"Read","description":"Reads file","input_schema":{"type":"object","properties":{"path":{"type":"string"},"n":{"type":"integer"}}}}]` + upstreamBytesExtras + `}`

const upstreamBytesChatReply = `{"id":"chat_1","object":"chat.completion","model":"m","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":7,"completion_tokens":1}}`
const upstreamBytesResponsesReply = `{"id":"resp_1","object":"response","model":"m","status":"completed","output":[{"id":"msg_1","type":"message","role":"assistant","content":[{"type":"output_text","text":"ok"}]}],"usage":{"input_tokens":7,"output_tokens":1}}`
const upstreamBytesMessagesReply = `{"id":"msg_1","type":"message","role":"assistant","model":"m","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":7,"output_tokens":1}}`

const upstreamBytesChatSSE = "data: {\"id\":\"chat_1\",\"object\":\"chat.completion.chunk\",\"model\":\"m\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"ok\"},\"finish_reason\":null}]}\n\n" +
	"data: {\"id\":\"chat_1\",\"object\":\"chat.completion.chunk\",\"model\":\"m\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":7,\"completion_tokens\":1}}\n\n" + "data: [DONE]\n\n"
const upstreamBytesResponsesSSE = "event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_1\",\"object\":\"response\",\"model\":\"m\",\"status\":\"in_progress\",\"output\":[]}}\n\n" +
	`event: response.output_item.added
` + `data: {"type":"response.output_item.added","output_index":0,"item":{"id":"msg_1","type":"message","role":"assistant","status":"in_progress","content":[]}}

` + `event: response.content_part.added
` + `data: {"type":"response.content_part.added","item_id":"msg_1","output_index":0,"content_index":0,"part":{"type":"output_text","text":"","annotations":[]}}

` + `event: response.output_text.delta
` + `data: {"type":"response.output_text.delta","item_id":"msg_1","output_index":0,"content_index":0,"delta":"ok"}

` + `event: response.output_text.done
` + `data: {"type":"response.output_text.done","item_id":"msg_1","output_index":0,"content_index":0,"text":"ok"}

` + `event: response.content_part.done
` + `data: {"type":"response.content_part.done","item_id":"msg_1","output_index":0,"content_index":0,"part":{"type":"output_text","text":"ok","annotations":[]}}

` + `event: response.output_item.done
` + `data: {"type":"response.output_item.done","output_index":0,"item":{"id":"msg_1","type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"ok","annotations":[]}]}}

` + "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":" + upstreamBytesResponsesReply + "}\n\n"

const upstreamBytesMessagesSSE = "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"m\",\"content\":[],\"usage\":{\"input_tokens\":7,\"output_tokens\":0}}}\n\n" +
	"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n" +
	"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"ok\"}}\n\n" +
	"event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n" +
	"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":1}}\n\n" + "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"

func upstreamBytesPath(p string) string {
	switch p {
	case "messages":
		return "/v1/messages"
	case "responses":
		return "/v1/responses"
	default:
		return "/v1/chat/completions"
	}
}
