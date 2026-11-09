package sessioncontext

import "encoding/json"

type messageInfo struct {
	role        string
	calls, refs []string
}

func describeMessage(raw json.RawMessage) messageInfo {
	var v map[string]any
	_ = json.Unmarshal(raw, &v)
	info := messageInfo{}
	info.role, _ = v["role"].(string)
	var scan func(any)
	scan = func(v any) {
		switch x := v.(type) {
		case []any:
			for _, item := range x {
				scan(item)
			}
		case map[string]any:
			str := func(key string) string { s, _ := x[key].(string); return s }
			switch str("type") {
			case "tool_use":
				info.calls = append(info.calls, "id:"+str("id"))
			case "tool_result":
				info.refs = append(info.refs, "id:"+str("tool_use_id"))
			case "function_call":
				info.calls = append(info.calls, "id:"+str("call_id"))
			case "function_call_output":
				info.refs = append(info.refs, "id:"+str("call_id"))
			}
			if id := str("tool_call_id"); id != "" {
				info.refs = append(info.refs, "id:"+id)
			}
			if calls, ok := x["tool_calls"].([]any); ok {
				for _, c := range calls {
					if m, ok := c.(map[string]any); ok {
						if id, ok := m["id"].(string); ok {
							info.calls = append(info.calls, "id:"+id)
						}
					}
				}
			}
			if c, ok := x["functionCall"].(map[string]any); ok {
				if name, ok := c["name"].(string); ok {
					info.calls = append(info.calls, "name:"+name)
				}
			}
			if c, ok := x["functionResponse"].(map[string]any); ok {
				if name, ok := c["name"].(string); ok {
					info.refs = append(info.refs, "name:"+name)
				}
			}
			for _, key := range []string{"content", "parts"} {
				if c, ok := x[key]; ok {
					scan(c)
				}
			}
		}
	}
	scan(v)
	return info
}
func protectedMessages(msgs []json.RawMessage) ([]bool, int) {
	info := make([]messageInfo, len(msgs))
	for i, m := range msgs {
		info[i] = describeMessage(m)
	}
	return protectInfo(info)
}

// Tool dependencies form an undirected graph: retaining a result keeps its
// call, and retaining a multi-call assistant message keeps all its results.
func protectInfo(info []messageInfo) ([]bool, int) {
	keep := make([]bool, len(info))
	edges := make([][]int, len(info))
	calls := map[string]int{}
	first, tail, human := -1, -1, -1
	for i, m := range info {
		if m.role == "system" || m.role == "developer" {
			keep[i] = true
		}
		if m.role == "user" {
			tail = i
			if len(m.refs) == 0 {
				human = i
				if first < 0 {
					first = i
				}
			}
		}
		for _, key := range m.calls {
			calls[key] = i
		}
		for _, key := range m.refs {
			if j, ok := calls[key]; ok {
				edges[i] = append(edges[i], j)
				edges[j] = append(edges[j], i)
			}
		}
	}
	if first >= 0 {
		keep[first] = true
	}
	if human >= 0 {
		keep[human] = true
	}
	if tail < 0 {
		tail = len(info) - 1
	}
	for i := tail; i < len(info); i++ {
		keep[i] = true
	}
	queue := make([]int, 0, len(info))
	for i, k := range keep {
		if k {
			queue = append(queue, i)
		}
	}
	for len(queue) > 0 {
		i := queue[0]
		queue = queue[1:]
		for _, j := range edges[i] {
			if !keep[j] {
				keep[j] = true
				queue = append(queue, j)
			}
			if j < tail && len(info[j].calls) > 0 {
				tail = j
			}
		}
	}
	return keep, tail
}
