package upgrader

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

type fakeAutoUpgradeExecutor struct {
	called bool
	result UpgradeResult
	err    error
}

func (f *fakeAutoUpgradeExecutor) ExecuteAutoUpgrade(_ context.Context, task UpgradeTask, ticket DownloadTicket, checksum string, reportProgress func(UpgradeProgress) error) (UpgradeResult, error) {
	f.called = true
	if task.TaskID == 0 || ticket.URL == "" || checksum == "" {
		return UpgradeResult{}, nil
	}
	if err := reportProgress(UpgradeProgress{Status: "downloading", Stage: "download", Progress: 50}); err != nil {
		return UpgradeResult{}, err
	}
	return f.result, f.err
}

func TestAutoUpgradeAgentDoesNotPollWithoutHint(t *testing.T) {
	proof := DeviceProof{InstanceID: "instance-1", LicenseKey: "license-1", HardwareHash: "hardware-1"}
	polls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/maintain-api/distribution/version-check":
			_, _ = w.Write([]byte(`{"update_available":true,"latest_version":"v1.15.0","auto_upgrade":false,"target_artifacts":[{"platform":"linux","arch":"amd64","sha256":"abc","storage_uri":"https://files.example/gateway.tar.gz"}]}`))
		case "/maintain-api/upgrade/poll":
			polls++
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()

	executor := &fakeAutoUpgradeExecutor{result: UpgradeResult{Status: "completed"}}
	agent := AutoUpgradeAgent{Client: NewClientWithProof(server.URL, proof), Executor: executor}
	claimed, err := agent.RunOnce(context.Background(), "v1.14.0", "stable")
	if err != nil {
		t.Fatalf("RunOnce() error = %v", err)
	}
	if claimed || executor.called || polls != 0 {
		t.Fatalf("claimed=%v called=%v polls=%d, want all false/zero", claimed, executor.called, polls)
	}
}

func TestAutoUpgradeAgentTreatsPoll204AsIdle(t *testing.T) {
	proof := DeviceProof{InstanceID: "instance-1", LicenseKey: "license-1", HardwareHash: "hardware-1"}
	polls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/maintain-api/distribution/version-check":
			assertProofHeaders(t, r, proof)
			_, _ = w.Write([]byte(`{"update_available":true,"latest_version":"v1.15.0","auto_upgrade":true,"target_artifacts":[{"platform":"linux","arch":"amd64","sha256":"abc","storage_uri":"https://files.example/gateway.tar.gz"}]}`))
		case "/maintain-api/upgrade/poll":
			polls++
			assertProofHeaders(t, r, proof)
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()

	executor := &fakeAutoUpgradeExecutor{result: UpgradeResult{Status: "completed"}}
	agent := AutoUpgradeAgent{Client: NewClientWithProof(server.URL, proof), Executor: executor}
	for i := 0; i < 2; i++ {
		claimed, err := agent.RunOnce(context.Background(), "v1.14.0", "stable")
		if err != nil {
			t.Fatalf("RunOnce(%d) error = %v", i, err)
		}
		if claimed {
			t.Fatalf("RunOnce(%d) claimed task after 204", i)
		}
	}
	if polls != 2 || executor.called {
		t.Fatalf("polls=%d called=%v, want two polls and no execution", polls, executor.called)
	}
}

func TestAutoUpgradeAgentRejectsTaskForUncheckedVersion(t *testing.T) {
	proof := DeviceProof{InstanceID: "instance-1", LicenseKey: "license-1", HardwareHash: "hardware-1"}
	failureReported := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/maintain-api/distribution/version-check":
			_, _ = w.Write([]byte(`{"update_available":true,"latest_version":"v1.15.0","auto_upgrade":true,"target_artifacts":[{"platform":"linux","arch":"amd64","sha256":"abc","storage_uri":"https://files.example/gateway.tar.gz"}]}`))
		case "/maintain-api/upgrade/poll":
			_, _ = w.Write([]byte(`{"task_id":42,"to_version":"v1.16.0"}`))
		case "/maintain-api/upgrade/tasks/42/result":
			failureReported = true
			var body UpgradeResult
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode result: %v", err)
			}
			if body.Status != "failed" {
				t.Fatalf("result status = %q, want failed", body.Status)
			}
			w.WriteHeader(http.StatusAccepted)
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()

	executor := &fakeAutoUpgradeExecutor{result: UpgradeResult{Status: "completed"}}
	agent := AutoUpgradeAgent{Client: NewClientWithProof(server.URL, proof), Executor: executor}
	claimed, err := agent.RunOnce(context.Background(), "v1.14.0", "stable")
	if err == nil {
		t.Fatal("RunOnce() error = nil, want task/artifact mismatch")
	}
	if !claimed || !failureReported || executor.called {
		t.Fatalf("claimed=%v failureReported=%v executorCalled=%v", claimed, failureReported, executor.called)
	}
}

func TestAutoUpgradeAgentExecutesAndReportsTask(t *testing.T) {
	proof := DeviceProof{InstanceID: "instance-1", LicenseKey: "license-1", HardwareHash: "hardware-1"}
	progressReported := false
	resultReported := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/maintain-api/distribution/version-check":
			assertProofHeaders(t, r, proof)
			_, _ = w.Write([]byte(`{"update_available":true,"latest_version":"v1.15.0","auto_upgrade":true,"target_artifacts":[{"platform":"linux","arch":"amd64","sha256":"abc","storage_uri":"https://files.example/gateway.tar.gz"}]}`))
		case "/maintain-api/upgrade/poll":
			assertProofHeaders(t, r, proof)
			_, _ = w.Write([]byte(`{"task_id":42,"to_version":"v1.15.0"}`))
		case "/maintain-api/downloads/ticket":
			var body downloadTicketRequest
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode ticket request: %v", err)
			}
			if body.Version != "v1.15.0" || body.Platform != "linux" || body.Arch != "amd64" {
				t.Fatalf("unexpected ticket request: %#v", body)
			}
			_, _ = w.Write([]byte(`{"request_id":"req-1","url":"https://files.example/gateway.tar.gz"}`))
		case "/maintain-api/upgrade/tasks/42/progress":
			progressReported = true
			assertProofHeaders(t, r, proof)
			w.WriteHeader(http.StatusAccepted)
		case "/maintain-api/upgrade/tasks/42/result":
			resultReported = true
			assertProofHeaders(t, r, proof)
			var body UpgradeResult
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode result: %v", err)
			}
			if body.Status != "completed" {
				t.Fatalf("result status = %q", body.Status)
			}
			w.WriteHeader(http.StatusAccepted)
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()

	executor := &fakeAutoUpgradeExecutor{result: UpgradeResult{Status: "completed"}}
	agent := AutoUpgradeAgent{Client: NewClientWithProof(server.URL, proof), Executor: executor}
	claimed, err := agent.RunOnce(context.Background(), "v1.14.0", "stable")
	if err != nil {
		t.Fatalf("RunOnce() error = %v", err)
	}
	if !claimed || !executor.called || !progressReported || !resultReported {
		t.Fatalf("claimed=%v executor=%v progress=%v result=%v", claimed, executor.called, progressReported, resultReported)
	}
}
