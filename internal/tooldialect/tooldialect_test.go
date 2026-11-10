package tooldialect_test

// 外部测试包：需要 import internal/ir 做协议键对齐断言，内部测试包会与
// ir → tooldialect 形成循环。
import (
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/internal/ir"
	"github.com/kaixuan/llm-gateway-go/internal/tooldialect"
)

// ★ 契约门：tooldialect 的协议键必须与 internal/ir 的 Protocol* 常量同值。
// 两边任意一侧改名，此断言转红（字符串字面量对齐是避免循环依赖的代价，
// 代价必须有门看住）。
func TestProtocolKeysAlignWithIR(t *testing.T) {
	if tooldialect.ProtocolAnthropicMessages != ir.ProtocolAnthropicMessages {
		t.Fatalf("协议键漂移：tooldialect=%q ir=%q（两侧必须同值）", tooldialect.ProtocolAnthropicMessages, ir.ProtocolAnthropicMessages)
	}
}

// ★ 事故门（2026-11-11 211 builtin.guard.tool_json_invalid 根因）：
// anthropic 方言必须把 content_block_start 的 `{}` 占位符排除在参数流之外；
// 真实半截参数（非标准中继形状）保留。
func TestAnthropicPlaceholderSemantics(t *testing.T) {
	d := tooldialect.ForProtocol(tooldialect.ProtocolAnthropicMessages)
	if got := d.Name(); !strings.Contains(got, "anthropic") {
		t.Fatalf("方言名 = %q", got)
	}
	for _, tc := range []struct {
		raw  string
		want string
	}{
		{raw: `{}`, want: ""}, // 标准占位符
		{raw: ``, want: ""},   // input 缺失（nil RawMessage）
		{raw: `{"file_path":"/a.go"`, want: `{"file_path":"/a.go"`}, // 中继真实半截参数
		{raw: `{"a":1}`, want: `{"a":1}`},                           // 完整参数在首帧
	} {
		if got := d.StartArguments([]byte(tc.raw)); got != tc.want {
			t.Fatalf("StartArguments(%q) = %q, want %q", tc.raw, got, tc.want)
		}
	}
}

// ★ 未装配即正确：不经生产装配（无校验器注入），anthropic 种子方言的
// MergeContinuation 用 json.Valid 兜底——合法续拼放行、非法尾部丢弃且
// 保留已下发的 start（不回撤已发字节）。
func TestAnthropicMergeContinuationBuiltinFallback(t *testing.T) {
	d := tooldialect.ForProtocol(tooldialect.ProtocolAnthropicMessages) // 种子版（validate=nil）
	if merged, ok := d.MergeContinuation(`{"a":`, `1}`); !ok || merged != `{"a":1}` {
		t.Fatalf("合法续拼: merged=%q ok=%v", merged, ok)
	}
	if merged, ok := d.MergeContinuation(`{"a":1`, ``); !ok || merged != `{"a":1` {
		t.Fatalf("无增量: merged=%q ok=%v", merged, ok)
	}
	if merged, ok := d.MergeContinuation(`{"a":1}`, `{"junk`); ok || merged != `{"a":1}` {
		t.Fatalf("非法尾部必须丢弃且保留 start: merged=%q ok=%v", merged, ok)
	}
}

// ★ 注入校验器（生产装配形状）：修复器返回的整文档必须透传（含保守修复
// 成功的形状）；校验器报错 ⇒ ok=false。
func TestAnthropicMergeContinuationWithValidator(t *testing.T) {
	var seenDoc string
	d := tooldialect.NewAnthropic(func(doc string) (string, bool, error) {
		seenDoc = doc
		if doc == `{"a":1,"b":2` {
			return `{"a":1,"b":2}`, true, nil // 模拟保守修复（闭合容器）
		}
		return doc, false, errFakeInvalid
	})
	if merged, ok := d.MergeContinuation(`{"a":1`, `,"b":2`); !ok || merged != `{"a":1,"b":2}` {
		t.Fatalf("修复结果必须透传: merged=%q ok=%v", merged, ok)
	}
	if seenDoc != `{"a":1,"b":2` {
		t.Fatalf("校验器收到的必须是 start+buffered 整文档: %q", seenDoc)
	}
	if merged, ok := d.MergeContinuation(`{"a":1`, `,x`); ok || merged != `{"a":1` {
		t.Fatalf("校验失败: merged=%q ok=%v", merged, ok)
	}
}

type fakeInvalidErr struct{}

func (fakeInvalidErr) Error() string { return "invalid" }

var errFakeInvalid = fakeInvalidErr{}

// ★ OCP 门：新厂商方言 = 实现 + 注册，核心（tooldialect.ForProtocol 消费方）零改动。
// 用一个虚构厂商方言演示：占位符约定不同（`null` 占位）+ 续拼策略不同。
func TestRegistryOpenForExtension(t *testing.T) {
	const fakeProto = "test.fake-vendor"
	tooldialect.Register(fakeProto, fakeVendorDialect{})
	d := tooldialect.ForProtocol(fakeProto)
	if d.Name() != "fake-vendor/tool-args" {
		t.Fatalf("未取到注册的厂商方言: %q", d.Name())
	}
	if got := d.StartArguments([]byte(`null`)); got != "" {
		t.Fatalf("fake 厂商占位符约定（null ⇒ 空）: got %q", got)
	}
	if merged, ok := d.MergeContinuation("", `{"x":1}`); !ok || merged != `{"x":1}` {
		t.Fatalf("fake 厂商续拼: merged=%q ok=%v", merged, ok)
	}
	// 未注册协议回落直通方言（不吞首帧输入）。
	p := tooldialect.ForProtocol("unknown-proto")
	if got := p.StartArguments([]byte(`{}`)); got != `{}` {
		t.Fatalf("直通方言不得吞首帧输入: got %q", got)
	}
}

type fakeVendorDialect struct{}

func (fakeVendorDialect) Name() string                     { return "fake-vendor/tool-args" }
func (fakeVendorDialect) StartArguments(raw []byte) string { return "" } // 该厂商首帧恒为 null 占位
func (fakeVendorDialect) MergeContinuation(start, buffered string) (string, bool) {
	return start + buffered, true
}
