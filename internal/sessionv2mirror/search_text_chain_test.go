package sessionv2mirror

import (
	"os"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/domains/hooks/observability/telemetry"
	"github.com/kaixuan/llm-gateway-go/domains/session/v2"
)

// §9.98：`session_turns.search_text` 是**退役 v1 的硬阻塞**（§9.97 阻塞 #1）——
// 252 实测该列 **0%% 填充**（近 7 天 0/50,295），而 v1 侧 **100%**（59,721/59,721）。
// 退役 v1 会让全文检索**静默失效**：列还在、查询不报错，只是永远空。
//
// 它曾是 0%，因为这条链上有**三个断点**，每一个单独看都像「已经支持」：
//
//	① telemetry.RequestLogEntry 没有 search_text 源（注释曾写「缺源，保持零值」）
//	② v2.ProcessedRequest 没有 SearchText 字段（TurnRecord 有，Write 的入参没有）
//	③ SessionWriterV2.Write 里 `SearchText: ""` 硬覆盖，把 req.SearchText 丢弃
//
// 修了其中任意一个，另外两个仍会让它静默回到 0%。**三道门各钉一个断点。**

// TestSearchTextReachesProcessedRequest 钉断点 ①：
// 镜像侧算出的 search_text 必须与 v1 侧**逐字节相同**——
// 两边跑的是同一个纯函数 searchText(entry)，所以相等是构造保证而非巧合。
// 这正是「数据在更改前后一致」在这个字段上的直接检验。
func TestSearchTextReachesProcessedRequest(t *testing.T) {
	sp := func(s string) *string { return &s }
	entry := &telemetry.RequestLogEntry{
		RequestID:       "req-search-text-parity",
		ClientModel:     sp("gpt-4o"),
		OutboundModel:   sp("gpt-4o-2024-08-06"),
		ClientProfile:   sp("chat"),
		RequestMode:     sp("chat"),
		GwSessionID:     sp("gw_parity"),
		APIKeyPrefix:    sp("sk-probe"),
		APIKeyOwnerUser: sp(""), // 空值必须被跳过，否则检索文本会出现多余空格
		ApplicationCode: sp("app-probe"),
	}

	want := telemetry.SearchText(entry)
	if want == nil || *want == "" {
		t.Fatal("telemetry.SearchText 返回空 —— 源函数本身坏了，本门后面测不出东西")
	}

	req := &v2.ProcessedRequest{}
	applyStorageS1AFields(req, entry)

	if req.SearchText != *want {
		t.Errorf("镜像侧 search_text = %q，v1 侧 = %q —— 必须逐字节相同，"+
			"否则同一请求在两族里检索不到", req.SearchText, *want)
	}
	if strings.Contains(req.SearchText, "  ") || strings.HasPrefix(req.SearchText, " ") {
		t.Errorf("search_text 出现连续/前导空格：%q —— 空值应被跳过而不是拼进去", req.SearchText)
	}
}

// TestSearchTextIsNotHardcodedEmptyInWriter 钉断点 ③。
//
// ★判据用**正则**而不是字面串：gofmt 会按注释行重新对齐结构体字段，
// 字面串写法**仅仅因为加了一行注释**就会假红。
// 这是 §9.93.3 那条教训的落地——「判据对代码排版的敏感度本身就是要测的量」。
// 本门第一版就踩了这个坑（gofmt 把 `SearchText:` 后的空格数改了 ⇒ 假红）。
func TestSearchTextIsNotHardcodedEmptyInWriter(t *testing.T) {
	data, err := os.ReadFile("../../domains/session/v2/session_writer_v2.go")
	if err != nil {
		t.Fatalf("读不到 session_writer_v2.go: %v", err)
	}
	src := string(data)

	if regexp.MustCompile(`SearchText:\s*"",`).MatchString(src) {
		t.Errorf("Write 又把 SearchText 写死成 \"\" —— req.SearchText 会被直接丢弃，\n" +
			"session_turns.search_text 立刻回到 0%% 填充，而**不会报任何错**")
	}
	if !regexp.MustCompile(`SearchText:\s*req\.SearchText,`).MatchString(src) {
		t.Error("Write 里找不到 `SearchText: req.SearchText,` —— 判据跟不上代码改法，\n" +
			"**它现在什么也没验**")
	}
}

// TestProcessedRequestCarriesSearchText 钉断点 ②：
// ProcessedRequest 必须有这个字段，否则镜像算出来也传不进 Write。
// 单独钉是因为它最容易被「重构时觉得没用」删掉。
func TestProcessedRequestCarriesSearchText(t *testing.T) {
	if _, ok := reflect.TypeOf(v2.ProcessedRequest{}).FieldByName("SearchText"); !ok {
		t.Error("ProcessedRequest 上没有 SearchText 字段 —— 镜像侧算出的检索文本传不进 Write，\n" +
			"该列会重新变成 0%% 填充")
	}
}
