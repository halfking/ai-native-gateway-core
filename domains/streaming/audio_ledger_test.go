// Package streaming — audio_ledger_test.go
//
// R40（R39 §七.3 移交收口）钉测：音频面的逐候选失败必须进共享
// candidate_failure_logs 台账——此前音频请求在 credential quality 视图上
// 完全不可见（chat/embeddings 均已接线）。回归形态：候选全部失败时，
// 每个被放弃的候选恰好记一行，request_id/tenant_id/attempt/transport 齐全。
package streaming

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/kaixuan/llm-gateway-go/domains/streaming/executors"
	"github.com/kaixuan/llm-gateway-go/provider"
	"github.com/kaixuan/llm-gateway-go/upstream"
)

// fakeCandidateFailureDB 捕获台账 INSERT 的参数（按 SQL 前缀过滤）。
type fakeCandidateFailureDB struct {
	mu     sync.Mutex
	calls  []string          // 每次 Exec 的 SQL
	rows   [][]any           // 与 calls 对齐的参数
}

func (f *fakeCandidateFailureDB) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, sql)
	f.rows = append(f.rows, args)
	return pgconn.CommandTag{}, nil
}

func (f *fakeCandidateFailureDB) candidateRows() [][]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out [][]any
	for i, sql := range f.calls {
		if strings.Contains(sql, "candidate_failure_logs_hot") {
			out = append(out, f.rows[i])
		}
	}
	return out
}

// TestAudioCandidateFailureLedger：两个候选都 500 → 台账恰好两行，
// credential_id 各自对号，request_id/tenant_id/attempt_index 透传。
func TestAudioCandidateFailureLedger(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":{"message":"boom"}}`, http.StatusInternalServerError)
	}))
	defer srv.Close()

	cand1 := provider.Candidate{
		CredentialID: 11, ProviderID: 1, BaseURL: srv.URL,
		Protocol: "openai-completions", CatalogCode: "xiaomi",
		RawModel: "mimo-v2.5-asr", Routable: true, APIKey: "sk-test",
	}
	cand1.AvailabilityState = "ready"
	cand2 := cand1
	cand2.CredentialID = 22

	fakeDB := &fakeCandidateFailureDB{}
	svc := NewAudioService(&fakeAudioResolver{candidates: []provider.Candidate{cand1, cand2}}, upstream.New())
	svc.SetFailureLogger(executors.NewCandidateFailureWriter(fakeDB))

	_, err := svc.Transcribe(t.Context(), TranscribeRequest{
		Model: "mimo-v2.5-asr", File: tinyWAVForTest(),
		Filename: "a.wav", ContentType: "audio/wav",
		TenantID: "tenant-a", RequestID: "req-ledger-1",
	}, nil)
	if err == nil {
		t.Fatalf("all-failing upstreams must surface an error")
	}

	rows := fakeDB.candidateRows()
	if len(rows) != 2 {
		t.Fatalf("candidate failure ledger rows = %d, want 2 (one per abandoned candidate)", len(rows))
	}
	// INSERT 参数序：request_id, tenant_id, session_id, credential_id,
	// provider_id, raw_model_name, attempt_index, ...
	seenCreds := map[int]bool{}
	for i, row := range rows {
		if got := row[0]; got != "req-ledger-1" {
			t.Errorf("row %d request_id = %v, want req-ledger-1", i, got)
		}
		if got := row[1]; got != "tenant-a" {
			t.Errorf("row %d tenant_id = %v, want tenant-a", i, got)
		}
		if got := row[6]; got != i {
			t.Errorf("row %d attempt_index = %v, want %d", i, got, i)
		}
		cred, _ := row[3].(int)
		seenCreds[cred] = true
	}
	if !seenCreds[11] || !seenCreds[22] {
		t.Errorf("ledger must record both abandoned candidates, saw %v", seenCreds)
	}
}

// TestAudioCandidateFailureLedgerNotWiredByDefault：默认不接线时零写入、
// 且不影响请求路径（nil-safe 回归保护）。
func TestAudioCandidateFailureLedgerNotWiredByDefault(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()

	cand := provider.Candidate{
		CredentialID: 1, ProviderID: 1, BaseURL: srv.URL,
		Protocol: "openai-completions", CatalogCode: "xiaomi",
		RawModel: "mimo-v2.5-asr", Routable: true, APIKey: "sk-test",
	}
	cand.AvailabilityState = "ready"
	svc := NewAudioService(&fakeAudioResolver{candidates: []provider.Candidate{cand}}, upstream.New())

	_, err := svc.Transcribe(t.Context(), TranscribeRequest{
		Model: "mimo-v2.5-asr", File: tinyWAVForTest(),
		Filename: "a.wav", ContentType: "audio/wav",
	}, nil)
	if err == nil {
		t.Fatalf("expected error from failing upstream")
	}
}
