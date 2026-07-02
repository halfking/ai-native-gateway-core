// Package channels provides notification channel implementations for approval workflows.
package channels

import (
	"bytes"
	"context"
	"crypto/tls"
	"fmt"
	"html/template"
	"net/smtp"
	"strings"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/approval"
)

// EmailChannel sends approval notifications via SMTP email.
type EmailChannel struct {
	smtpHost     string
	smtpPort     int
	smtpUser     string
	smtpPassword string
	fromAddress  string
	baseURL      string // Base URL for approval web UI
}

// NewEmailChannel creates a new email notification channel.
func NewEmailChannel(smtpHost string, smtpPort int, smtpUser, smtpPassword, fromAddress, baseURL string) *EmailChannel {
	return &EmailChannel{
		smtpHost:     smtpHost,
		smtpPort:     smtpPort,
		smtpUser:     smtpUser,
		smtpPassword: smtpPassword,
		fromAddress:  fromAddress,
		baseURL:      baseURL,
	}
}

// SendApprovalNotification sends an email notification to approvers.
func (c *EmailChannel) SendApprovalNotification(ctx context.Context, req *approval.ApprovalRequest, approvers []approval.Approver) error {
	if len(approvers) == 0 {
		return fmt.Errorf("no approvers to notify")
	}

	// Generate email content
	subject := c.generateSubject(req)
	htmlBody, err := c.generateHTMLBody(req)
	if err != nil {
		return fmt.Errorf("failed to generate email body: %w", err)
	}

	// Collect recipient emails
	var recipients []string
	for _, approver := range approvers {
		if approver.Enabled && approver.Email != "" {
			recipients = append(recipients, approver.Email)
		}
	}

	if len(recipients) == 0 {
		return fmt.Errorf("no valid email recipients found")
	}

	// Send email
	return c.sendEmail(recipients, subject, htmlBody)
}

// generateSubject creates the email subject line.
func (c *EmailChannel) generateSubject(req *approval.ApprovalRequest) string {
	riskEmoji := map[approval.RiskLevel]string{
		approval.RiskLow:      "🟢",
		approval.RiskMedium:   "🟡",
		approval.RiskHigh:     "🟠",
		approval.RiskCritical: "🔴",
	}
	emoji := riskEmoji[req.RiskLevel]
	return fmt.Sprintf("【审批请求】%s %s - %s", emoji, req.RiskLevel, req.SessionID)
}

// generateHTMLBody creates the HTML email body.
func (c *EmailChannel) generateHTMLBody(req *approval.ApprovalRequest) (string, error) {
	// Create template with custom functions
	tmpl, err := template.New("approval").Funcs(template.FuncMap{
		"mul": func(a, b float64) float64 {
			return a * b
		},
	}).Parse(emailTemplate)
	if err != nil {
		return "", err
	}

	data := struct {
		Request     *approval.ApprovalRequest
		ApproveURL  string
		RejectURL   string
		ViewURL     string
		RiskColor   string
		RiskIcon    string
		TriggerName string
	}{
		Request:     req,
		ApproveURL:  fmt.Sprintf("%s/approvals/%s/approve", c.baseURL, req.RequestID),
		RejectURL:   fmt.Sprintf("%s/approvals/%s/reject", c.baseURL, req.RequestID),
		ViewURL:     fmt.Sprintf("%s/approvals/%s", c.baseURL, req.RequestID),
		RiskColor:   getRiskColor(req.RiskLevel),
		RiskIcon:    getRiskIcon(req.RiskLevel),
		TriggerName: getTriggerName(req.TriggerType),
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return "", err
	}

	return buf.String(), nil
}

