package streaming

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/provider"
)

// modalityMismatchResolver is a providerResolver stub that reports a
// modality mismatch on demand.
//
// The two knobs matter separately:
//
//	known      — whether the model name is recognised at all
//	mismatch   — what ModelModalityMismatch reports (nil = "not a modality
//	              problem", i.e. the zero candidates came from somewhere else)
//
// A test that only exercised the "no candidates" path would pass even if the
// new branch never fired, so both arms are asserted below.
type modalityMismatchResolver struct {
	known    bool
	mismatch *provider.ModalityMismatch
}

func (r *modalityMismatchResolver) Enabled() bool { return true }

func (r *modalityMismatchResolver) GetCandidates(context.Context, string, string, string) ([]provider.Candidate, *provider.Policy, error) {
	return nil, provider.DefaultPolicy(), nil
}

func (r *modalityMismatchResolver) GetCandidatesByModality(context.Context, string, string, string, string) ([]provider.Candidate, *provider.Policy, error) {
	return nil, provider.DefaultPolicy(), nil
}

func (r *modalityMismatchResolver) ModelKnown(context.Context, string) bool { return r.known }

func (r *modalityMismatchResolver) ModelModalityMismatch(context.Context, string, string, string, string) (*provider.ModalityMismatch, error) {
	return r.mismatch, nil
}

// TestWriteModalityMismatchError_BodyShape pins the response shape.
//
// The whole point of this error is that a client can act on it without
// parsing prose, so the machine-readable fields are part of the contract:
// code, requested_modality, registered_modality, supported_modalities.
//
// ★★ 2026-10-29 补一条**黄金断言**（第五份表达 · 错误载荷的 supported_modalities）：
//
//	载荷里的 supported_modalities 必须等于**发布清单的黄金形状**，而不是
//	「函数返回什么就放什么」—— 后者是恒真的。
//	⇒ 这里用一个 multimodal 登记的模型（清单最长、最容易少项）来钉。
func TestWriteModalityMismatchError_BodyShape(t *testing.T) {
	h := &ChatHandler{}
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader("{}"))
	w := httptest.NewRecorder()

	mismatch := &provider.ModalityMismatch{
		RequestedModality:  "vision",
		RegisteredModality: "text",
		CanonicalModel:     "glm-5.3-flash",
	}
	h.writeModalityMismatchError(r, w, "req-123", "glm-5.3-flash", mismatch,
		&chatRequestBody{}, []byte("{}"), nil)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d: a modality mismatch is a client-side problem, not a transient 503",
			w.Code, http.StatusBadRequest)
	}

	var body struct {
		Error struct {
			Message string `json:"message"`
			Type    string `json:"type"`
			Code    string `json:"code"`
			Kind    string `json:"kind"`
			Request string `json:"request_id"`
			Detail  struct {
				RequestedModality  string   `json:"requested_modality"`
				RegisteredModality string   `json:"registered_modality"`
				Supported          []string `json:"supported_modalities"`
			} `json:"modality_mismatch"`
		} `json:"error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("response is not valid JSON: %v\nbody: %s", err, w.Body.String())
	}

	if body.Error.Code != "modality_mismatch" {
		t.Errorf("code = %q, want %q", body.Error.Code, "modality_mismatch")
	}
	if body.Error.Kind != "modality_mismatch" {
		t.Errorf("kind = %q, want %q", body.Error.Kind, "modality_mismatch")
	}
	if body.Error.Type != "invalid_request_error" {
		t.Errorf("type = %q, want %q", body.Error.Type, "invalid_request_error")
	}
	if body.Error.Request != "req-123" {
		t.Errorf("request_id = %q, want %q", body.Error.Request, "req-123")
	}
	if body.Error.Detail.RequestedModality != "vision" {
		t.Errorf("requested_modality = %q, want %q", body.Error.Detail.RequestedModality, "vision")
	}
	if body.Error.Detail.RegisteredModality != "text" {
		t.Errorf("registered_modality = %q, want %q", body.Error.Detail.RegisteredModality, "text")
	}
	// A text-only model can serve text and nothing else. Listing more than
	// that would tell the client to send images to a model that cannot read
	// them.
	if len(body.Error.Detail.Supported) != 1 || body.Error.Detail.Supported[0] != "text" {
		t.Errorf("supported_modalities = %v, want [text]", body.Error.Detail.Supported)
	}
	// The message carries the LOCALISED modality word ("image" in English),
	// while the machine-readable field carries the raw token ("vision").
	// Both must be present: the token for machines, the word for humans.
	if !strings.Contains(body.Error.Message, "image") {
		t.Errorf("message does not name the modality: %q", body.Error.Message)
	}
	if !strings.Contains(body.Error.Message, "glm-5.3-flash") {
		t.Errorf("message does not name the model: %q", body.Error.Message)
	}
}

// TestWriteModalityMismatchError_SupportedModalitiesGolden 钉住错误载荷里的
// supported_modalities **黄金形状**（第五份表达）。
//
// ★ 为什么单独一条：载荷那段代码是
//
//	"supported_modalities": supportedModalitiesFor(registeredModality)
//
// ⇒ 断言「载荷 == 函数返回值」是**恒真**的 ⇒ 必须断言**黄金形状**。
// ⇒ 取 multimodal 登记（清单最长、最容易少一项）来钉。
func TestWriteModalityMismatchError_SupportedModalitiesGolden(t *testing.T) {
	h := &ChatHandler{}
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader("{}"))
	w := httptest.NewRecorder()

	mismatch := &provider.ModalityMismatch{
		RequestedModality:  "vision",
		RegisteredModality: "multimodal",
		CanonicalModel:     "gemini-2.5-pro",
	}
	h.writeModalityMismatchError(r, w, "req-456", "gemini-2.5-pro", mismatch,
		&chatRequestBody{}, []byte("{}"), nil)

	var body struct {
		Error struct {
			Detail struct {
				Supported []string `json:"supported_modalities"`
			} `json:"modality_mismatch"`
		} `json:"error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("response is not valid JSON: %v\nbody: %s", err, w.Body.String())
	}
	want := []string{"text", "vision", "audio", "video", "image", "multimodal"}
	got := body.Error.Detail.Supported
	if len(got) != len(want) {
		t.Fatalf("supported_modalities = %v, want %v", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("supported_modalities = %v, want %v", got, want)
		}
	}
}

