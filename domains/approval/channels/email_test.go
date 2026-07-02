package channels

import (
	"context"
	"fmt"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/approval"
)

// mockSMTPServer simulates an SMTP server for testing.
type mockSMTPServer struct {
	listener      net.Listener
	receivedMails []receivedMail
	mu            sync.Mutex
	addr          string
}

type receivedMail struct {
	from    string
	to      []string
	data    string
	rawData string
}

// newMockSMTPServer creates a new mock SMTP server.
func newMockSMTPServer() (*mockSMTPServer, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}

	server := &mockSMTPServer{
		listener: listener,
		addr:     listener.Addr().String(),
	}

	go server.serve()

	return server, nil
}

// serve handles incoming SMTP connections.
func (s *mockSMTPServer) serve() {
	for {
		conn, err := s.listener.Accept()
		if err != nil {
			return
		}
		go s.handleConnection(conn)
	}
}

// handleConnection handles a single SMTP connection.
func (s *mockSMTPServer) handleConnection(conn net.Conn) {
	defer conn.Close()

	var mail receivedMail
	var inData bool
	var dataBuffer strings.Builder

	// Send greeting
	conn.Write([]byte("220 Mock SMTP Server\r\n"))

	buf := make([]byte, 1024)
	for {
		n, err := conn.Read(buf)
		if err != nil {
			return
		}

		line := string(buf[:n])

		if inData {
			dataBuffer.WriteString(line)
			if strings.Contains(line, "\r\n.\r\n") {
				mail.data = dataBuffer.String()
				mail.rawData = dataBuffer.String()
				s.mu.Lock()
				s.receivedMails = append(s.receivedMails, mail)
				s.mu.Unlock()
				conn.Write([]byte("250 OK\r\n"))
				inData = false
				dataBuffer.Reset()
			}
			continue
		}

		cmd := strings.ToUpper(strings.TrimSpace(line))

		switch {
		case strings.HasPrefix(cmd, "EHLO") || strings.HasPrefix(cmd, "HELO"):
			conn.Write([]byte("250 OK\r\n"))
		case strings.HasPrefix(cmd, "MAIL FROM:"):
			mail.from = extractEmail(line)
			conn.Write([]byte("250 OK\r\n"))
		case strings.HasPrefix(cmd, "RCPT TO:"):
			mail.to = append(mail.to, extractEmail(line))
			conn.Write([]byte("250 OK\r\n"))
		case cmd == "DATA":
			conn.Write([]byte("354 Start mail input\r\n"))
			inData = true
		case cmd == "QUIT":
			conn.Write([]byte("221 Bye\r\n"))
			return
		default:
			conn.Write([]byte("250 OK\r\n"))
		}
	}
}

// extractEmail extracts email address from SMTP command.
func extractEmail(line string) string {
	start := strings.Index(line, "<")
	end := strings.Index(line, ">")
	if start != -1 && end != -1 {
		return line[start+1 : end]
	}
	return ""
}

// close shuts down the mock server.
func (s *mockSMTPServer) close() {
	s.listener.Close()
}

// getReceivedMails returns all received emails.
func (s *mockSMTPServer) getReceivedMails() []receivedMail {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]receivedMail{}, s.receivedMails...)
}

