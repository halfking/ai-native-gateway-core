package streaming

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	pgxmock "github.com/pashagolub/pgxmock/v4"
)

// TestServeGeminiModels_NativeShape pins the Gemini NATIVE model list
// shape (docs/vendor-formats/gemini.md §模型列表).
//
// ★ A native Gemini client (google-genai SDK) parses
// {"models":[{"name":"models/…"}]}. Handing it the OpenAI
// {"object":"list","data":[…]} shape would fail at the client's
// unmarshal step — far away from the cause.
func TestServeGeminiModels_NativeShape(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	// context_window is NULL on purpose: the handler scans it into **int
	// (real pgx supports multi-level pointers for nullable columns;
	// pgxmock only honours them for NULL). The context-window mapping is
	// covered separately by TestWriteGeminiEntries_MapsContextWindow.
	rows := pgxmock.NewRows([]string{"canonical_name", "family", "modality", "context_window"}).
		AddRow("gemini-2.5-flash", "gemini", "text", nil)
	mock.ExpectQuery(`credential_model_bindings cmb`).
		WillReturnRows(rows)

	h := NewModelsHandler()
	h.SetModelsDB(mock)
	rec := httptest.NewRecorder()
	h.ServeGeminiModels(rec, httptest.NewRequest(http.MethodGet, "/v1beta/models", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	var payload struct {
		Models []geminiModelEntry `json:"models"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode: %v\n%s", err, rec.Body.String())
	}
	if len(payload.Models) != 1 {
		t.Fatalf("models = %+v, want exactly one entry", payload.Models)
	}
	m := payload.Models[0]
	if m.Name != "models/gemini-2.5-flash" {
		t.Errorf("name = %q, want the models/ prefixed resource name", m.Name)
	}
	if m.DisplayName != "gemini-2.5-flash" {
		t.Errorf("displayName = %q, want the model id", m.DisplayName)
	}
	if len(m.SupportedGenerationMethods) == 0 {
		t.Error("supportedGenerationMethods is empty")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("query expectations: %v", err)
	}
}

// TestWriteGeminiEntries_MapsContextWindow pins the context-window
// mapping (and the "models/" prefix rule) at the serializer level.
//
// The DB-level test cannot carry a non-NULL context_window because
// pgxmock cannot scan one into the handler's **int (real pgx can).
func TestWriteGeminiEntries_MapsContextWindow(t *testing.T) {
	h := NewModelsHandler()
	ctxWindow := 1048576
	rec := httptest.NewRecorder()
	h.writeGeminiEntries(rec, []modelEntry{
		{ID: "gemini-2.5-flash", Object: "model", ContextWindow: &ctxWindow},
		{ID: "models/already-prefixed", Object: "model"},
		{ID: "", Object: "model"}, // empty id ⇒ skipped
	})

	var payload struct {
		Models []geminiModelEntry `json:"models"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode: %v\n%s", err, rec.Body.String())
	}
	if len(payload.Models) != 2 {
		t.Fatalf("models = %+v, want 2 entries (the empty id must be skipped)", payload.Models)
	}
	if payload.Models[0].InputTokenLimit == nil || *payload.Models[0].InputTokenLimit != 1048576 {
		t.Errorf("inputTokenLimit = %v, want 1048576", payload.Models[0].InputTokenLimit)
	}
	if payload.Models[1].Name != "models/already-prefixed" {
		t.Errorf("name = %q, want no duplicated models/ prefix", payload.Models[1].Name)
	}
	if payload.Models[1].InputTokenLimit != nil {
		t.Errorf("inputTokenLimit = %v, want absent when the window is unknown", *payload.Models[1].InputTokenLimit)
	}
}

// ★ TestServeGeminiModels_DoesNotPromiseCountTokens pins that the list
// only advertises the generation methods this gateway ACTUALLY serves
// for a Gemini upstream.
//
// ★ The gateway does not implement :countTokens (it needs an honest
// token count, which means forwarding to the upstream — see the audit
// doc). Listing it would be a promise the gateway does not keep, and a
// native SDK that trusts the list would then call it and fail.
func TestServeGeminiModels_DoesNotPromiseCountTokens(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	rows := pgxmock.NewRows([]string{"canonical_name", "family", "modality", "context_window"}).
		AddRow("gemini-2.5-flash", "gemini", "text", nil)
	mock.ExpectQuery(`credential_model_bindings cmb`).
		WillReturnRows(rows)

	h := NewModelsHandler()
	h.SetModelsDB(mock)
	rec := httptest.NewRecorder()
	h.ServeGeminiModels(rec, httptest.NewRequest(http.MethodGet, "/v1beta/models", nil))

	var payload struct {
		Models []geminiModelEntry `json:"models"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	for _, m := range payload.Models {
		for _, method := range m.SupportedGenerationMethods {
			if method == "countTokens" {
				t.Errorf("model %q advertises countTokens, which the gateway does not implement", m.Name)
			}
		}
	}
}

// ★ TestServeGeminiModels_SameSourceAsOpenAIList pins that the Gemini
// native list and the OpenAI list come from ONE data source.
//
// ★ Two data sources would be two truths about "what can this gateway
// serve" — and the two lists would drift.
func TestServeGeminiModels_SameSourceAsOpenAIList(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	// Both handlers must issue the SAME routable-model query.
	for i := 0; i < 2; i++ {
		rows := pgxmock.NewRows([]string{"canonical_name", "family", "modality", "context_window"}).
			AddRow("gemini-2.5-flash", "gemini", "text", nil)
		mock.ExpectQuery(`credential_model_bindings cmb`).
			WillReturnRows(rows)
	}

	h := NewModelsHandler()
	h.SetModelsDB(mock)

	openaiRec := httptest.NewRecorder()
	h.ServeHTTP(openaiRec, httptest.NewRequest(http.MethodGet, "/v1/models", nil))
	geminiRec := httptest.NewRecorder()
	h.ServeGeminiModels(geminiRec, httptest.NewRequest(http.MethodGet, "/v1beta/models", nil))

	var openaiPayload struct {
		Data []modelEntry `json:"data"`
	}
	if err := json.Unmarshal(openaiRec.Body.Bytes(), &openaiPayload); err != nil {
		t.Fatalf("decode openai: %v", err)
	}
	var geminiPayload struct {
		Models []geminiModelEntry `json:"models"`
	}
	if err := json.Unmarshal(geminiRec.Body.Bytes(), &geminiPayload); err != nil {
		t.Fatalf("decode gemini: %v", err)
	}
	if len(openaiPayload.Data) != len(geminiPayload.Models) {
		t.Fatalf("the two lists disagree on the model set: openai=%d gemini=%d",
			len(openaiPayload.Data), len(geminiPayload.Models))
	}
	if len(geminiPayload.Models) == 1 && geminiPayload.Models[0].DisplayName != openaiPayload.Data[0].ID {
		t.Errorf("the two lists disagree on the model id: openai=%q gemini=%q",
			openaiPayload.Data[0].ID, geminiPayload.Models[0].DisplayName)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("query expectations: %v", err)
	}
}