// TestSupportedModalitiesFor pins the acceptance table the response publishes.
//
// ★ This table is the client's contract for "may I send this modality to
// this model". It must mirror provider.ModalityAccepts — the same rule the
// router's modality gate applies. If the two drift, the client would be told
// a model accepts audio while the gateway rejects every audio request for it.
//
// ★★ 2026-10-29：这张表**不再手写**——它由 provider.ModalityAccepts 过滤
// provider.ModalityRequestModalities 派生（见 handler.go 的
// supportedModalitiesFor）。⇒ 本测试的期望值成了**契约的黄金表**：它是
// 「派生出来的形状」与「人读得懂的形状」之间唯一的对照物。
// ⇒ 期望值里必须含有「天真的表会算错」的格，否则这张表就没有牙：
//
//	· R=multimodal 那行**不含** 'embedding'（兜底精确臂：只接同名登记的行）
//	  —— 一张「multimodal 接一切」的表会多出 'embedding' ⇒ 被这里抓住；
//	· R=embedding 那行**含** 'embedding'（登记为 embedding 的模型确实接
//	  嵌入请求）—— 一张「默认只有 text」的表会漏掉它 ⇒ 被这里抓住。
func TestSupportedModalitiesFor(t *testing.T) {
	cases := []struct {
		registered string
		want       []string
	}{
		{"text", []string{"text"}},
		{"multimodal", []string{"text", "vision", "audio", "video", "image", "multimodal"}},
		{"vision", []string{"text", "vision", "image"}},
		{"audio", []string{"text", "audio"}},
		{"video", []string{"text", "video"}},
		{"image", []string{"text", "image"}},
		{"embedding", []string{"text", "embedding"}},
		{"", []string{"text"}},
	}
	for _, tc := range cases {
		got := supportedModalitiesFor(tc.registered)
		if len(got) != len(tc.want) {
			t.Errorf("supportedModalitiesFor(%q) = %v, want %v", tc.registered, got, tc.want)
			continue
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Errorf("supportedModalitiesFor(%q) = %v, want %v", tc.registered, got, tc.want)
				break
			}
		}
	}
}