func TestEmailChannel_SendApprovalNotification(t *testing.T) {
	// Start mock SMTP server
	server, err := newMockSMTPServer()
	if err != nil {
		t.Fatalf("failed to start mock SMTP server: %v", err)
	}
	defer server.close()

	// Parse server address
	host, portStr, _ := net.SplitHostPort(server.addr)
	var port int
	fmt.Sscanf(portStr, "%d", &port)

	// Create email channel
	channel := NewEmailChannel(
		host,
		port,
		"",
		"",
		"noreply@example.com",
		"https://approval.example.com",
	)

	// Create test approval request
	req := &approval.ApprovalRequest{
		RequestID:     "req-123",
		SessionID:     "sess-456",
		TenantID:      "tenant-789",
		TriggerType:   approval.TriggerSensitiveContent,
		TriggerReason: "Detected PII in message",
		RiskLevel:     approval.RiskHigh,
		UserMessage:   "My SSN is 123-45-6789",
		EstimatedCost: 0.05,
		EstimatedTokens: 500,
		Status:        approval.StatusPending,
		CreatedAt:     time.Now(),
		ExpiresAt:     time.Now().Add(1 * time.Hour),
		SensitiveInfo: []approval.SensitiveItemSummary{
			{
				Type:       "PII",
				Content:    "***-**-6789",
				Location:   "message[0].content",
				Confidence: 0.95,
			},
		},
	}

	approvers := []approval.Approver{
		{
			UserID:  "user-1",
			Name:    "John Doe",
			Email:   "john@example.com",
			Role:    "admin",
			Enabled: true,
		},
		{
			UserID:  "user-2",
			Name:    "Jane Smith",
			Email:   "jane@example.com",
			Role:    "manager",
			Enabled: true,
		},
	}

	// Send notification
	ctx := context.Background()
	err = channel.SendApprovalNotification(ctx, req, approvers)
	if err != nil {
		t.Fatalf("failed to send notification: %v", err)
	}

	// Wait for email to be received
	time.Sleep(100 * time.Millisecond)

	// Verify email was sent
	mails := server.getReceivedMails()
	if len(mails) == 0 {
		t.Fatal("no emails received")
	}

	mail := mails[0]

	// Verify sender
	if mail.from != "noreply@example.com" {
		t.Errorf("expected sender noreply@example.com, got %s", mail.from)
	}

	// Verify recipients
	expectedRecipients := []string{"john@example.com", "jane@example.com"}
	if len(mail.to) != len(expectedRecipients) {
		t.Errorf("expected %d recipients, got %d", len(expectedRecipients), len(mail.to))
	}

	// Verify email content
	if !strings.Contains(mail.data, "审批请求") {
		t.Error("email should contain '审批请求'")
	}
	if !strings.Contains(mail.data, req.RequestID) {
		t.Error("email should contain request ID")
	}
	if !strings.Contains(mail.data, req.SessionID) {
		t.Error("email should contain session ID")
	}
	if !strings.Contains(mail.data, "HIGH") {
		t.Error("email should contain risk level")
	}
	if !strings.Contains(mail.data, "My SSN is 123-45-6789") {
		t.Error("email should contain user message")
	}

	// Verify HTML format
	if !strings.Contains(mail.data, "text/html") {
		t.Error("email should be HTML format")
	}
	if !strings.Contains(mail.data, "<html>") {
		t.Error("email should contain HTML tags")
	}

	// Verify action links
	if !strings.Contains(mail.data, "https://approval.example.com/approvals/req-123/approve") {
		t.Error("email should contain approve link")
	}
	if !strings.Contains(mail.data, "https://approval.example.com/approvals/req-123/reject") {
		t.Error("email should contain reject link")
	}
}

func TestEmailChannel_NoApprovers(t *testing.T) {
	channel := NewEmailChannel(
		"localhost",
		25,
		"",
		"",
		"noreply@example.com",
		"https://approval.example.com",
	)

	req := &approval.ApprovalRequest{
		RequestID: "req-123",
	}

	err := channel.SendApprovalNotification(context.Background(), req, []approval.Approver{})
	if err == nil {
		t.Error("expected error when no approvers provided")
	}
	if !strings.Contains(err.Error(), "no approvers") {
		t.Errorf("unexpected error message: %v", err)
	}
}

func TestEmailChannel_NoEmailRecipients(t *testing.T) {
	channel := NewEmailChannel(
		"localhost",
		25,
		"",
		"",
		"noreply@example.com",
		"https://approval.example.com",
	)

	req := &approval.ApprovalRequest{
		RequestID: "req-123",
	}

	// Approvers without email addresses
	approvers := []approval.Approver{
		{
			UserID:  "user-1",
			Name:    "John Doe",
			Email:   "",
			Enabled: true,
		},
		{
			UserID:  "user-2",
			Name:    "Jane Smith",
			Email:   "",
			Enabled: true,
		},
	}

	err := channel.SendApprovalNotification(context.Background(), req, approvers)
	if err == nil {
		t.Error("expected error when no email recipients")
	}
	if !strings.Contains(err.Error(), "no valid email recipients") {
		t.Errorf("unexpected error message: %v", err)
	}
}

