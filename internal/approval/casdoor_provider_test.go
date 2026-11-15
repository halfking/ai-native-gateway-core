package approval

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/internal/casdoor"
)

// MockCasdoorClient implements casdoor.Client interface for testing.
type MockCasdoorClient struct {
	createTaskFunc      func(ctx context.Context, req *casdoor.CreateTaskRequest) (*casdoor.Task, error)
	getTaskStatusFunc   func(ctx context.Context, externalID string) (*casdoor.Task, error)
	cancelTaskFunc      func(ctx context.Context, externalID string) error
	buildConsoleURLFunc func(taskID int) string
	isEnabled           bool
}

func (m *MockCasdoorClient) CreateTask(ctx context.Context, req *casdoor.CreateTaskRequest) (*casdoor.Task, error) {
	if m.createTaskFunc != nil {
		return m.createTaskFunc(ctx, req)
	}
	return &casdoor.Task{
		ID:         123,
		Status:     "pending",
		ExternalID: req.ExternalID,
	}, nil
}

func (m *MockCasdoorClient) GetTaskStatus(ctx context.Context, externalID string) (*casdoor.Task, error) {
	if m.getTaskStatusFunc != nil {
		return m.getTaskStatusFunc(ctx, externalID)
	}
	return nil, nil
}

func (m *MockCasdoorClient) CancelTask(ctx context.Context, externalID string) error {
	if m.cancelTaskFunc != nil {
		return m.cancelTaskFunc(ctx, externalID)
	}
	return nil
}

func (m *MockCasdoorClient) BuildConsoleURL(taskID int) string {
	if m.buildConsoleURLFunc != nil {
		return m.buildConsoleURLFunc(taskID)
	}
	return "https://casdoor.test/tasks/" + string(rune(taskID))
}

func (m *MockCasdoorClient) IsEnabled() bool {
	return m.isEnabled
}

// MockApprovalStore implements ApprovalStore interface for testing.
type MockApprovalStore struct {
	data map[string]*Approval
}

func NewMockApprovalStore() *MockApprovalStore {
	return &MockApprovalStore{
		data: make(map[string]*Approval),
	}
}

func (m *MockApprovalStore) Save(ctx context.Context, approval *Approval) error {
	m.data[approval.ID] = approval
	return nil
}

func (m *MockApprovalStore) Get(ctx context.Context, requestID string) (*Approval, error) {
	approval, ok := m.data[requestID]
	if !ok {
		return nil, nil
	}
	return approval, nil
}

func (m *MockApprovalStore) Update(ctx context.Context, approval *Approval) error {
	if _, ok := m.data[approval.ID]; !ok {
		return errors.New("not found")
	}
	m.data[approval.ID] = approval
	return nil
}

func TestCasdoorProviderName(t *testing.T) {
	provider := &CasdoorProvider{}
	if provider.Name() != "casdoor" {
		t.Errorf("Name() = %v, want casdoor", provider.Name())
	}
}

func TestCasdoorProviderIsEnabled(t *testing.T) {
	t.Run("enabled when client is enabled", func(t *testing.T) {
		client := &MockCasdoorClient{isEnabled: true}
		provider := NewCasdoorProvider(client, nil)
		if !provider.IsEnabled() {
			t.Error("IsEnabled() = false, want true")
		}
	})

	t.Run("disabled when client is disabled", func(t *testing.T) {
		client := &MockCasdoorClient{isEnabled: false}
		provider := NewCasdoorProvider(client, nil)
		if provider.IsEnabled() {
			t.Error("IsEnabled() = true, want false")
		}
	})

	t.Run("disabled when client is nil", func(t *testing.T) {
		provider := NewCasdoorProvider(nil, nil)
		if provider.IsEnabled() {
			t.Error("IsEnabled() = true, want false when client is nil")
		}
	})
}