// sendEmail sends an email via SMTP.
func (c *EmailChannel) sendEmail(to []string, subject, htmlBody string) error {
	// Build email message
	msg := c.buildMessage(to, subject, htmlBody)

	// Connect to SMTP server
	addr := fmt.Sprintf("%s:%d", c.smtpHost, c.smtpPort)
	
	// Setup TLS config
	tlsConfig := &tls.Config{
		ServerName: c.smtpHost,
	}

	// Connect with TLS
	conn, err := tls.Dial("tcp", addr, tlsConfig)
	if err != nil {
		// Fallback to non-TLS connection
		conn, err := smtp.Dial(addr)
		if err != nil {
			return fmt.Errorf("failed to connect to SMTP server: %w", err)
		}
		defer conn.Close()
		
		// Start TLS if available
		if ok, _ := conn.Extension("STARTTLS"); ok {
			if err := conn.StartTLS(tlsConfig); err != nil {
				return fmt.Errorf("failed to start TLS: %w", err)
			}
		}

		// Authenticate
		if c.smtpUser != "" && c.smtpPassword != "" {
			auth := smtp.PlainAuth("", c.smtpUser, c.smtpPassword, c.smtpHost)
			if err := conn.Auth(auth); err != nil {
				return fmt.Errorf("failed to authenticate: %w", err)
			}
		}

		// Send email
		if err := conn.Mail(c.fromAddress); err != nil {
			return fmt.Errorf("failed to set sender: %w", err)
		}

		for _, recipient := range to {
			if err := conn.Rcpt(recipient); err != nil {
				return fmt.Errorf("failed to add recipient %s: %w", recipient, err)
			}
		}

		w, err := conn.Data()
		if err != nil {
			return fmt.Errorf("failed to get data writer: %w", err)
		}
		defer w.Close()

		if _, err := w.Write([]byte(msg)); err != nil {
			return fmt.Errorf("failed to write email data: %w", err)
		}

		return nil
	}
	defer conn.Close()

	// Use TLS connection
	client, err := smtp.NewClient(conn, c.smtpHost)
	if err != nil {
		return fmt.Errorf("failed to create SMTP client: %w", err)
	}
	defer client.Close()

	// Authenticate
	if c.smtpUser != "" && c.smtpPassword != "" {
		auth := smtp.PlainAuth("", c.smtpUser, c.smtpPassword, c.smtpHost)
		if err := client.Auth(auth); err != nil {
			return fmt.Errorf("failed to authenticate: %w", err)
		}
	}

	// Send email
	if err := client.Mail(c.fromAddress); err != nil {
		return fmt.Errorf("failed to set sender: %w", err)
	}

	for _, recipient := range to {
		if err := client.Rcpt(recipient); err != nil {
			return fmt.Errorf("failed to add recipient %s: %w", recipient, err)
		}
	}

	w, err := client.Data()
	if err != nil {
		return fmt.Errorf("failed to get data writer: %w", err)
	}
	defer w.Close()

	if _, err := w.Write([]byte(msg)); err != nil {
		return fmt.Errorf("failed to write email data: %w", err)
	}

	return nil
}

// buildMessage constructs the RFC 5322 email message.
func (c *EmailChannel) buildMessage(to []string, subject, htmlBody string) string {
	var buf bytes.Buffer
	
	buf.WriteString(fmt.Sprintf("From: %s\r\n", c.fromAddress))
	buf.WriteString(fmt.Sprintf("To: %s\r\n", strings.Join(to, ", ")))
	buf.WriteString(fmt.Sprintf("Subject: %s\r\n", subject))
	buf.WriteString("MIME-Version: 1.0\r\n")
	buf.WriteString("Content-Type: text/html; charset=UTF-8\r\n")
	buf.WriteString(fmt.Sprintf("Date: %s\r\n", time.Now().Format(time.RFC1123Z)))
	buf.WriteString("\r\n")
	buf.WriteString(htmlBody)
	
	return buf.String()
}

// Helper functions
func getRiskColor(level approval.RiskLevel) string {
	colors := map[approval.RiskLevel]string{
		approval.RiskLow:      "#28a745",
		approval.RiskMedium:   "#ffc107",
		approval.RiskHigh:     "#fd7e14",
		approval.RiskCritical: "#dc3545",
	}
	return colors[level]
}

func getRiskIcon(level approval.RiskLevel) string {
	icons := map[approval.RiskLevel]string{
		approval.RiskLow:      "🟢",
		approval.RiskMedium:   "🟡",
		approval.RiskHigh:     "🟠",
		approval.RiskCritical: "🔴",
	}
	return icons[level]
}

func getTriggerName(trigger approval.ApprovalTriggerType) string {
	names := map[approval.ApprovalTriggerType]string{
		approval.TriggerSensitiveContent: "敏感内容检测",
		approval.TriggerHighCost:         "高成本预警",
		approval.TriggerToolCall:         "工具调用",
		approval.TriggerPolicyMatch:      "策略匹配",
		approval.TriggerManualMode:       "手动审批模式",
	}
	return names[trigger]
}

