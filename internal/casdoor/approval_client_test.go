package casdoor

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestNewClient(t *testing.T) {
	cfg := Config{
		Endpoint:     "https://casdoor.test",
		ClientID:     "test-client",
		ClientSecret: "test-secret",
	}

	client := NewClient(cfg)

	if client == nil {
		t.Fatal("NewClient() returned nil")
	}

	if client.config.TaskOwner != "admin" {
		t.Errorf("TaskOwner default = %v, want admin", client.config.TaskOwner)
	}

	if client.config.TTLMinutes != 30 {
		t.Errorf("TTLMinutes default = %v, want 30", client.config.TTLMinutes)
	}
}

func TestClientIsEnabled(t *testing.T) {
	tests := []struct {
		name     string
		config   Config
		expected bool
	}{
		{
			name: "fully configured",
			config: Config{
				Enabled:      true,
				Endpoint:     "https://casdoor.test",
				ClientID:     "client",
				ClientSecret: "secret",
			},
			expected: true,
		},
		{
			name: "disabled",
			config: Config{
				Enabled:      false,
				Endpoint:     "https://casdoor.test",
				ClientID:     "client",
				ClientSecret: "secret",
			},
			expected: false,
		},
		{
			name: "missing endpoint",
			config: Config{
				Enabled:      true,
				ClientID:     "client",
				ClientSecret: "secret",
			},
			expected: false,
		},
		{
			name: "missing client id",
			config: Config{
				Enabled:      true,
				Endpoint:     "https://casdoor.test",
				ClientSecret: "secret",
			},
			expected: false,
		},
		{
			name: "missing client secret",
			config: Config{
				Enabled:  true,
				Endpoint: "https://casdoor.test",
				ClientID: "client",
			},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := NewClient(tt.config)
			if got := client.IsEnabled(); got != tt.expected {
				t.Errorf("IsEnabled() = %v, want %v", got, tt.expected)
			}
		})
	}
}

func TestCreateTask(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Verify request
			if r.Method != "POST" {
				t.Errorf("Method = %v, want POST", r.Method)
			}
			if r.URL.Path != "/api/add-approval-task-from-app" {
				t.Errorf("Path = %v, want /api/add-approval-task-from-app", r.URL.Path)
			}

			// Verify Basic Auth
			username, password, ok := r.BasicAuth()
			if !ok || username != "test-client" || password != "test-secret" {
				t.Error("Basic Auth incorrect")
			}

			// Return success
			w.WriteHeader(200)
			w.Write([]byte(`{
				"status": "ok",
				"data": {
					"id": 123,
					"status": "pending",
					"externalId": "ext-123",
					"applicant": "admin/user1",
					"createdTime": "2026-11-15T10:00:00Z"
				}
			}`))
		}))
		defer server.Close()

		client := NewClient(Config{
			Enabled:      true,
			Endpoint:     server.URL,
			ClientID:     "test-client",
			ClientSecret: "test-secret",
			CallbackURL:  "https://test.com/callback",
			TaskOwner:    "admin",
		})

		req := &CreateTaskRequest{
			ExternalID:   "ext-123",
			Applicant:    "admin/user1",
			Type:         "test",
			ResourceType: "session",
			ResourceID:   "sess-123",
			Title:        "Test Task",
		}

		task, err := client.CreateTask(context.Background(), req)
		if err != nil {
			t.Fatalf("CreateTask() error = %v", err)
		}

		if task.ID != 123 {
			t.Errorf("Task.ID = %v, want 123", task.ID)
		}
		if task.Status != "pending" {
			t.Errorf("Task.Status = %v, want pending", task.Status)
		}
		if task.ExternalID != "ext-123" {
			t.Errorf("Task.ExternalID = %v, want ext-123", task.ExternalID)
		}
	})

	t.Run("client not enabled", func(t *testing.T) {
		client := NewClient(Config{
			Enabled: false,
		})

		_, err := client.CreateTask(context.Background(), &CreateTaskRequest{})
		if err == nil {
			t.Error("CreateTask() expected error when disabled")
		}
		if !strings.Contains(err.Error(), "not enabled") {
			t.Errorf("Error message = %v, want 'not enabled'", err)
		}
	})

	t.Run("server error", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(500)
			w.Write([]byte(`{"status":"error","msg":"internal error"}`))
		}))
		defer server.Close()

		client := NewClient(Config{
			Enabled:      true,
			Endpoint:     server.URL,
			ClientID:     "test-client",
			ClientSecret: "test-secret",
		})

		_, err := client.CreateTask(context.Background(), &CreateTaskRequest{})
		if err == nil {
			t.Error("CreateTask() expected error on 500")
		}
	})
}

