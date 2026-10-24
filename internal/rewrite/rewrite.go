// Package rewrite —— **按候选改写的类型化契约**（P1-4）。
//
// # 这道改造补的是哪块缺口
//
// 计划书（docs/research/2026-10-24-magpie-v0.1.1367-…md §5.2 / §6-P1-4）指出：
// magpie 支持「**按 provider 改写**」，但那是**进程内脚本引擎**（moejs/goja 类）；
// 本仓**不走那条路** —— 计划书给的建议是 (b)：
//
//	「把『按 provider 的改写』**下沉为 Go 侧的类型化改写点**（不是脚本）」。
//
// ★ 而本仓**已经有**这条管道：`domains/streaming/executors/executor_chat.go` 的
// `prepareRequestBody(params, cand, ledger...)` —— 它**逐候选**成型请求体
// （换模型名 → 注入 stream_options → 白名单 → 折叠 tool 历史 → 能力消毒 →
// 合并连续消息 → 上下文压缩）⇒ ★ **那就是「按候选改写」唯一的收口点**。
//
// ⇒ 本包把「改写」这件事**定成契约**（而不是再造第二套机制）：
//
//	① **选择器**：`RewritePoint{Point, Provider, Model}` —— 在哪个位置、对哪个候选；
//	② **只允许改一个顶层字段**，且**字节保真**（目标字段之外的字节**逐字不动**）；
//	③ **失败策略** + **超时**：失败 ⇒ 默认**原样发出**（fail-open）；
//	④ **逐候选隔离**：同一请求的不同候选互不污染。
//
// # 为什么「只允许改一个顶层字段」是硬约束
//
// 若允许「重新序列化整个 JSON」，那么**键序、空格、数字字面量**都会变
// ⇒ 上游看到的字节与客户端发出的字节不再逐字对应
// ⇒ 「字节保真」这条契约就无从谈起（也无法证伪）。
// ⇒ 因此本包的实现是**外科式字节替换**：定位目标字段的**值区间**，
// 只替换那一段，其余字节**原样拼接** ⇒ 字节保真是**结构性的**，不是事后校验的。
//
// # 为什么「失败 ⇒ 原样发出」是默认
//
// 改写是**增强**，不是**闸门**。改写失败（超时、字段不存在、值不合法）时，
// 把一个**本来能成功**的请求打死是更坏的结果
// ⇒ 默认 fail-open（原样发出）；确需 fail-closed 的场景由 `Spec.OnFailure` 显式声明。
//
// # 诚实边界
//
// ★ 本包是**契约 + 参考实现**，**尚未接线到 `prepareRequestBody`**：
// 计划书 P1-4 的措辞是「把 (b) 路径**定成接口**」⇒ 契约先定死，
// 接线与「规则从哪来（配置面）」属后续。
package rewrite

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// Point 是请求体改写管道上的一个**位置**。
type Point string

const (
	// PointAfterModel 在「模型名已按候选替换」之后。
	// ★ 这是本仓管道上第一个「已经知道最终候选身份」的位置
	// （`prepareRequestBody` 的第一步就是按候选换模型名）。
	PointAfterModel Point = "after_model"
	// PointBeforeSend 在「即将发给上游之前」。
	// ★ 这是最后一个位置 —— 此时的请求体已经过白名单/消毒/压缩，
	// 改写它意味着改写的是**真正会发出去的那份**。
	PointBeforeSend Point = "before_send"
)

// FailurePolicy 是改写失败时的策略。
type FailurePolicy string

const (
	// PolicyFailOpen：失败 ⇒ **原样发出**（默认）。
	PolicyFailOpen FailurePolicy = "open"
	// PolicyFailClosed：失败 ⇒ 返回错误，请求**不发出**。
	PolicyFailClosed FailurePolicy = "closed"
)

// DefaultTimeout 是未显式声明时的改写超时。
const DefaultTimeout = 2 * time.Second

// RewritePoint 选出「在哪个位置、对哪个候选」生效。
//
// ★ Provider / Model **留空 = 匹配任意** ⇒ 选择器是「逐维收窄」的：
// 只填 Provider 就是「所有该厂商的模型」，只填 Model 就是「所有厂商的该模型」。
type RewritePoint struct {
	// Point 是管道位置（空 = 任意位置）。
	Point Point
	// Provider 是候选的厂商标识（空 = 任意）。
	Provider string
	// Model 是候选的模型标识（空 = 任意）。
	Model string
}

// Candidate 是改写时能看到的候选身份（**最小面** —— 只给改写需要的）。
type Candidate struct {
	// Provider 是候选的厂商标识。
	Provider string
	// Model 是候选的模型标识。
	Model string
}

// Spec 是一条改写规则：**只允许改一个顶层字段**。
type Spec struct {
	// RewritePoint 决定这条规则对谁、在哪生效。
	RewritePoint
	// Field 是要改的**顶层** JSON 字段名（如 "model" / "temperature"）。
	// ★ 空 ⇒ 规则无效（见 `Spec.Validate`）。
	Field string
	// Value 是新的字段值（**原样写入**，不做规范化 —— 保真）。
	Value json.RawMessage
	// Timeout 是这条规则的独立超时（0 ⇒ DefaultTimeout）。
	Timeout time.Duration
	// OnFailure 是失败策略（空 ⇒ PolicyFailOpen）。
	OnFailure FailurePolicy
}

