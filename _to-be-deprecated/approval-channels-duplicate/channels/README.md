# Approval Notification Channels

This package provides notification channel implementations for the approval workflow system.

## Supported Channels

### 1. Email Channel

Sends HTML email notifications via SMTP.

**Features:**
- HTML formatted emails with approval details
- Action buttons (Approve/Reject) linking to web UI
- Risk level color coding
- Sensitive information display
- TLS/STARTTLS support

**Usage:**
```go
channel := channels.NewEmailChannel(
    "smtp.example.com",  // SMTP host
    587,                 // SMTP port
    "user@example.com",  // SMTP user
    "password",          // SMTP password
    "noreply@example.com", // From address
    "https://approval.example.com", // Base URL for approval UI
)

err := channel.SendApprovalNotification(ctx, approvalRequest, approvers)
```

### 2. Webhook Channel

Sends approval notifications to custom HTTP endpoints with HMAC signature verification.

**Features:**
- JSON payload with complete approval request data
- HMAC-SHA256 signature for authenticity verification
- Automatic retry with exponential backoff (3 retries)
- 30-second timeout
- Rate limit handling (429 status code)

**Usage:**
```go
channel := channels.NewWebhookChannel(
    "https://api.example.com/webhooks/approval", // Webhook URL
    "your-secret-key", // Secret for HMAC signing
)

err := channel.SendApprovalNotification(ctx, approvalRequest, approvers)
```

**Webhook Payload Structure:**
```json
{
  "event": "approval.created",
  "timestamp": 1234567890,
  "request": {
    "request_id": "req-123",
    "session_id": "sess-456",
    "tenant_id": "tenant-789",
    "trigger_type": "sensitive_content",
    "trigger_reason": "Detected PII in message",
    "risk_level": "HIGH",
    "user_message": "...",
    "estimated_cost": 0.05,
    "estimated_tokens": 500,
    "status": "pending",
    "created_at": "2026-07-03T00:00:00Z",
    "expires_at": "2026-07-03T01:00:00Z"
  },
  "approvers": [
    {
      "user_id": "user-1",
      "name": "John Doe",
      "email": "john@example.com",
      "role": "admin",
      "priority": 1,
      "enabled": true
    }
  ]
}
```

**Webhook Signature Verification:**

Recipients should verify the webhook signature using the `X-Webhook-Signature` header:

```go
import "github.com/kaixuan/llm-gateway-go/domains/approval/channels"

// In your webhook handler
payload, _ := ioutil.ReadAll(r.Body)
signature := r.Header.Get("X-Webhook-Signature")
secret := "your-secret-key"

if !channels.VerifyWebhookSignature(payload, signature, secret) {
    http.Error(w, "Invalid signature", http.StatusUnauthorized)
    return
}

// Process the webhook...
```

## Configuration Options

### Email Channel

- **SetTimeout**: Configure SMTP connection timeout (not implemented, uses default)
- **TLS/STARTTLS**: Automatically negotiated based on server capabilities

### Webhook Channel

- **SetMaxRetries**: Configure maximum retry attempts (default: 3)
- **SetTimeout**: Configure HTTP client timeout (default: 30s)

```go
channel := channels.NewWebhookChannel(url, secret)
channel.SetMaxRetries(5)
channel.SetTimeout(60 * time.Second)
```

## Error Handling

### Email Channel

- Returns error if no approvers provided
- Returns error if no valid email recipients found
- Returns error on SMTP connection/authentication failures

### Webhook Channel

- Retries on server errors (5xx) and rate limiting (429)
- Does not retry on client errors (4xx except 429)
- Returns `*WebhookError` for HTTP errors
- Context cancellation errors are not retried

```go
err := channel.SendApprovalNotification(ctx, req, approvers)
if channels.IsWebhookError(err) {
    webhookErr := err.(*channels.WebhookError)
    log.Printf("Webhook failed with status %d: %s", webhookErr.StatusCode, webhookErr.Body)
}
```

## Testing

Both channels include comprehensive unit tests with mock servers:

```bash
# Run all channel tests
go test ./domains/approval/channels/...

# Run specific tests
go test ./domains/approval/channels/... -run TestEmailChannel
go test ./domains/approval/channels/... -run TestWebhookChannel
```

## Security Considerations

### Email Channel
- Supports TLS/STARTTLS for secure SMTP connections
- Redacts sensitive information in email content
- Uses proper email header formatting

### Webhook Channel
- HMAC-SHA256 signature prevents request tampering
- Recipients must verify signature before processing
- Signature includes the raw request body
- Use HTTPS URLs for webhook endpoints

## Future Enhancements

- [ ] Email template customization
- [ ] Async notification sending with queue
- [ ] Notification delivery status tracking
- [ ] Support for HTML email inline images
- [ ] Webhook payload customization
- [ ] Circuit breaker for webhook failures
