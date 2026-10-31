package main

// explain.go —— EXPLAIN (FORMAT JSON) 的读数抽取 + 参数内联。
//
// # 为什么参数要内联而不是走绑定参数
//
// `EXPLAIN` 是一条**独立语句**，它的参数占位符不能引用外层语句的 $1…
// ⇒ 而 pgx 的 `Query(ctx, sql, args...)` 会把 sql 里的第一个 $1 当成**它自己的**参数
// ⇒ ⇒ 「EXPLAIN … WHERE ts >= $1」这种写法在 pgx 里根本没法传参。
//
// ⇒ 所以本工具把 spec 里给好的实参**内联成 SQL 字面量**再跑 EXPLAIN。
// ⇒ ★ 好处：每次测量用的参数是**固定且可复核的**（spec 里逐条写着），
//   不像「每次现算一个 now()」那样两次读数不可比。
// ⇒ ★ 代价：参数必须是**字面量安全**的（内联会做引号转义；转义不了的值就别放进 spec）。

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

// explainPayload 是 EXPLAIN (FORMAT JSON) 的顶层结构。
type explainPayload struct {
	Plan       explainNode `json:"Plan"`
	PlanningMs float64     `json:"Planning Time"`
	ExecMs     float64     `json:"Execution Time"`
}

// explainNode 是 plan 树的一个节点。
type explainNode struct {
	NodeType           string        `json:"Node Type"`
	RelationName       string        `json:"Relation Name"`
	ParentRelationship string        `json:"Parent Relationship"`
	ActualRows         int64         `json:"Actual Rows"`
	ActualTotalTime    float64       `json:"Actual Total Time"`
	PlanRows           int64         `json:"Plan Rows"`
	SubplansRemoved    int64         `json:"Subplans Removed"`
	SharedHitBlocks    int64         `json:"Shared Hit Blocks"`
	SharedReadBlocks   int64         `json:"Shared Read Blocks"`
	Plans              []explainNode `json:"Plans"`
}

// parseExplain 把 EXPLAIN JSON 填进读数。
func parseExplain(r *VariantReading, payload []byte) {
	// ★ EXPLAIN (FORMAT JSON) 返回的是**数组**（外层是 [ {Plan...} ]）⇒
	//   解到 []explainPayload，不是单对象。
	var ps []explainPayload
	if err := json.Unmarshal(payload, &ps); err != nil {
		r.Err = "解析 EXPLAIN JSON 失败: " + err.Error()
		return
	}
	if len(ps) == 0 {
		r.Err = "EXPLAIN JSON 是空数组"
		return
	}
	p := ps[0]
	r.OK = true
	r.PlanningMs = p.PlanningMs
	r.ExecutionMs = p.ExecMs

	parts := map[string]bool{}
	// ★★★ buffers **只能取根节点的**，不能把所有节点相加：
	//   PG 在每个节点的 "Buffers:" 行里给出的是**该节点及其所有子节点**的合计
	//   ⇒ ⇒ 把每个节点都加一遍 = **把同一批块重复计入**（★ 实测虚高 7 倍：
	//   ★ 252 上真实 252,246 块被算成 1,796,812 块）。
	//   ⇒ ⇒ 权威口径 = **根节点那一个数**（★ 含并行 worker 的块）。
	//
	// ★ 副作用（★ 要知道）：⇒ 「B 少扫了哪些关系」那个直接证据仍来自节点遍历，
	//   不受影响；⇒ 但凡引用过旧口径的**绝对值**都要改。
	rootHit, rootRead := p.Plan.SharedHitBlocks, p.Plan.SharedReadBlocks
	var walk func(n explainNode)
	walk = func(n explainNode) {
		if n.RelationName != "" {
			parts[n.RelationName] = true
		}
		r.SubplansRemoved += n.SubplansRemoved
		for _, c := range n.Plans {
			walk(c)
		}
	}
	walk(p.Plan)
	r.SharedHitBlocks = rootHit
	r.SharedReadBlocks = rootRead
	r.ActualRows = p.Plan.ActualRows

	names := make([]string, 0, len(parts))
	for n := range parts {
		names = append(names, n)
	}
	sort.Strings(names)
	r.PartitionNames = strings.Join(names, ",")
	r.PartitionsScanned = len(names)
}

// inlineParams 把 SQL 里的 $1..$n 换成字面量。
//
// ★ **必须跳过字符串字面量内部的 $**（否则 'a $1 b' 这种会被改坏）。
func inlineParams(sql string, params []any) string {
	if len(params) == 0 {
		return sql
	}
	var b strings.Builder
	b.Grow(len(sql) + 32)
	inSingle := false
	for i := 0; i < len(sql); {
		c := sql[i]
		if inSingle {
			b.WriteByte(c)
			if c == '\'' {
				// '' 是转义的单引号
				if i+1 < len(sql) && sql[i+1] == '\'' {
					b.WriteByte(sql[i+1])
					i += 2
					continue
				}
				inSingle = false
			}
			i++
			continue
		}
		if c == '\'' {
			inSingle = true
			b.WriteByte(c)
			i++
			continue
		}
		if c == '$' && i+1 < len(sql) && sql[i+1] >= '1' && sql[i+1] <= '9' {
			j := i + 1
			for j < len(sql) && sql[j] >= '0' && sql[j] <= '9' {
				j++
			}
			idx, _ := strconv.Atoi(sql[i+1 : j])
			if idx >= 1 && idx <= len(params) {
				b.WriteString(sqlLiteral(params[idx-1]))
				i = j
				continue
			}
		}
		b.WriteByte(c)
		i++
	}
	return b.String()
}

// sqlLiteral 把一个实参转成 SQL 字面量。
func sqlLiteral(v any) string {
	switch t := v.(type) {
	case nil:
		return "NULL"
	case string:
		return "'" + strings.ReplaceAll(t, "'", "''") + "'"
	case bool:
		if t {
			return "TRUE"
		}
		return "FALSE"
	case int:
		return strconv.Itoa(t)
	case int64:
		return strconv.FormatInt(t, 10)
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	case time.Time:
		return "'" + t.UTC().Format(time.RFC3339Nano) + "'::timestamptz"
	case fmt.Stringer:
		return "'" + strings.ReplaceAll(t.String(), "'", "''") + "'"
	default:
		return "'" + strings.ReplaceAll(fmt.Sprintf("%v", v), "'", "''") + "'"
	}
}
