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

	mock.ExpectExec(regexp.QuoteMeta(candidateFailureInsertSQL)).
		WithArgs("req-1", "tenant-1", "sess-1", 7, 9, "model-1", 2, "network", "[network] boom: <nil>", pgxmock.AnyArg(), "", "", pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), nil).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))

	w := &CandidateFailureWriter{pool: mock}
	w.LogFailure("req-1", "tenant-1", "sess-1", 7, 9, "model-1", 2,
		&upstreampkg.Error{Kind: upstreampkg.KindNetwork, Message: "boom"}, nil, candidateFailureIntPtr(123), nil)
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func candidateFailureIntPtr(v int) *int { return &v }
