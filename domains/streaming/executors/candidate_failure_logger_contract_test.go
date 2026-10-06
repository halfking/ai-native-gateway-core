package executors

import (
	"fmt"
	"regexp"
	"testing"

	"github.com/kaixuan/llm-gateway-go/errorsx"
	upstreampkg "github.com/kaixuan/llm-gateway-go/upstream"
	"github.com/pashagolub/pgxmock/v4"
)

func TestCandidateFailureWriterInsertContract(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(mock.Close)

	// 2026-09-05 审计闭环1：同一行数据双写 candidate_failure_logs_hot（旧读端）
	// 与 supplier_errors_hot（唯一事实源）。两条 INSERT 共用同一 3s 超时上下文。
	mock.ExpectExec(regexp.QuoteMeta(candidateFailureInsertSQL)).
		WithArgs("req-1", "tenant-1", "sess-1", 7, 9, "model-1", 2, "network", "[network] boom: <nil>", pgxmock.AnyArg(), "", "", pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mock.ExpectExec(regexp.QuoteMeta(supplierErrorInsertSQL)).
		WithArgs(pgxmock.AnyArg(), "req-1", "", "tenant-1", "sess-1", 9, "", 7, "model-1", 2,
			"network", pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), "", pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))

	w := &CandidateFailureWriter{pool: mock}
	w.LogFailure("req-1", "tenant-1", "sess-1", 7, 9, "model-1", 2,
		&upstreampkg.Error{Kind: upstreampkg.KindNetwork, Message: "boom"}, nil, candidateFailureIntPtr(123), nil)
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func candidateFailureIntPtr(v int) *int { return &v }

// R49-C2（2026-10-07）回归门：audio 面的上游错误是自有类型
// （streaming.audioUpstreamStatusError），不包装 *upstream.Error。此前
// buildRow 对这类错误掉进消息分类兜底 ⇒ supplier_errors_hot 的
// http_status 恒 NULL、error_kind 多误判 transient，凭据详情对音频供应
// 商的服务质量面（distinct_status_codes / 类型分布）失真。修复后凡实现
// StatusCode()+Body() 的错误按同口径提取 status/body，kind 走
// ClassifyErrorWithBody（与 *upstream.Error 路径同语义）。
type r49AudioLikeUpstreamErr struct {
	status int
	body   string
}

func (e *r49AudioLikeUpstreamErr) Error() string { return fmt.Sprintf("upstream http %d", e.status) }
func (e *r49AudioLikeUpstreamErr) StatusCode() int {
	return e.status
}
func (e *r49AudioLikeUpstreamErr) Body() string { return e.body }

func TestBuildRow_AudioStyleStatusErrorCarriesStatusAndKind(t *testing.T) {
	w := &CandidateFailureWriter{}
	row := w.buildRow("req-audio", "tenant", "", 1, 2, "asr-model", 0,
		&r49AudioLikeUpstreamErr{status: 429, body: `{"code":1310,"message":"quota"}`}, "", nil, nil, nil)
	if row.UpstreamStatusCode == nil || *row.UpstreamStatusCode != 429 {
		t.Fatalf("upstream_status_code = %v, want 429 —— audio 面状态码仍未进台账（R49-C2）", row.UpstreamStatusCode)
	}
	if row.ErrorKind != string(errorsx.KindRateLimit) {
		t.Fatalf("error_kind = %q, want rate_limit（429 状态门）", row.ErrorKind)
	}
	if row.UpstreamResponseBody == "" || row.UpstreamResponsePreview == "" {
		t.Fatalf("body/preview 未投影：body=%q preview=%q", row.UpstreamResponseBody, row.UpstreamResponsePreview)
	}
}
