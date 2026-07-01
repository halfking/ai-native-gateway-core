package remotecontrol

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// LarkCommandParser 飞书指令解析器
//
// 职责：
//   - 解析飞书消息中的指令
//   - 支持 /pause, /resume, /status 等指令
//   - 参数提取和验证
type LarkCommandParser struct{}

// NewLarkCommandParser 创建飞书指令解析器
func NewLarkCommandParser() *LarkCommandParser {
	return &LarkCommandParser{}
}

// ParseCommand 解析指令
//
// 支持的格式：
//   /pause session_123
//   /resume session_123
//   /terminate session_123 reason="manual termination"
//   /status session_123
//   /inspect session_123
func (p *LarkCommandParser) ParseCommand(content, userID, userName string) (*RemoteCommand, error) {
	content = strings.TrimSpace(content)
	
	// 检查是否是指令（以 / 开头）
	if !strings.HasPrefix(content, "/") {
		return nil, fmt.Errorf("not a command")
	}
	
	// 分割指令和参数
	parts := strings.Fields(content)
	if len(parts) == 0 {
		return nil, fmt.Errorf("empty command")
	}
	
	cmdText := strings.TrimPrefix(parts[0], "/")
	cmdType := p.parseCommandType(cmdText)
	if cmdType == "" {
		return nil, fmt.Errorf("unknown command: %s", cmdText)
	}
	
	// 提取会话ID（通常是第二个参数）
	sessionID := ""
	if len(parts) > 1 {
		sessionID = parts[1]
	}
	
	// 提取其他参数
	parameters := p.parseParameters(parts[2:])
	
	// 构建指令
	cmd := &RemoteCommand{
		Type:       cmdType,
		SessionID:  sessionID,
		IssuerID:   userID,
		IssuerName: userName,
		Parameters: parameters,
		Status:     CommandStatusPending,
		CreatedAt:  time.Now(),
	}
	
	return cmd, nil
}

// parseCommandType 解析指令类型
func (p *LarkCommandParser) parseCommandType(text string) CommandType {
	text = strings.ToLower(text)
	
	switch text {
	case "pause", "暂停":
		return CommandTypePause
	case "resume", "恢复":
		return CommandTypeResume
	case "terminate", "终止", "kill":
		return CommandTypeTerminate
	case "inspect", "检查", "detail":
		return CommandTypeInspect
	case "status", "状态":
		return CommandTypeStatus
	case "modify", "修改":
		return CommandTypeModify
	default:
		return ""
	}
}

// parseParameters 解析参数
//
// 支持格式：
//   key=value
//   key="value with spaces"
func (p *LarkCommandParser) parseParameters(parts []string) map[string]any {
	params := make(map[string]any)
	
	// 正则匹配 key=value 或 key="value"
	re := regexp.MustCompile(`(\w+)=("([^"]+)"|(\S+))`)
	
	for _, part := range parts {
		matches := re.FindStringSubmatch(part)
		if len(matches) >= 3 {
			key := matches[1]
			value := matches[3]
			if value == "" {
				value = matches[4]
			}
			params[key] = value
		}
	}
	
	return params
}

// FormatCommandResult 格式化指令结果为飞书消息
func (p *LarkCommandParser) FormatCommandResult(cmd *RemoteCommand) string {
	if cmd == nil {
		return "⚠️ 指令为空"
	}
	
	var builder strings.Builder
	
	// 标题
	switch cmd.Status {
	case CommandStatusCompleted:
		builder.WriteString("✅ 指令执行成功\n\n")
	case CommandStatusFailed:
		builder.WriteString("❌ 指令执行失败\n\n")
	case CommandStatusExecuting:
		builder.WriteString("⏳ 指令执行中\n\n")
	default:
		builder.WriteString("📋 指令状态\n\n")
	}
	
	// 基本信息
	builder.WriteString(fmt.Sprintf("**指令ID**: %s\n", cmd.ID))
	builder.WriteString(fmt.Sprintf("**类型**: %s\n", cmd.Type))
	if cmd.SessionID != "" {
		builder.WriteString(fmt.Sprintf("**会话ID**: %s\n", cmd.SessionID))
	}
	builder.WriteString(fmt.Sprintf("**操作人**: %s\n", cmd.IssuerName))
	builder.WriteString(fmt.Sprintf("**状态**: %s\n", cmd.Status))
	
	// 执行时间
	if cmd.ExecutedAt != nil && cmd.CompletedAt != nil {
		duration := cmd.CompletedAt.Sub(*cmd.ExecutedAt)
		builder.WriteString(fmt.Sprintf("**耗时**: %dms\n", duration.Milliseconds()))
	}
	
	// 结果或错误
	if cmd.Status == CommandStatusFailed && cmd.Error != "" {
		builder.WriteString(fmt.Sprintf("\n**错误**: %s\n", cmd.Error))
	} else if cmd.Result != nil && len(cmd.Result) > 0 {
		builder.WriteString("\n**结果**:\n")
		for key, value := range cmd.Result {
			builder.WriteString(fmt.Sprintf("- %s: %v\n", key, value))
		}
	}
	
	return builder.String()
}