// emailTemplate is the HTML template for approval notifications.
const emailTemplate = `<!DOCTYPE html>
<html>
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>审批请求</title>
    <style>
        body {
            font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, 'Helvetica Neue', Arial, sans-serif;
            line-height: 1.6;
            color: #333;
            max-width: 600px;
            margin: 0 auto;
            padding: 20px;
            background-color: #f5f5f5;
        }
        .container {
            background-color: #ffffff;
            border-radius: 8px;
            box-shadow: 0 2px 4px rgba(0,0,0,0.1);
            overflow: hidden;
        }
        .header {
            background: linear-gradient(135deg, #667eea 0%, #764ba2 100%);
            color: white;
            padding: 30px 20px;
            text-align: center;
        }
        .header h1 {
            margin: 0;
            font-size: 24px;
            font-weight: 600;
        }
        .content {
            padding: 30px 20px;
        }
        .risk-badge {
            display: inline-block;
            padding: 6px 12px;
            border-radius: 4px;
            font-weight: 600;
            font-size: 14px;
            margin-bottom: 20px;
            background-color: {{.RiskColor}};
            color: white;
        }
        .info-table {
            width: 100%;
            border-collapse: collapse;
            margin: 20px 0;
        }
        .info-table th,
        .info-table td {
            padding: 12px;
            text-align: left;
            border-bottom: 1px solid #e9ecef;
        }
        .info-table th {
            background-color: #f8f9fa;
            font-weight: 600;
            width: 30%;
        }
        .message-box {
            background-color: #f8f9fa;
            border-left: 4px solid #667eea;
            padding: 15px;
            margin: 20px 0;
            border-radius: 4px;
        }
        .message-box h3 {
            margin: 0 0 10px 0;
            font-size: 16px;
            color: #495057;
        }
        .message-box p {
            margin: 0;
            color: #6c757d;
            word-wrap: break-word;
        }
        .sensitive-items {
            margin: 20px 0;
        }
        .sensitive-item {
            background-color: #fff3cd;
            border-left: 4px solid #ffc107;
            padding: 10px;
            margin: 10px 0;
            border-radius: 4px;
        }
        .button-container {
            text-align: center;
            margin: 30px 0;
            padding: 20px 0;
            border-top: 2px solid #e9ecef;
        }
        .button {
            display: inline-block;
            padding: 12px 30px;
            margin: 0 10px;
            border-radius: 6px;
            text-decoration: none;
            font-weight: 600;
            font-size: 16px;
            transition: opacity 0.2s;
        }
        .button:hover {
            opacity: 0.8;
        }
        .button-approve {
            background-color: #28a745;
            color: white;
        }
        .button-reject {
            background-color: #dc3545;
            color: white;
        }
        .button-view {
            background-color: #6c757d;
            color: white;
            display: block;
            margin: 10px auto;
            width: fit-content;
        }
        .footer {
            text-align: center;
            padding: 20px;
            color: #6c757d;
            font-size: 14px;
            background-color: #f8f9fa;
        }
        .expires-warning {
            background-color: #fff3cd;
            border: 1px solid #ffc107;
            padding: 12px;
            border-radius: 4px;
            margin: 20px 0;
            text-align: center;
        }
    </style>
</head>
<body>
    <div class="container">
        <div class="header">
            <h1>{{.RiskIcon}} 审批请求通知</h1>
        </div>
        
        <div class="content">
            <div class="risk-badge">
                风险等级: {{.Request.RiskLevel}}
            </div>
            
            <table class="info-table">
                <tr>
                    <th>请求 ID</th>
                    <td>{{.Request.RequestID}}</td>
                </tr>
                <tr>
                    <th>会话 ID</th>
                    <td>{{.Request.SessionID}}</td>
                </tr>
                <tr>
                    <th>租户 ID</th>
                    <td>{{.Request.TenantID}}</td>
                </tr>
                <tr>
                    <th>触发原因</th>
                    <td>{{.TriggerName}}</td>
                </tr>
                <tr>
                    <th>原因说明</th>
                    <td>{{.Request.TriggerReason}}</td>
                </tr>
                <tr>
                    <th>预估成本</th>
                    <td>${{printf "%.4f" .Request.EstimatedCost}} ({{.Request.EstimatedTokens}} tokens)</td>
                </tr>
                <tr>
                    <th>创建时间</th>
                    <td>{{.Request.CreatedAt.Format "2006-01-02 15:04:05"}}</td>
                </tr>
                <tr>
                    <th>过期时间</th>
                    <td>{{.Request.ExpiresAt.Format "2006-01-02 15:04:05"}}</td>
                </tr>
            </table>
            
            <div class="message-box">
                <h3>用户消息</h3>
                <p>{{.Request.UserMessage}}</p>
            </div>
            
            {{if .Request.SensitiveInfo}}
            <div class="sensitive-items">
                <h3>检测到的敏感信息</h3>
                {{range .Request.SensitiveInfo}}
                <div class="sensitive-item">
                    <strong>类型:</strong> {{.Type}} | 
                    <strong>置信度:</strong> {{printf "%.0f%%" (mul .Confidence 100)}} | 
                    <strong>位置:</strong> {{.Location}}<br>
                    <strong>内容:</strong> {{.Content}}
                </div>
                {{end}}
            </div>
            {{end}}
            
            <div class="expires-warning">
                ⏰ 此审批请求将在 {{.Request.ExpiresAt.Format "2006-01-02 15:04:05"}} 过期
            </div>
            
            <div class="button-container">
                <a href="{{.ApproveURL}}" class="button button-approve">✅ 批准</a>
                <a href="{{.RejectURL}}" class="button button-reject">❌ 拒绝</a>
            </div>
            
            <a href="{{.ViewURL}}" class="button button-view">查看完整详情</a>
        </div>
        
        <div class="footer">
            <p>这是一封自动发送的审批通知邮件，请勿直接回复。</p>
            <p>如有问题，请联系系统管理员。</p>
        </div>
    </div>
</body>
</html>`