// Validate 报告这条规则本身是否合法。
//
// ★ 「合法」的定义刻意很窄：**只允许改一个顶层字段** ⇒ Field 必须是**单个**
// 顶层键名（不含路径分隔符、不含引号），否则规则会被拒绝而不是「尽力而为」。
func (s Spec) Validate() error {
	if s.Field == "" {
		return errors.New("rewrite: Field 不能为空（改写只允许改一个顶层字段）")
	}
	if bytes.ContainsAny([]byte(s.Field), ".\"/\\") {
		return fmt.Errorf("rewrite: Field %q 不是单个顶层字段名（不支持路径）", s.Field)
	}
	switch s.OnFailure {
	case "", PolicyFailOpen, PolicyFailClosed:
	default:
		return fmt.Errorf("rewrite: 未知失败策略 %q", s.OnFailure)
	}
	if s.Timeout < 0 {
		return fmt.Errorf("rewrite: Timeout 不能为负（%v）", s.Timeout)
	}
	if len(s.Value) == 0 {
		return errors.New("rewrite: Value 不能为空")
	}
	if !json.Valid(s.Value) {
		return fmt.Errorf("rewrite: Value 不是合法 JSON：%q", string(s.Value))
	}
	return nil
}

// Matches 报告这条规则是否对给定候选与位置生效。
func (s Spec) Matches(point Point, cand Candidate) bool {
	if s.Point != "" && s.Point != point {
		return false
	}
	if s.Provider != "" && s.Provider != cand.Provider {
		return false
	}
	if s.Model != "" && s.Model != cand.Model {
		return false
	}
	return true
}

// failurePolicy 返回该规则的实际失败策略（默认 fail-open）。
func (s Spec) failurePolicy() FailurePolicy {
	if s.OnFailure == "" {
		return PolicyFailOpen
	}
	return s.OnFailure
}

// timeout 返回该规则的实际超时（默认 DefaultTimeout）。
func (s Spec) timeout() time.Duration {
	if s.Timeout <= 0 {
		return DefaultTimeout
	}
	return s.Timeout
}

// ErrRewriteFailed 是一个改写失败的错误（fail-closed 路径会返回它）。
var ErrRewriteFailed = errors.New("rewrite: 改写失败")

// testDelayHook 是**仅供测试**的注入点（见 `applyOne`）。
//
// ★ 它是包级可变状态 ⇒ **测试之间不得并行**（本包无 t.Parallel）。
var testDelayHook func()

// Apply 在 body 上应用所有匹配的规则，返回**最终**请求体。
//
// ★ 语义：
//
//	① 逐条按声明顺序应用匹配的规则（每条都作用于**上一条的输出**）；
//	② 某条失败 ⇒ 按该条的 `OnFailure`：
//	   - fail-open（默认）⇒ **保留该条之前的字节**（原样发出），继续下一条；
//	   - fail-closed ⇒ 返回 `ErrRewriteFailed` 包裹的错误；
//	③ **纯函数**：不改入参、不持有可变状态 ⇒ 同一请求的不同候选互不污染。
//
// ★ 超时的语义：每条规则**独立**计时（`Spec.Timeout`），
// 超时按失败处理（⇒ fail-open 时原样发出）。
func Apply(ctx context.Context, point Point, body []byte, cand Candidate, specs []Spec) ([]byte, error) {
	out := body
	for i, spec := range specs {
		if !spec.Matches(point, cand) {
			continue
		}
		next, err := applyOne(ctx, out, spec)
		if err != nil {
			if spec.failurePolicy() == PolicyFailClosed {
				return nil, fmt.Errorf("%w: 第 %d 条规则（field=%q provider=%q model=%q）：%v",
					ErrRewriteFailed, i+1, spec.Field, spec.Provider, spec.Model, err)
			}
			// ★ fail-open：**保留该条之前的字节**（不是丢弃整个改写链的结果）。
			continue
		}
		out = next
	}
	return out, nil
}

// applyOne 执行单条规则（只改目标字段的**值区间**）。
func applyOne(ctx context.Context, body []byte, spec Spec) ([]byte, error) {
	if err := spec.Validate(); err != nil {
		return nil, err
	}
	// ★ 每条规则独立计时；ctx 已取消 ⇒ 立刻失败。
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	timeout := spec.timeout()
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	done := make(chan []byte, 1)
	errc := make(chan error, 1)
	go func() {
		// ★ 仅供测试的注入点：让测试能**确定性地**触发超时路径
		// （否则「超时」只能用 1ns 赌运气，而赌运气的门 = 假绿的门）。
		// 生产路径上它恒为 nil ⇒ 只是一次 nil 检查。
		if testDelayHook != nil {
			testDelayHook()
		}
		start, end, ok := locateTopLevelField(body, spec.Field)
		if !ok {
			errc <- fmt.Errorf("顶层字段 %q 不存在", spec.Field)
			return
		}
		var buf bytes.Buffer
		buf.Grow(len(body) - (end - start) + len(spec.Value))
		buf.Write(body[:start])
		buf.Write(spec.Value)
		buf.Write(body[end:])
		done <- buf.Bytes()
	}()

	select {
	case <-ctx.Done():
		return nil, fmt.Errorf("改写超时（%v）：%v", timeout, ctx.Err())
	case err := <-errc:
		return nil, err
	case out := <-done:
		return out, nil
	}
}

