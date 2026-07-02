// Package notification — email.go
//
// EmailChannel 通过 SMTP 发送审批通知邮件。
// 实现 NotificationChannel 接口（ParseCallback 为空实现——邮件审批走 Web 链接，无 IM 回调）。
package notification

import (
	"bytes"
	"context"
	"crypto/tls"
	"fmt"
	"html/template"
	"net/smtp"
	"strings"
	"time"
)

// EmailConfig 邮件渠道配置。
type EmailConfig struct {
	Host        string // SMTP 主机
	Port        int    // SMTP 端口（通常 587）
	Username    string // SMTP 用户名
	Password    string // SMTP 密码
	FromAddress string // 发件人地址
	BaseURL     string // 审批 Web UI 基础 URL（用于构造批准/拒绝链接）
	UseTLS      bool   // 是否强制 TLS（端口 465 时为 true）
}

// EmailChannel 邮件通知渠道。
type EmailChannel struct {
	cfg EmailConfig
}

// NewEmailChannel 创建邮件渠道。
func NewEmailChannel(cfg EmailConfig) *EmailChannel {
	return &EmailChannel{cfg: cfg}
}

// Name 返回渠道标识。
func (c *EmailChannel) Name() string { return "email" }

// Send 发送纯文本邮件。
// Message.Metadata["to"] 指定收件人邮箱，多个用逗号分隔。
func (c *EmailChannel) Send(ctx context.Context, msg *Message) error {
	if c == nil {
		return fmt.Errorf("email channel: nil receiver")
	}
	to, ok := msg.Metadata["to"].(string)
	if !ok || to == "" {
		return fmt.Errorf("email channel: missing 'to' in message metadata")
	}
	recipients := splitAndTrim(to)
	body := fmt.Sprintf("Subject: %s\r\nContent-Type: text/plain; charset=UTF-8\r\n\r\n%s", msg.Title, msg.Content)
	return c.sendMail(ctx, recipients, []byte(body))
}

// SendCard 发送审批卡片邮件（HTML 格式，含批准/拒绝按钮链接）。
// 收件人从 card.Metadata["recipients"]（[]Recipient 或 []string）提取。
func (c *EmailChannel) SendCard(ctx context.Context, card *InteractiveCard) error {
	if c == nil {
		return fmt.Errorf("email channel: nil receiver")
	}
	if card == nil {
		return fmt.Errorf("email channel: nil card")
	}
	recipients := extractEmailRecipients(card.Metadata)
	if len(recipients) == 0 {
		return fmt.Errorf("email channel: no recipients resolved from card metadata")
	}

	html, err := c.renderCardHTML(card)
	if err != nil {
		return fmt.Errorf("email channel: render html: %w", err)
	}

	subject := card.Header.Title
	if subject == "" {
		subject = "审批通知"
	}
	body := fmt.Sprintf("Subject: %s\r\nContent-Type: text/html; charset=UTF-8\r\n\r\n%s", subject, html)
	return c.sendMail(ctx, recipients, []byte(body))
}

// ParseCallback 邮件渠道不支持回调（审批通过 Web 链接完成）。
func (c *EmailChannel) ParseCallback(_ context.Context, _ []byte) (*Callback, error) {
	return nil, fmt.Errorf("email channel: callback not supported (use web link)")
}

// HealthCheck 验证 SMTP 连接。
func (c *EmailChannel) HealthCheck(ctx context.Context) error {
	if c == nil {
		return fmt.Errorf("email channel: nil receiver")
	}
	addr := fmt.Sprintf("%s:%d", c.cfg.Host, c.cfg.Port)
	var cl *smtp.Client
	var err error
	if c.cfg.UseTLS {
		tlsCfg := &tls.Config{ServerName: c.cfg.Host}
		conn, derr := tls.Dial("tcp", addr, tlsCfg)
		if derr != nil {
			return fmt.Errorf("email healthcheck: tls dial %s: %w", addr, derr)
		}
		cl, err = smtp.NewClient(conn, c.cfg.Host)
	} else {
		cl, err = smtp.Dial(addr)
	}
	if err != nil {
		return fmt.Errorf("email healthcheck: dial %s: %w", addr, err)
	}
	defer cl.Close()
	if err := cl.Hello("gateway-healthcheck"); err != nil {
		return fmt.Errorf("email healthcheck: hello: %w", err)
	}
	return nil
}

