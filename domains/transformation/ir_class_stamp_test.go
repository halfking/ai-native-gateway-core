package transformation

import (
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/domain"
	"github.com/kaixuan/llm-gateway-go/internal/ir"
)

// V6-W1.6 T2 (docs/架构优化v6/09-ir-class-journal-decoupling.md §R8): the
// executor stamps TransportContext.RequestClass/DueAt per attempt; the
// converter's Parse methods copy them onto the returned IR so downstream
// consumers (dimension entries, journal) can read the request class without
// header access.

func TestParseStampsRequestClassFromContext(t *testing.T) {
	dueAt := time.Now().Add(time.Hour).UTC()
	conv := NewTransportIRConverter(&mockIRAdapter{})
	conv.SetContext(&domain.TransportContext{
		RequestClass: string(ir.ClassScheduled),
		DueAt:        dueAt,
	})

	irReq, err := conv.ParseAnthropic([]byte(`{}`))
	if err != nil {
		t.Fatalf("ParseAnthropic: %v", err)
	}
	if irReq.Class != ir.ClassScheduled {
		t.Fatalf("IR.Class = %q, want %q", irReq.Class, ir.ClassScheduled)
	}
	if !irReq.DueAt.Equal(dueAt) {
		t.Fatalf("IR.DueAt = %v, want %v", irReq.DueAt, dueAt)
	}

	irReq, err = conv.ParseOpenAI([]byte(`{}`))
	if err != nil {
		t.Fatalf("ParseOpenAI: %v", err)
	}
	if irReq.Class != ir.ClassScheduled || !irReq.DueAt.Equal(dueAt) {
		t.Fatalf("ParseOpenAI did not stamp Class/DueAt: %+v", irReq)
	}

	irReq, err = conv.ParseResponses([]byte(`{}`))
	if err != nil {
		t.Fatalf("ParseResponses: %v", err)
	}
	if irReq.Class != ir.ClassScheduled || !irReq.DueAt.Equal(dueAt) {
		t.Fatalf("ParseResponses did not stamp Class/DueAt: %+v", irReq)
	}
}

// The provider-scoped converter must stamp from its OWN context (set via
// scoped.SetContext), not the parent's — per-request metadata rides the scope.
func TestScopedParseStampsRequestClass(t *testing.T) {
	dueAt := time.Now().Add(30 * time.Minute).UTC()
	parent := NewTransportIRConverter(&mockIRAdapter{})
	scoped := parent.WithProviderScope(7)
	sc, ok := scoped.(interface {
		SetContext(*domain.TransportContext)
	})
	if !ok {
		t.Fatal("scoped converter does not expose SetContext")
	}
	sc.SetContext(&domain.TransportContext{
		RequestClass: string(ir.ClassScheduled),
		DueAt:        dueAt,
	})

	irReq, err := scoped.(interface {
		ParseOpenAI([]byte) (*ir.InternalRequest, error)
	}).ParseOpenAI(irrBody)
	if err != nil {
		t.Fatalf("scoped ParseOpenAI: %v", err)
	}
	if irReq.Class != ir.ClassScheduled || !irReq.DueAt.Equal(dueAt) {
		t.Fatalf("scoped ParseOpenAI did not stamp Class/DueAt: %+v", irReq)
	}
}

// Without a context (or with an immediate request) Parse must leave the
// gateway-internal fields at their zero values — empty class ≙ immediate.
func TestParseWithoutContextLeavesClassZero(t *testing.T) {
	conv := NewTransportIRConverter(&mockIRAdapter{})
	irReq, err := conv.ParseOpenAI([]byte(`{}`))
	if err != nil {
		t.Fatalf("ParseOpenAI: %v", err)
	}
	if irReq.Class != "" {
		t.Fatalf("IR.Class = %q, want empty (immediate default)", irReq.Class)
	}
	if !irReq.DueAt.IsZero() {
		t.Fatalf("IR.DueAt = %v, want zero", irReq.DueAt)
	}
}

var irrBody = []byte(`{}`)