// locateTopLevelField 返回 body 的**顶层对象**里 field 的「值」字节区间 [start,end)。
//
// ★ 它**不**做 JSON 反序列化再重新序列化 ⇒ 因此返回的区间是**原字节**的区间
// ⇒ 替换它就能保证其余字节逐字不动。
//
// ★ 只看**顶层**：嵌套对象里的同名字段不算数（那不是「顶层字段」）。
func locateTopLevelField(body []byte, field string) (int, int, bool) {
	i := skipWS(body, 0)
	if i >= len(body) || body[i] != '{' {
		return 0, 0, false
	}
	i++
	for {
		i = skipWS(body, i)
		if i >= len(body) {
			return 0, 0, false
		}
		if body[i] == '}' {
			return 0, 0, false // 顶层对象结束 ⇒ 没有这个字段
		}
		if body[i] != '"' {
			return 0, 0, false // 形态不对 ⇒ 不猜
		}
		key, next, ok := scanString(body, i)
		if !ok {
			return 0, 0, false
		}
		i = skipWS(body, next)
		if i >= len(body) || body[i] != ':' {
			return 0, 0, false
		}
		i = skipWS(body, i+1)
		valStart := i
		valEnd, ok := scanValue(body, i)
		if !ok {
			return 0, 0, false
		}
		if key == field {
			return valStart, valEnd, true
		}
		i = skipWS(body, valEnd)
		if i < len(body) && body[i] == ',' {
			i++
			continue
		}
		if i < len(body) && body[i] == '}' {
			return 0, 0, false
		}
		if i >= len(body) {
			return 0, 0, false
		}
	}
}

// skipWS 跳过空白。
func skipWS(b []byte, i int) int {
	for i < len(b) {
		switch b[i] {
		case ' ', '\t', '\n', '\r':
			i++
		default:
			return i
		}
	}
	return i
}

// scanString 从 b[i]（必须是 '"'）开始读一个 JSON 字符串，返回解出的内容与结束后的下标。
//
// ★ 处理转义（\" \\ \/ \b \f \n \r \t \uXXXX）⇒ 键名里的 \" 不会提前终止字符串。
func scanString(b []byte, i int) (string, int, bool) {
	if i >= len(b) || b[i] != '"' {
		return "", i, false
	}
	i++
	var sb bytes.Buffer
	for i < len(b) {
		c := b[i]
		switch c {
		case '\\':
			if i+1 >= len(b) {
				return "", i, false
			}
			e := b[i+1]
			switch e {
			case '"', '\\', '/':
				sb.WriteByte(e)
				i += 2
			case 'b':
				sb.WriteByte('\b')
				i += 2
			case 'f':
				sb.WriteByte('\f')
				i += 2
			case 'n':
				sb.WriteByte('\n')
				i += 2
			case 'r':
				sb.WriteByte('\r')
				i += 2
			case 't':
				sb.WriteByte('\t')
				i += 2
			case 'u':
				if i+6 > len(b) {
					return "", i, false
				}
				var r rune
				for k := 0; k < 4; k++ {
					h := b[i+2+k]
					var v rune
					switch {
					case h >= '0' && h <= '9':
						v = rune(h - '0')
					case h >= 'a' && h <= 'f':
						v = rune(h-'a') + 10
					case h >= 'A' && h <= 'F':
						v = rune(h-'A') + 10
					default:
						return "", i, false
					}
					r = r<<4 | v
				}
				sb.WriteRune(r)
				i += 6
			default:
				return "", i, false
			}
		case '"':
			return sb.String(), i + 1, true
		default:
			sb.WriteByte(c)
			i++
		}
	}
	return "", i, false
}

// scanValue 从 b[i] 开始读一个 JSON 值，返回结束后的下标。
func scanValue(b []byte, i int) (int, bool) {
	if i >= len(b) {
		return i, false
	}
	switch b[i] {
	case '"':
		_, next, ok := scanString(b, i)
		return next, ok
	case '{', '[':
		depth := 0
		for i < len(b) {
			switch b[i] {
			case '"':
				_, next, ok := scanString(b, i)
				if !ok {
					return i, false
				}
				i = next
				continue
			case '{', '[':
				depth++
			case '}', ']':
				depth--
				if depth == 0 {
					return i + 1, true
				}
			}
			i++
		}
		return i, false
	default:
		// number / true / false / null ⇒ 到分隔符为止
		start := i
		for i < len(b) {
			c := b[i]
			if c == ',' || c == '}' || c == ']' || c == ' ' || c == '\t' || c == '\n' || c == '\r' {
				break
			}
			i++
		}
		if i == start {
			return i, false
		}
		return i, true
	}
}