func TestCasdoorProviderCreateApproval(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		client := &MockCasdoorClient{
			isEnabled: true,
			createTaskFunc: func(ctx context.Context, req *casdoor.CreateTaskRequest) (*casdoor.Task, error) {
				// Verify request fields
				if req.Type != "sensitive_content" {
					t.Errorf("Type = %v, want sensitive_content", req.Type)
				}
				if req.ResourceType != "llm_session" {
					t.Errorf("ResourceType = %v, want llm_session", req.ResourceType)
				}
				if req.ExternalID != "sess-123:req-456" {
					t.Errorf("ExternalID = %v, want sess-123:req-456", req.ExternalID)
				}

				// Verify payload contains request data
				var payload map[string]interface{}
				json.Unmarshal([]byte(req.Payload), &payload)
				if payload["requestId"] != "req-456" {
					t.Errorf("Payload requestId = %v, want req-456", payload["requestId"])
				}

				return &casdoor.Task{
					ID:         789,
					Status:     "pending",
					ExternalID: req.ExternalID,
				}, nil
			},
		}

		store := NewMockApprovalStore()
		provider := NewCasdoorProvider(client, store)

		req := &CreateApprovalRequest{
			RequestID:     "req-456",
			SessionID:     "sess-123",
			TenantID:      "tenant-1",
			UserID:        "user-1",
			CasdoorUserID: "admin/user1",
			DetectResult: &DetectResult{
				Summary: "test detection",
			},
			CreatedAt: time.Now(),
		}

		approval, err := provider.CreateApproval(context.Background(), req)
		if err != nil {
			t.Fatalf("CreateApproval() error = %v", err)
		}

		if approval.ID != "req-456" {
			t.Errorf("Approval.ID = %v, want req-456", approval.ID)
		}
		if approval.Status != "pending" {
			t.Errorf("Approval.Status = %v, want pending", approval.Status)
		}
		if approval.Provider != "casdoor" {
			t.Errorf("Approval.Provider = %v, want casdoor", approval.Provider)
		}
		if approval.CasdoorTaskID != 789 {
			t.Errorf("Approval.CasdoorTaskID = %v, want 789", approval.CasdoorTaskID)
		}

		// Verify stored in store
		stored, _ := store.Get(context.Background(), "req-456")
		if stored == nil {
			t.Error("Approval not saved to store")
		}
	})

	t.Run("client error", func(t *testing.T) {
		client := &MockCasdoorClient{
			isEnabled: true,
			createTaskFunc: func(ctx context.Context, req *casdoor.CreateTaskRequest) (*casdoor.Task, error) {
				return nil, errors.New("casdoor api error")
			},
		}

		store := NewMockApprovalStore()
		provider := NewCasdoorProvider(client, store)

		req := &CreateApprovalRequest{
			RequestID: "req-456",
			SessionID: "sess-123",
			TenantID:  "tenant-1",
			CreatedAt: time.Now(),
		}

		_, err := provider.CreateApproval(context.Background(), req)
		if err == nil {
			t.Error("CreateApproval() expected error")
		}
	})

	t.Run("provider not enabled", func(t *testing.T) {
		client := &MockCasdoorClient{isEnabled: false}
		provider := NewCasdoorProvider(client, nil)

		req := &CreateApprovalRequest{
			RequestID: "req-456",
			CreatedAt: time.Now(),
		}

		_, err := provider.CreateApproval(context.Background(), req)
		if err == nil {
			t.Error("CreateApproval() expected error when not enabled")
		}
	})
}