func TestGetTaskStatus(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != "GET" {
				t.Errorf("Method = %v, want GET", r.Method)
			}

			externalID := r.URL.Query().Get("externalId")
			if externalID != "ext-123" {
				t.Errorf("externalId = %v, want ext-123", externalID)
			}

			w.WriteHeader(200)
			w.Write([]byte(`{
				"status": "ok",
				"data": {
					"id": 123,
					"status": "approved",
					"externalId": "ext-123",
					"decidedBy": "admin/approver1",
					"decidedTime": "2026-11-15T11:00:00Z",
					"comment": "approved"
				}
			}`))
		}))
		defer server.Close()

		client := NewClient(Config{
			Enabled:      true,
			Endpoint:     server.URL,
			ClientID:     "test-client",
			ClientSecret: "test-secret",
		})

		task, err := client.GetTaskStatus(context.Background(), "ext-123")
		if err != nil {
			t.Fatalf("GetTaskStatus() error = %v", err)
		}

		if task.Status != "approved" {
			t.Errorf("Task.Status = %v, want approved", task.Status)
		}
		if task.DecidedBy != "admin/approver1" {
			t.Errorf("Task.DecidedBy = %v, want admin/approver1", task.DecidedBy)
		}
	})

	t.Run("not found", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(404)
		}))
		defer server.Close()

		client := NewClient(Config{
			Enabled:      true,
			Endpoint:     server.URL,
			ClientID:     "test-client",
			ClientSecret: "test-secret",
		})

		task, err := client.GetTaskStatus(context.Background(), "not-exist")
		if err != nil {
			t.Fatalf("GetTaskStatus() error = %v", err)
		}
		if task != nil {
			t.Error("GetTaskStatus() expected nil on 404")
		}
	})
}

func TestCancelTask(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			t.Errorf("Method = %v, want POST", r.Method)
		}

		w.WriteHeader(200)
		w.Write([]byte(`{"status":"ok"}`))
	}))
	defer server.Close()

	client := NewClient(Config{
		Enabled:      true,
		Endpoint:     server.URL,
		ClientID:     "test-client",
		ClientSecret: "test-secret",
	})

	err := client.CancelTask(context.Background(), "ext-123")
	if err != nil {
		t.Fatalf("CancelTask() error = %v", err)
	}
}

func TestBuildConsoleURL(t *testing.T) {
	client := NewClient(Config{
		Endpoint: "https://casdoor.test",
	})

	url := client.BuildConsoleURL(123)
	expected := "https://casdoor.test/management/approval-tasks?taskId=123"

	if url != expected {
		t.Errorf("BuildConsoleURL() = %v, want %v", url, expected)
	}
}

func TestAutoFillFields(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req CreateTaskRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatal(err)
		}

		// Verify auto-filled fields
		if req.Application != "test-client" {
			t.Errorf("Application = %v, want test-client", req.Application)
		}
		if req.Owner != "custom-owner" {
			t.Errorf("Owner = %v, want custom-owner", req.Owner)
		}
		if req.CallbackURL != "https://test.com/callback" {
			t.Errorf("CallbackURL = %v, want https://test.com/callback", req.CallbackURL)
		}
		if req.ExpiresAt == "" {
			t.Error("ExpiresAt should be auto-filled")
		}
		if req.Steps != "@admin/default-flow" {
			t.Errorf("Steps = %v, want @admin/default-flow", req.Steps)
		}

		w.WriteHeader(200)
		w.Write([]byte(`{"status":"ok","data":{"id":1}}`))
	}))
	defer server.Close()

	client := NewClient(Config{
		Enabled:      true,
		Endpoint:     server.URL,
		ClientID:     "test-client",
		ClientSecret: "test-secret",
		CallbackURL:  "https://test.com/callback",
		TaskOwner:    "admin",
		DefaultFlow:  "@admin/default-flow",
		TTLMinutes:   30,
	})

	req := &CreateTaskRequest{
		Owner:        "custom-owner",
		ExternalID:   "ext-123",
		Applicant:    "admin/user1",
		Type:         "test",
		ResourceType: "session",
		ResourceID:   "sess-123",
		Title:        "Test",
	}

	_, err := client.CreateTask(context.Background(), req)
	if err != nil {
		t.Fatalf("CreateTask() error = %v", err)
	}
}
