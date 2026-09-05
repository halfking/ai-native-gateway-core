package executors

import (
	"regexp"
	"testing"

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