func TestCasdoorProviderHandleWebhook(t *testing.T) {
	t.Run("approved webhook", func(t *testing.T) {
		store := NewMockApprovalStore()
		client := &MockCasdoorClient{isEnabled: true}
		provider := NewCasdoorProvider(client, store)

		// Pre-populate store with pending approval
		createdAt := time.Now().Add(-10 * time.Minute)
		store.Save(context.Background(), &Approval{
			ID:            "req-456",
			SessionID:     "sess-123",
			TenantID:      "tenant-1",
			Status:        "pending",
			Provider:      "casdoor",
			CasdoorTaskID: 789,
			CreatedAt:     createdAt,
		})

		event := &casdoor.WebhookEvent{
			EventID:    "evt-123",
			TaskID:     789,
			ExternalID: "sess-123:req-456",
			Status:     "approved",
			Actor:      "admin/approver1",
			Comment:    "looks good",
		}

		err := provider.HandleWebhook(context.Background(), event)
		if err != nil {
			t.Fatalf("HandleWebhook() error = %v", err)
		}

		// Verify approval updated
		approval, _ := store.Get(context.Background(), "req-456")
		if approval.Status != "approved" {
			t.Errorf("Approval.Status = %v, want approved", approval.Status)
		}
		if approval.ApprovedBy != "admin/approver1" {
			t.Errorf("Approval.ApprovedBy = %v, want admin/approver1", approval.ApprovedBy)
		}
		if approval.ApprovedAt == nil {
			t.Error("Approval.ApprovedAt should be set")
		}
	})

	t.Run("rejected webhook", func(t *testing.T) {
		store := NewMockApprovalStore()
		client := &MockCasdoorClient{isEnabled: true}
		provider := NewCasdoorProvider(client, store)

		store.Save(context.Background(), &Approval{
			ID:        "req-789",
			SessionID: "sess-456",
			Status:    "pending",
			Provider:  "casdoor",
			CreatedAt: time.Now(),
		})

		event := &casdoor.WebhookEvent{
			ExternalID: "sess-456:req-789",
			Status:     "rejected",
			Actor:      "admin/approver1",
			Comment:    "not approved",
		}

		err := provider.HandleWebhook(context.Background(), event)
		if err != nil {
			t.Fatalf("HandleWebhook() error = %v", err)
		}

		approval, _ := store.Get(context.Background(), "req-789")
		if approval.Status != "rejected" {
			t.Errorf("Approval.Status = %v, want rejected", approval.Status)
		}
		if approval.Reason != "not approved" {
			t.Errorf("Approval.Reason = %v, want 'not approved'", approval.Reason)
		}
	})

	t.Run("idempotency - already processed", func(t *testing.T) {
		store := NewMockApprovalStore()
		client := &MockCasdoorClient{isEnabled: true}
		provider := NewCasdoorProvider(client, store)

		// Already approved
		approvedTime := time.Now()
		store.Save(context.Background(), &Approval{
			ID:         "req-999",
			SessionID:  "sess-999",
			Status:     "approved",
			Provider:   "casdoor",
			ApprovedBy: "admin/approver1",
			ApprovedAt: &approvedTime,
			CreatedAt:  time.Now(),
		})

		event := &casdoor.WebhookEvent{
			ExternalID: "sess-999:req-999",
			Status:     "rejected",
			Actor:      "admin/approver2",
		}

		// Should not error, just skip
		err := provider.HandleWebhook(context.Background(), event)
		if err != nil {
			t.Fatalf("HandleWebhook() error = %v", err)
		}

		// Status should remain approved
		approval, _ := store.Get(context.Background(), "req-999")
		if approval.Status != "approved" {
			t.Errorf("Approval.Status = %v, want approved (unchanged)", approval.Status)
		}
		if approval.ApprovedBy != "admin/approver1" {
			t.Errorf("Approval.ApprovedBy = %v, want admin/approver1 (unchanged)", approval.ApprovedBy)
		}
	})

	t.Run("invalid externalId format", func(t *testing.T) {
		store := NewMockApprovalStore()
		client := &MockCasdoorClient{isEnabled: true}
		provider := NewCasdoorProvider(client, store)

		event := &casdoor.WebhookEvent{
			ExternalID: "invalid-format",
			Status:     "approved",
		}

		err := provider.HandleWebhook(context.Background(), event)
		if err == nil {
			t.Error("HandleWebhook() expected error for invalid externalId")
		}
	})

	t.Run("approval not found", func(t *testing.T) {
		store := NewMockApprovalStore()
		client := &MockCasdoorClient{isEnabled: true}
		provider := NewCasdoorProvider(client, store)

		event := &casdoor.WebhookEvent{
			ExternalID: "sess-123:req-not-exist",
			Status:     "approved",
		}

		err := provider.HandleWebhook(context.Background(), event)
		if err == nil {
			t.Error("HandleWebhook() expected error when approval not found")
		}
	})

	t.Run("cancelled webhook", func(t *testing.T) {
		store := NewMockApprovalStore()
		client := &MockCasdoorClient{isEnabled: true}
		provider := NewCasdoorProvider(client, store)

		store.Save(context.Background(), &Approval{
			ID:        "req-cancel",
			SessionID: "sess-cancel",
			Status:    "pending",
			Provider:  "casdoor",
			CreatedAt: time.Now(),
		})

		event := &casdoor.WebhookEvent{
			ExternalID: "sess-cancel:req-cancel",
			Status:     "cancelled",
			Comment:    "applicant cancelled",
		}

		err := provider.HandleWebhook(context.Background(), event)
		if err != nil {
			t.Fatalf("HandleWebhook() error = %v", err)
		}

		approval, _ := store.Get(context.Background(), "req-cancel")
		if approval.Status != "cancelled" {
			t.Errorf("Approval.Status = %v, want cancelled", approval.Status)
		}
	})
}

