package dispatch_test

import (
	"testing"

	"github.com/kaixuan/llm-gateway-go/domains/dispatch"
	"github.com/kaixuan/llm-gateway-go/internal/ir"
)

// V6-W1.6 E14 (docs/架构优化v6/09-ir-class-journal-decoupling.md §3.2): the
// dispatch package mirrors ir.RequestClass values as string constants instead
// of importing internal/ir. This external test package can import both and
// pins the constants against drift (the dispatch package itself must stay
// ir-free — see dependency_boundary_test.go).
func TestRequestClassMirrorsIRConstants(t *testing.T) {
	if dispatch.RequestClassImmediate != string(ir.ClassImmediate) {
		t.Fatalf("RequestClassImmediate = %q, ir.ClassImmediate = %q", dispatch.RequestClassImmediate, ir.ClassImmediate)
	}
	if dispatch.RequestClassScheduled != string(ir.ClassScheduled) {
		t.Fatalf("RequestClassScheduled = %q, ir.ClassScheduled = %q", dispatch.RequestClassScheduled, ir.ClassScheduled)
	}
}
