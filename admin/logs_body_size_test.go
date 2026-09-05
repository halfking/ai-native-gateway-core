package admin

import (
	"errors"
	"testing"

	"github.com/kaixuan/llm-gateway-go/domains/requestdetail"
)

func TestValidatePersistedBodySize(t *testing.T) {
	underLimit := make([]byte, requestdetail.MaxBodyFileSize/2)
	if err := validatePersistedBodySize(underLimit, underLimit); err != nil {
		t.Fatalf("bodies at the shared limit must be accepted: %v", err)
	}

	overLimit := make([]byte, requestdetail.MaxBodyFileSize+1)
	if err := validatePersistedBodySize(overLimit); !errors.Is(err, requestdetail.ErrBodyTooLarge) {
		t.Fatalf("over-limit persisted body must return ErrBodyTooLarge, got %v", err)
	}
}