// TestSupportedModalitiesFor_IsDerivedNotTabulated 是【有牙】断言：证明
// supportedModalitiesFor **真的在过滤**，而不是又一份手写的表。
//
// ★ 为什么需要它：表改成「由判据派生」之后，原来那条「表 vs 判据」的交叉
//
//	检查变成了**恒真**（两边同一个来源）⇒ 它不再能证明任何事
//	（LESSONS §559 通用式三：恒真断言要主动做负控）。
//
// ⇒ 负控做法：换掉**输入**（请求模态词表）⇒ 输出必须跟着变。
//
//	· 少给一个模态 ⇒ 清单必须少一项；
//	· 给一个判据不接的模态 ⇒ 清单里不能出现它。
func TestSupportedModalitiesFor_IsDerivedNotTabulated(t *testing.T) {
	// 负控 ①：裁掉词表 ⇒ 清单必须同步裁掉。
	got := filterSupportedModalities("multimodal", []string{"text", "vision"})
	want := []string{"text", "vision"}
	if len(got) != len(want) {
		t.Fatalf("裁剪词表后清单 = %v, want %v —— ★ 这张表不是过滤出来的（是又一份手写的表）", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("裁剪词表后清单 = %v, want %v", got, want)
		}
	}

	// 负控 ②：给一个判据不接的模态（登记=multimodal 时不接 embedding）⇒
	// 清单里不能出现它。
	got = filterSupportedModalities("multimodal", []string{"text", "embedding"})
	if len(got) != 1 || got[0] != "text" {
		t.Fatalf("登记=multimodal 时清单 = %v, want [text] —— ★ 'embedding' 不该出现（兜底精确臂：只接同名登记的行）⇒ 这张表没在用判据", got)
	}

	// 负控 ③：登记=embedding 时 'embedding' 必须出现（反向：默认只给 text
	// 的手写表会漏掉它）。
	got = filterSupportedModalities("embedding", []string{"text", "embedding"})
	if len(got) != 2 || got[0] != "text" || got[1] != "embedding" {
		t.Fatalf("登记=embedding 时清单 = %v, want [text embedding] —— ★ 登记为 embedding 的模型确实接嵌入请求（判据的兜底精确臂）", got)
	}
}

// TestModalityAccepts_MatchesPublishedTable cross-checks the two
// implementations of the acceptance rule.
//
// ★★ 2026-10-29：这条测试**已经变成恒真**（发布表现在由
// provider.ModalityAccepts 派生 ⇒ 两边同一个来源）⇒ 它不再能证明任何事
// ⇒ 它的职责被拆成两条**有牙**的断言：
//
//	· TestSupportedModalitiesFor（黄金表：派生出来的形状 vs 人读得懂的形状，
//	  且表里含有「天真的表会算错」的格）；
//	· TestSupportedModalitiesFor_IsDerivedNotTabulated（负控：换输入必须换输出）。
//
// ⇒ 本条保留为**形状回归**（判据与发布清单在【全部】登记值上逐格一致，
//
//	登记侧的域锚在事实源上），并加一条负控防止它退化成「两边都空」。
func TestModalityAccepts_MatchesPublishedTable(t *testing.T) {
	registered := append([]string{}, provider.ModalityRequestModalities...)
	registered = append(registered, "") // 空登记 ⇒ SQL 侧 COALESCE 归一为 text
	// ★ 请求侧**不含空串**：空请求模态的含义是「没有模态过滤」（判据放行一切），
	//   它不是一个「客户端可以发送的模态」⇒ 不进发布清单 ⇒ 不参与本比对。
	requested := append([]string{}, provider.ModalityRequestModalities...)

	for _, reg := range registered {
		supported := supportedModalitiesFor(reg)
		for _, req := range requested {
			want := modalityListContains(supported, req)
			got := provider.ModalityAccepts(reg, req)
			if got != want {
				t.Errorf("registered=%q requested=%q: modalityAccepts=%v but published table says %v",
					reg, req, got, want)
			}
		}
	}

	// ★ 负控：清单不能恒为空（否则上面那条循环会在「全 false」上自洽 ⇒ 恒真）。
	for _, reg := range registered {
		if len(supportedModalitiesFor(reg)) == 0 {
			t.Errorf("registered=%q 的发布清单是空的 —— ★ 清单恒空 ⇒ 上面的逐格比对恒真 ⇒ 没有信息量", reg)
		}
	}
}

func modalityListContains(list []string, want string) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}

// TestModalityMismatch_IsZero covers the "not a mismatch" signal, which the
// handler branches on. A nil-safe IsZero keeps the branch from panicking on
// a nil mismatch.
func TestModalityMismatch_IsZero(t *testing.T) {
	var nilMismatch *provider.ModalityMismatch
	if !nilMismatch.IsZero() {
		t.Error("nil mismatch must report IsZero() = true")
	}
	if !(&provider.ModalityMismatch{}).IsZero() {
		t.Error("a mismatch with no requested modality must report IsZero() = true")
	}
	if (&provider.ModalityMismatch{RequestedModality: "vision"}).IsZero() {
		t.Error("a mismatch with a requested modality must report IsZero() = false")
	}
}
