package bg

import (
	"testing"
)

func TestSelfCheckWorkerTriggerManualRun(t *testing.T) {
	w := &SelfCheckWorker{
		stopCh:    make(chan struct{}),
		triggerCh: make(chan string, 1),
	}

	if err := w.TriggerManualRun("model-a"); err != nil {
		t.Fatalf("first trigger returned error: %v", err)
	}
	if got := <-w.triggerCh; got != "model-a" {
		t.Fatalf("triggered model = %q, want model-a", got)
	}
}

func TestSelfCheckWorkerTriggerManualRunRejectsFullQueue(t *testing.T) {
	w := &SelfCheckWorker{
		stopCh:    make(chan struct{}),
		triggerCh: make(chan string, 1),
	}
	if err := w.TriggerManualRun("model-a"); err != nil {
		t.Fatalf("first trigger returned error: %v", err)
	}
	if err := w.TriggerManualRun("model-b"); err == nil {
		t.Fatal("second trigger succeeded with a full queue")
	}
}

func TestSelfCheckWorkerTriggerManualRunRejectsStoppedWorker(t *testing.T) {
	w := &SelfCheckWorker{
		stopCh:    make(chan struct{}),
		triggerCh: make(chan string, 1),
	}
	close(w.stopCh)

	if err := w.TriggerManualRun("model-a"); err == nil {
		t.Fatal("trigger succeeded for a stopped worker")
	}
}