func TestCasdoorProviderGetApproval(t *testing.T) {
	t.Run("found in store", func(t *testing.T) {
		store := NewMockApprovalStore()
		client := &MockCasdoorClient{isEnabled: true}
		provider := NewCasdoorProvider(client, store)

		expected := &Approval{
			ID:        "req-123",
			SessionID: "sess-123",
			Status:    "approved",
			Provider:  "casdoor",
			CreatedAt: time.Now(),
		}
		store.Save(context.Background(), expected)

		approval, err := provider.GetApproval(context.Background(), "req-123")
		if err != nil {
			t.Fatalf("GetApproval() error = %v", err)
		}
		if approval.ID != "req-123" {
			t.Errorf("Approval.ID = %v, want req-123", approval.ID)
		}
	})

	t.Run("not found", func(t *testing.T) {
		store := NewMockApprovalStore()
		client := &MockCasdoorClient{isEnabled: true}
		provider := NewCasdoorProvider(client, store)

		approval, err := provider.GetApproval(context.Background(), "not-exist")
		if err != nil {
			t.Fatalf("GetApproval() error = %v", err)
		}
		if approval != nil {
			t.Error("GetApproval() expected nil for not found")
		}
	})
}

func TestCasdoorProviderCancelApproval(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		store := NewMockApprovalStore()
		client := &MockCasdoorClient{
			isEnabled: true,
			cancelTaskFunc: func(ctx context.Context, externalID string) error {
				if externalID != "sess-123:req-456" {
					t.Errorf("CancelTask externalID = %v, want sess-123:req-456", externalID)
				}
				return nil
			},
		}
		provider := NewCasdoorProvider(client, store)

		store.Save(context.Background(), &Approval{
			ID:        "req-456",
			SessionID: "sess-123",
			Status:    "pending",
			Provider:  "casdoor",
			CreatedAt: time.Now(),
		})

		err := provider.CancelApproval(context.Background(), "req-456")
		if err != nil {
			t.Fatalf("CancelApproval() error = %v", err)
		}

		// Verify status updated
		approval, _ := store.Get(context.Background(), "req-456")
		if approval.Status != "cancelled" {
			t.Errorf("Approval.Status = %v, want cancelled", approval.Status)
		}
	})

	t.Run("not found", func(t *testing.T) {
		store := NewMockApprovalStore()
		client := &MockCasdoorClient{isEnabled: true}
		provider := NewCasdoorProvider(client, store)

		err := provider.CancelApproval(context.Background(), "not-exist")
		if err == nil {
			t.Error("CancelApproval() expected error for not found")
		}
	})

	t.Run("already resolved", func(t *testing.T) {
		store := NewMockApprovalStore()
		client := &MockCasdoorClient{isEnabled: true}
		provider := NewCasdoorProvider(client, store)

		store.Save(context.Background(), &Approval{
			ID:        "req-done",
			SessionID: "sess-done",
			Status:    "approved",
			Provider:  "casdoor",
			CreatedAt: time.Now(),
		})

		// Should not error, just no-op
		err := provider.CancelApproval(context.Background(), "req-done")
		if err != nil {
			t.Fatalf("CancelApproval() error = %v", err)
		}

		// Status should remain approved
		approval, _ := store.Get(context.Background(), "req-done")
		if approval.Status != "approved" {
			t.Errorf("Approval.Status = %v, want approved (unchanged)", approval.Status)
		}
	})
}
