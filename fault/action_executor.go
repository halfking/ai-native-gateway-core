package fault

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

type ActionExecutor struct {
	handlers map[string]ActionHandler
}

type ActionHandler func(ctx context.Context, config map[string]interface{}) (string, error)

func NewActionExecutor() *ActionExecutor {
	executor := &ActionExecutor{
		handlers: make(map[string]ActionHandler),
	}

	executor.RegisterHandler(ActionRestart, executor.handleRestart)
	executor.RegisterHandler(ActionScaleUp, executor.handleScaleUp)
	executor.RegisterHandler(ActionNotify, executor.handleNotify)
	executor.RegisterHandler(ActionFailover, executor.handleFailover)
	executor.RegisterHandler(ActionRecover, executor.handleAutoRecover)
	executor.RegisterHandler(ActionScript, executor.handleRunScript)

	return executor
}

func (ae *ActionExecutor) RegisterHandler(action string, handler ActionHandler) {
	ae.handlers[action] = handler
}

func (ae *ActionExecutor) Execute(ctx context.Context, action string, config map[string]interface{}) (string, error) {
	handler, ok := ae.handlers[action]
	if !ok {
		return "", errors.New("unknown action: " + action)
	}

	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()

	return handler(ctx, config)
}

func (ae *ActionExecutor) handleRestart(ctx context.Context, config map[string]interface{}) (string, error) {
	slog.Info("executing restart action", "config", config)
	return "restart action simulated", nil
}

func (ae *ActionExecutor) handleScaleUp(ctx context.Context, config map[string]interface{}) (string, error) {
	slog.Info("executing scale_up action", "config", config)
	return "scale_up action simulated", nil
}

func (ae *ActionExecutor) handleNotify(ctx context.Context, config map[string]interface{}) (string, error) {
	channel, _ := config["channel"].(string)
	message, _ := config["message"].(string)
	slog.Info("executing notify action", "channel", channel, "message", message)
	return "notification sent to " + channel, nil
}

func (ae *ActionExecutor) handleFailover(ctx context.Context, config map[string]interface{}) (string, error) {
	slog.Info("executing failover action", "config", config)
	return "failover action simulated", nil
}

func (ae *ActionExecutor) handleAutoRecover(ctx context.Context, config map[string]interface{}) (string, error) {
	slog.Info("executing auto_recover action", "config", config)
	return "auto_recover action simulated", nil
}

func (ae *ActionExecutor) handleRunScript(ctx context.Context, config map[string]interface{}) (string, error) {
	scriptPath, ok := config["script"].(string)
	if !ok || scriptPath == "" {
		return "", errors.New("script path not provided")
	}
	allowedDir := strings.TrimSpace(os.Getenv("LLM_GATEWAY_FAULT_SCRIPT_DIR"))
	if allowedDir == "" {
		return "", errors.New("script execution is disabled")
	}
	allowedDir, err := filepath.Abs(allowedDir)
	if err != nil {
		return "", errors.New("invalid script directory")
	}
	scriptPath, err = filepath.Abs(scriptPath)
	if err != nil {
		return "", errors.New("invalid script path")
	}
	if scriptPath == allowedDir || !strings.HasPrefix(scriptPath, allowedDir+string(filepath.Separator)) {
		return "", errors.New("script path is outside the allowed directory")
	}

	args, _ := config["args"].([]string)

	slog.Info("executing script", "script", scriptPath, "args", args)

	cmd := exec.CommandContext(ctx, scriptPath, args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", errors.New("script execution failed: " + err.Error() + ", output: " + string(output))
	}

	return "script executed successfully: " + string(output), nil
}