// sendMail 发送邮件（支持超时控制）。
func (c *EmailChannel) sendMail(ctx context.Context, to []string, body []byte) error {
	addr := fmt.Sprintf("%s:%d", c.cfg.Host, c.cfg.Port)
	var auth smtp.Auth
	if c.cfg.Username != "" {
		auth = smtp.PlainAuth("", c.cfg.Username, c.cfg.Password, c.cfg.Host)
	}

	done := make(chan error, 1)
	go func() {
		done <- smtp.SendMail(addr, auth, c.cfg.FromAddress, to, body)
	}()

	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		return fmt.Errorf("email channel: send timeout: %w", ctx.Err())
	}
}

// renderCardHTML 将审批卡片渲染为 HTML 邮件。
func (c *EmailChannel) renderCardHTML(card *InteractiveCard) (string, error) {
	tmpl := template.Must(template.New("approval-email").Parse(emailCardTemplate))
	var buf bytes.Buffer
	data := map[string]any{
		"Title":       card.Header.Title,
		"Elements":    card.Elements,
		"Actions":     card.Actions,
		"ApprovalURL": c.cfg.BaseURL,
		"Timestamp":   time.Now().Format("2006-01-02 15:04:05"),
	}
	if err := tmpl.Execute(&buf, data); err != nil {
		return "", err
	}
	return buf.String(), nil
}

// extractEmailRecipients 从卡片 Metadata 中提取邮箱列表。
// 支持两种格式：
//   - "recipients" → []Recipient（有 Email 字段）
//   - "to"         → string（逗号分隔）
func extractEmailRecipients(meta map[string]any) []string {
	if meta == nil {
		return nil
	}
	seen := make(map[string]struct{})
	var addrs []string

	if raw, ok := meta["recipients"]; ok {
		switch v := raw.(type) {
		case []Recipient:
			for _, r := range v {
				if r.Email == "" {
					continue
				}
				if _, ok := seen[r.Email]; ok {
					continue
				}
				seen[r.Email] = struct{}{}
				addrs = append(addrs, r.Email)
			}
		case []any:
			for _, item := range v {
				if r, ok := item.(Recipient); ok && r.Email != "" {
					if _, dup := seen[r.Email]; dup {
						continue
					}
					seen[r.Email] = struct{}{}
					addrs = append(addrs, r.Email)
				}
			}
		}
	}

	if s, ok := meta["to"].(string); ok && s != "" {
		for _, a := range splitAndTrim(s) {
			if a == "" {
				continue
			}
			if _, dup := seen[a]; dup {
				continue
			}
			seen[a] = struct{}{}
			addrs = append(addrs, a)
		}
	}

	return addrs
}

// splitAndTrim 按逗号分割并去除空白。
func splitAndTrim(s string) []string {
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// emailCardTemplate 是审批邮件的 HTML 模板。
// CardElement 用 Text 或 Fields([]CardField{Key,Value})。
// CardAction 用 Text/Style/URL。
const emailCardTemplate = `<!DOCTYPE html>
<html>
<head><meta charset="UTF-8"></head>
<body style="font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', sans-serif; max-width: 600px; margin: 0 auto; padding: 20px;">
  <h2 style="color: #1a1a1a;">{{.Title}}</h2>
  {{range .Elements}}
    {{if .Text}}
    <p style="color: #333; line-height: 1.6;">{{.Text}}</p>
    {{end}}
    {{if .Fields}}
    <table style="width: 100%; border-collapse: collapse; margin: 12px 0;">
      {{range .Fields}}
      <tr>
        <td style="padding: 8px 12px; border: 1px solid #e0e0e0; background: #f9f9f9; font-weight: 600; width: 30%;">{{.Key}}</td>
        <td style="padding: 8px 12px; border: 1px solid #e0e0e0;">{{.Value}}</td>
      </tr>
      {{end}}
    </table>
    {{end}}
  {{end}}
  {{if .Actions}}
  <div style="margin: 24px 0; text-align: center;">
    {{range .Actions}}
    <a href="{{.URL}}"
       style="display: inline-block; padding: 10px 28px; margin: 0 8px; border-radius: 6px;
              text-decoration: none; font-weight: 600;
              {{if eq .Style "primary"}}background: #1677ff; color: #fff;{{else if eq .Style "danger"}}background: #ff4d4f; color: #fff;{{else}}background: #f0f0f0; color: #333;{{end}}">
      {{.Text}}
    </a>
    {{end}}
  </div>
  {{end}}
  <p style="color: #999; font-size: 12px; margin-top: 32px;">发送时间: {{.Timestamp}}</p>
</body>
</html>`
