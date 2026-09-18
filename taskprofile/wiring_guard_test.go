package taskprofile

import (
	"os"
	"strings"
	"testing"
)

// R43 (2026-09-18): the FeedbackRecorder was originally attached to the
// routingopt-side CorrectionStore (cmd/gateway/routing_optimizer_init.go),
// but that store is read-only (CorrectionSource → CorrectionStats only), so
// RecordFeedback never fired and the Prometheus classification feedback
// counters stayed at zero. The production recorder must live on the admin
// handlers' store — the only write path (POST /corrections + CSV import).
// These pins keep both sides honest; if the wiring moves, update them in the
// same commit.
func TestAdminWiring_AttachesFeedbackRecorder(t *testing.T) {
	src, err := os.ReadFile("../admin/handler.go")
	if err != nil {
		t.Fatalf("read admin/handler.go: %v", err)
	}
	s := string(src)
	if !strings.Contains(s, "taskprofile.NewHandlers(h.db)") {
		t.Fatal("admin/handler.go no longer constructs taskprofile.NewHandlers; update this pin if the wiring moved")
	}
	if !strings.Contains(s, "SetRecorder(") {
		t.Fatal("admin/handler.go does not call SetRecorder on the taskprofile handlers — " +
			"correction writes will no longer feed the classification feedback counters (R43 leak)")
	}
}

func TestOptimizerSideStore_StaysReadOnly(t *testing.T) {
	src, err := os.ReadFile("../cmd/gateway/routing_optimizer_init.go")
	if err != nil {
		t.Fatalf("read cmd/gateway/routing_optimizer_init.go: %v", err)
	}
	s := string(src)
	if strings.Contains(s, "correctionStore.SetRecorder(") {
		t.Fatal("routing_optimizer_init.go re-attached SetRecorder to the read-only CorrectionSource store — " +
			"that recorder can never fire (no write path reaches this store); attach it on the admin side instead")
	}
	if !strings.Contains(s, "WithCorrectionSource(taskprofileCorrectionSource{store: correctionStore})") {
		t.Fatal("routing_optimizer_init.go no longer wires the CorrectionSource; confidence damping would silently stop")
	}
}
