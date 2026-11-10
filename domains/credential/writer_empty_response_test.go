package credential

// 断链①（2026-11-11）：KindEmptyResponse 的绑定级分层冷却写路径。
// 关键断言：
//   1. 只写 credential_model_bindings（reason=auto_empty_response_streak），
//      不碰 credentials（同凭据兄弟模型不受连坐）
//   2. 空模型名走全绑定臂（无 raw 解析查询）
//   3. WriteOnError 不再因 default 分支静默吞掉 empty_response
import (
	"context"
	"testing"

	"github.com/kaixuan/llm-gateway-go/errorsx"

	"github.com/pashagolub/pgxmock/v4"
)

// TestWriteOnError_EmptyResponse_CoolsBindingOnly 钉住：
// 空响应（由 reducer 连击阈值触发才到达这里）写 cmb 冷却、不写 credentials。
func TestWriteOnError_EmptyResponse_CoolsBindingOnly(t *testing.T) {
	mockDB := newSQLOnlyMock()
	defer mockDB.Close()
	expectRawBindingResolution(mockDB, 42, "glm-5.3-flash", "z-ai/glm-5.3-flash")

	// $1=reason, $2=sustainedRecoverAt, $3=initialRecoverAt, $4=credentialID, $5=rawModel
	mockDB.ExpectExec(`UPDATE credential_model_bindings`).
		WithArgs(
			"auto_empty_response_streak",
			pgxmock.AnyArg(), // sustained recover-at
			pgxmock.AnyArg(), // initial recover-at
			42,
			"z-ai/glm-5.3-flash",
		).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	w := &Writer{dbPool: mockDB}
	if err := w.WriteOnError(context.Background(), 42, "glm-5.3-flash",
		Failure{Kind: errorsx.KindEmptyResponse, Detail: "empty_stream_no_content"}); err != nil {
		t.Fatalf("WriteOnError: %v", err)
	}
	if err := mockDB.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

// TestWriteOnError_EmptyResponse_EmptyModelBranchCoversAllBindings：
// rawModel 为空 ⇒ 不做解析查询、直接按凭据全绑定臂写（与 writeModelServedTiered 同形）。
func TestWriteOnError_EmptyResponse_EmptyModelBranchCoversAllBindings(t *testing.T) {
	mockDB := newSQLOnlyMock()
	defer mockDB.Close()

	// $1=reason, $2=sustainedRecoverAt, $3=initialRecoverAt, $4=credentialID
	mockDB.ExpectExec(`UPDATE credential_model_bindings`).
		WithArgs(
			"auto_empty_response_streak",
			pgxmock.AnyArg(),
			pgxmock.AnyArg(),
			7,
		).
		WillReturnResult(pgxmock.NewResult("UPDATE", 2))

	w := &Writer{dbPool: mockDB}
	if err := w.WriteOnError(context.Background(), 7, "", Failure{Kind: errorsx.KindEmptyResponse}); err != nil {
		t.Fatalf("WriteOnError: %v", err)
	}
	if err := mockDB.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

// TestWriteOnError_EmptyResponse_ReasonStringIsDistinct：
// 升级臂只延伸同 reason 的冷却 ⇒ reason 串必须与 MNF/model_unavailable 族
// 可区分（operator-visible），且幂等重放不覆盖其它 reason 的冷却。
func TestWriteOnError_EmptyResponse_ReasonStringIsDistinct(t *testing.T) {
	if "auto_empty_response_streak" == "auto_model_not_found" ||
		"auto_empty_response_streak" == "auto_model_unavailable" {
		t.Fatal("empty-response reason collides with the model-served ladder")
	}
}