func TestEmailChannel_DisabledApprovers(t *testing.T) {
	channel := NewEmailChannel(
		"localhost",
		25,
		"",
		"",
		"noreply@example.com",
		"https://approval.example.com",
	)

	req := &approval.ApprovalRequest{
		RequestID: "req-123",
	}

	// All approvers disabled
	approvers := []approval.Approver{
		{
			UserID:  "user-1",
			Name:    "John Doe",
			Email:   "john@example.com",
			Enabled: false,
		},
	}

	err := channel.SendApprovalNotification(context.Background(), req, approvers)
	if err == nil {
		t.Error("expected error when all approvers disabled")
	}
}

func TestEmailChannel_GenerateSubject(t *testing.T) {
	channel := NewEmailChannel(
		"localhost",
		25,
		"",
		"",
		"noreply@example.com",
		"https://approval.example.com",
	)

	tests := []struct {
		name      string
		req       *approval.ApprovalRequest
		wantParts []string
	}{
		{
			name: "high risk",
			req: &approval.ApprovalRequest{
				SessionID: "sess-123",
				RiskLevel: approval.RiskHigh,
			},
			wantParts: []string{"审批请求", "HIGH", "sess-123"},
		},
		{
			name: "low risk",
			req: &approval.ApprovalRequest{
				SessionID: "sess-456",
				RiskLevel: approval.RiskLow,
			},
			wantParts: []string{"审批请求", "LOW", "sess-456"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			subject := channel.generateSubject(tt.req)
			for _, part := range tt.wantParts {
				if !strings.Contains(subject, part) {
					t.Errorf("subject should contain %q, got: %s", part, subject)
				}
			}
		})
	}
}

func TestEmailChannel_GenerateHTMLBody(t *testing.T) {
	channel := NewEmailChannel(
		"localhost",
		25,
		"",
		"",
		"noreply@example.com",
		"https://approval.example.com",
	)

	req := &approval.ApprovalRequest{
		RequestID:       "req-123",
		SessionID:       "sess-456",
		TenantID:        "tenant-789",
		TriggerType:     approval.TriggerSensitiveContent,
		TriggerReason:   "Detected sensitive data",
		RiskLevel:       approval.RiskCritical,
		UserMessage:     "Test message",
		EstimatedCost:   0.10,
		EstimatedTokens: 1000,
		CreatedAt:       time.Now(),
		ExpiresAt:       time.Now().Add(1 * time.Hour),
	}

	body, err := channel.generateHTMLBody(req)
	if err != nil {
		t.Fatalf("failed to generate HTML body: %v", err)
	}

	// Verify HTML structure
	requiredElements := []string{
		"<!DOCTYPE html>",
		"<html>",
		"<head>",
		"<body>",
		"req-123",
		"sess-456",
		"tenant-789",
		"Test message",
		"CRITICAL",
		"https://approval.example.com/approvals/req-123/approve",
		"https://approval.example.com/approvals/req-123/reject",
	}

	for _, element := range requiredElements {
		if !strings.Contains(body, element) {
			t.Errorf("HTML body should contain %q", element)
		}
	}
}

func TestEmailChannel_GetRiskColor(t *testing.T) {
	tests := []struct {
		level approval.RiskLevel
		want  string
	}{
		{approval.RiskLow, "#28a745"},
		{approval.RiskMedium, "#ffc107"},
		{approval.RiskHigh, "#fd7e14"},
		{approval.RiskCritical, "#dc3545"},
	}

	for _, tt := range tests {
		t.Run(string(tt.level), func(t *testing.T) {
			got := getRiskColor(tt.level)
			if got != tt.want {
				t.Errorf("getRiskColor(%s) = %s, want %s", tt.level, got, tt.want)
			}
		})
	}
}

func TestEmailChannel_GetTriggerName(t *testing.T) {
	tests := []struct {
		trigger approval.ApprovalTriggerType
		want    string
	}{
		{approval.TriggerSensitiveContent, "敏感内容检测"},
		{approval.TriggerHighCost, "高成本预警"},
		{approval.TriggerToolCall, "工具调用"},
		{approval.TriggerPolicyMatch, "策略匹配"},
		{approval.TriggerManualMode, "手动审批模式"},
	}

	for _, tt := range tests {
		t.Run(string(tt.trigger), func(t *testing.T) {
			got := getTriggerName(tt.trigger)
			if got != tt.want {
				t.Errorf("getTriggerName(%s) = %s, want %s", tt.trigger, got, tt.want)
			}
		})
	}
}
