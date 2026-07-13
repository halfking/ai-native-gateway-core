package licensing

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"

	"github.com/kaixuan/llm-gateway-go/domains/notification"
)

// ActivationNotifier sends activation code to customer after offline approval.
type ActivationNotifier interface {
	NotifyApproved(ctx context.Context, lic *License, req *OfflineRequest, activationCode string) error
}

// LogActivationNotifier logs approval details (default fallback).
type LogActivationNotifier struct{}

func (n *LogActivationNotifier) NotifyApproved(_ context.Context, lic *License, req *OfflineRequest, activationCode string) error {
	slog.Info("offline activation approved",
		"license_key", req.LicenseKey,
		"request_id", req.RequestID,
		"customer_email", lic.CustomerEmail,
		"activation_code", activationCode,
	)
	return nil
}

// EmailActivationNotifier sends activation code via SMTP.
type EmailActivationNotifier struct {
	channel *notification.EmailChannel
}

func (n *EmailActivationNotifier) NotifyApproved(ctx context.Context, lic *License, req *OfflineRequest, activationCode string) error {
	if lic.CustomerEmail == "" {
		return fmt.Errorf("customer email is empty")
	}
	content := fmt.Sprintf(
		"您的离线激活请求已审批通过。\n\nLicense: %s\n设备: %s\n激活码: %s\n\n请在网关安装程序中输入激活码完成激活。",
		req.LicenseKey,
		req.DeviceName,
		activationCode,
	)
	return n.channel.Send(ctx, &notification.Message{
		Title:   "LLM Gateway 离线激活码",
		Content: content,
		Metadata: map[string]any{
			"to": lic.CustomerEmail,
		},
	})
}

// NewActivationNotifierFromEnv returns email notifier when SMTP env is configured, else log notifier.
func NewActivationNotifierFromEnv() ActivationNotifier {
	host := strings.TrimSpace(os.Getenv("LICENSE_AUTHORITY_SMTP_HOST"))
	if host == "" {
		return &LogActivationNotifier{}
	}

	port := 587
	if raw := strings.TrimSpace(os.Getenv("LICENSE_AUTHORITY_SMTP_PORT")); raw != "" {
		if p, err := strconv.Atoi(raw); err == nil && p > 0 {
			port = p
		}
	}

	useTLS := port == 465
	if raw := strings.TrimSpace(os.Getenv("LICENSE_AUTHORITY_SMTP_USE_TLS")); raw != "" {
		useTLS = strings.EqualFold(raw, "true") || raw == "1"
	}

	from := strings.TrimSpace(os.Getenv("LICENSE_AUTHORITY_SMTP_FROM"))
	if from == "" {
		slog.Warn("LICENSE_AUTHORITY_SMTP_FROM not set, using log notifier only")
		return &LogActivationNotifier{}
	}

	channel := notification.NewEmailChannel(notification.EmailConfig{
		Host:        host,
		Port:        port,
		Username:    os.Getenv("LICENSE_AUTHORITY_SMTP_USER"),
		Password:    os.Getenv("LICENSE_AUTHORITY_SMTP_PASS"),
		FromAddress: from,
		UseTLS:      useTLS,
	})
	return &EmailActivationNotifier{channel: channel}
}