// LarkCommandAPI 飞书指令API
//
// 职责：
//   - 接收飞书消息中的指令
//   - 解析并执行指令
//   - 返回执行结果
type LarkCommandAPI struct {
	parser   *LarkCommandParser
	executor CommandExecutor
}

// NewLarkCommandAPI 创建飞书指令API
func NewLarkCommandAPI(executor CommandExecutor) *LarkCommandAPI {
	return &LarkCommandAPI{
		parser:   NewLarkCommandParser(),
		executor: executor,
	}
}

// HandleCommand 处理指令
func (api *LarkCommandAPI) HandleCommand(ctx context.Context, content, userID, userName, tenantID string) (string, error) {
	// 解析指令
	cmd, err := api.parser.ParseCommand(content, userID, userName)
	if err != nil {
		return "", fmt.Errorf("failed to parse command: %w", err)
	}
	
	// 设置租户ID
	cmd.TenantID = tenantID
	
	// 执行指令
	if err := api.executor.Execute(ctx, cmd); err != nil {
		return api.parser.FormatCommandResult(cmd), err
	}
	
	// 返回格式化的结果
	return api.parser.FormatCommandResult(cmd), nil
}

// GetHelpText 获取帮助文本
func (api *LarkCommandAPI) GetHelpText() string {
	return `
📖 **远程控制指令帮助**

**基本格式**:
/指令 会话ID [参数]

**支持的指令**:

**1. 暂停会话**
/pause session_123
/暂停 session_123

**2. 恢复会话**
/resume session_123
/恢复 session_123

**3. 终止会话**
/terminate session_123 reason="需要终止"
/终止 session_123 reason="人工干预"

**4. 检查会话**
/inspect session_123
/检查 session_123

**5. 查询状态**
/status session_123
/状态 session_123

**示例**:
/pause sess_abc123
/resume sess_abc123
/terminate sess_abc123 reason="安全原因"
/status sess_abc123
`
}

// ValidateSessionID 验证会话ID格式
func ValidateSessionID(sessionID string) bool {
	if sessionID == "" {
		return false
	}
	
	// 简单验证：至少3个字符
	if len(sessionID) < 3 {
		return false
	}
	
	// 可以添加更严格的验证规则
	// 例如：正则表达式匹配特定格式
	
	return true
}

// CommandTemplate 指令模板
type CommandTemplate struct {
	Name        string
	Type        CommandType
	Description string
	Example     string
	Parameters  []ParameterDef
}

// ParameterDef 参数定义
type ParameterDef struct {
	Name        string
	Type        string
	Required    bool
	Description string
	Default     any
}

// GetCommandTemplates 获取指令模板
func GetCommandTemplates() []CommandTemplate {
	return []CommandTemplate{
		{
			Name:        "暂停会话",
			Type:        CommandTypePause,
			Description: "暂停正在执行的会话",
			Example:     "/pause session_123",
			Parameters:  []ParameterDef{},
		},
		{
			Name:        "恢复会话",
			Type:        CommandTypeResume,
			Description: "恢复已暂停的会话",
			Example:     "/resume session_123",
			Parameters:  []ParameterDef{},
		},
		{
			Name:        "终止会话",
			Type:        CommandTypeTerminate,
			Description: "强制终止会话",
			Example:     "/terminate session_123 reason=\"安全原因\"",
			Parameters: []ParameterDef{
				{
					Name:        "reason",
					Type:        "string",
					Required:    false,
					Description: "终止原因",
				},
			},
		},
		{
			Name:        "检查会话",
			Type:        CommandTypeInspect,
			Description: "查看会话的详细信息",
			Example:     "/inspect session_123",
			Parameters:  []ParameterDef{},
		},
		{
			Name:        "查询状态",
			Type:        CommandTypeStatus,
			Description: "查询会话当前状态",
			Example:     "/status session_123",
			Parameters:  []ParameterDef{},
		},
	}
}
