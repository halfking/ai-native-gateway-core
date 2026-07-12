package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"
)

// HeartbeatPayload 心跳载荷（与 center.HeartbeatPayload 保持一致）
type HeartbeatPayload struct {
	UptimeSecs   int64   `json:"uptime_secs"`
	GoVersion    string  `json:"go_version"`
	NumGoroutine int     `json:"num_goroutine"`
	AllocMB      float64 `json:"alloc_mb"`
	TotalAllocMB float64 `json:"total_alloc_mb"`
	SysMB        float64 `json:"sys_mb"`
	CPUCores     int     `json:"cpu_cores"`
}

// HeartbeatSender 心跳发送器
type HeartbeatSender struct {
	masterURL     string
	instanceID    string
	instanceToken string
	version       string
	httpClient    *http.Client
	startTime     time.Time
}

// NewHeartbeatSender 创建心跳发送器
func NewHeartbeatSender(masterURL, instanceID, instanceToken, version string) *HeartbeatSender {
	return &HeartbeatSender{
		masterURL:     masterURL,
		instanceID:    instanceID,
		instanceToken: instanceToken,
		version:       version,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
		startTime: time.Now(),
	}
}

// SendHeartbeat 发送一次心跳
func (s *HeartbeatSender) SendHeartbeat(ctx context.Context) error {
	// 采集指标
	var memStats runtime.MemStats
	runtime.ReadMemStats(&memStats)

	payload := HeartbeatPayload{
		UptimeSecs:   int64(time.Since(s.startTime).Seconds()),
		GoVersion:    runtime.Version(),
		NumGoroutine: runtime.NumGoroutine(),
		AllocMB:      float64(memStats.Alloc) / 1024 / 1024,
		TotalAllocMB: float64(memStats.TotalAlloc) / 1024 / 1024,
		SysMB:        float64(memStats.Sys) / 1024 / 1024,
		CPUCores:     runtime.NumCPU(),
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal payload: %w", err)
	}

	url := s.masterURL + "/api/v1/instances/heartbeat"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+s.instanceToken)
	req.Header.Set("User-Agent", "llm-gw-installer/1.0")

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("http request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return fmt.Errorf("API error (status %d)", resp.StatusCode)
	}

	return nil
}

// StartDaemon 启动 daemon 模式（阻塞）
func (s *HeartbeatSender) StartDaemon(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	// 立即发送第一次心跳
	if err := s.SendHeartbeat(ctx); err != nil {
		logWarn(fmt.Sprintf("发送心跳失败: %v", err))
	}

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := s.SendHeartbeat(ctx); err != nil {
				logWarn(fmt.Sprintf("发送心跳失败: %v", err))
			}
		}
	}
}

// runHeartbeat 执行 heartbeat 子命令
func runHeartbeat(args []string) error {
	fs := flag.NewFlagSet("heartbeat", flag.ExitOnError)
	daemon := fs.Bool("daemon", false, "后台运行（持续发送心跳）")
	interval := fs.Duration("interval", 60*time.Second, "心跳间隔")
	masterURL := fs.String("master-url", "https://llm.kxpms.cn", "主控端地址")

	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("解析参数失败: %w", err)
	}

	// 读取 instance_token
	instanceToken, err := readInstanceToken()
	if err != nil {
		return fmt.Errorf("读取 instance_token 失败: %w\n提示：请先执行 activate 命令完成实例注册", err)
	}

	// 读取 instance_id
	instanceID, err := readInstanceID()
	if err != nil {
		return fmt.Errorf("读取 instance_id 失败: %w", err)
	}

	// 创建 HeartbeatSender
	sender := NewHeartbeatSender(*masterURL, instanceID, instanceToken, readVersion())

	ctx := context.Background()

	if *daemon {
		// daemon 模式：持续发送心跳
		logInfo(fmt.Sprintf("▶ 启动 heartbeat daemon（间隔 %s）", *interval))
		logInfo(fmt.Sprintf("  主控端: %s", *masterURL))
		logInfo(fmt.Sprintf("  实例ID: %s", instanceID))

		// 监听 SIGTERM/SIGINT 实现优雅关闭
		ctx, cancel := context.WithCancel(ctx)
		defer cancel()

		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)

		go func() {
			sig := <-sigCh
			logInfo(fmt.Sprintf("  收到信号 %v，优雅关闭...", sig))
			cancel()
		}()

		// 启动 daemon（阻塞）
		sender.StartDaemon(ctx, *interval)

		logInfo("✅ heartbeat daemon 已停止")
		return nil
	}

	// 单次模式：发送一次心跳后退出
	logInfo("▶ 单次心跳模式")
	logInfo(fmt.Sprintf("  主控端: %s", *masterURL))
	logInfo(fmt.Sprintf("  实例ID: %s", instanceID))

	if err := sender.SendHeartbeat(ctx); err != nil {
		return fmt.Errorf("发送心跳失败: %w", err)
	}

	logInfo("✅ 心跳发送成功")
	return nil
}

// heartbeatCmd 注册 heartbeat 子命令
func heartbeatCmd() *cobra.Command {
	var (
		daemon    bool
		interval  time.Duration
		masterURL string
	)

	cmd := &cobra.Command{
		Use:   "heartbeat",
		Short: "发送心跳到主控端",
		Long: `发送心跳到主控端，支持单次执行和 daemon 模式。

示例：
  # 单次执行
  llm-gw-installer heartbeat

  # daemon 模式（持续发送心跳）
  llm-gw-installer heartbeat --daemon --interval 60s

  # 自定义主控端地址
  llm-gw-installer heartbeat --master-url https://master.example.com`,
		RunE: func(cmd *cobra.Command, args []string) error {
			// 读取 instance_token
			instanceToken, err := readInstanceToken()
			if err != nil {
				return fmt.Errorf("读取 instance_token 失败: %w\n提示：请先执行 activate 命令完成实例注册", err)
			}

			// 读取 instance_id
			instanceID, err := readInstanceID()
			if err != nil {
				return fmt.Errorf("读取 instance_id 失败: %w", err)
			}

			// 创建 HeartbeatSender
			sender := NewHeartbeatSender(masterURL, instanceID, instanceToken, readVersion())

			ctx := context.Background()

			if daemon {
				// daemon 模式：持续发送心跳
				logInfo(fmt.Sprintf("▶ 启动 heartbeat daemon（间隔 %s）", interval))
				logInfo(fmt.Sprintf("  主控端: %s", masterURL))
				logInfo(fmt.Sprintf("  实例ID: %s", instanceID))

				// 监听 SIGTERM/SIGINT 实现优雅关闭
				ctx, cancel := context.WithCancel(ctx)
				defer cancel()

				sigCh := make(chan os.Signal, 1)
				signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)

				go func() {
					sig := <-sigCh
					logInfo(fmt.Sprintf("  收到信号 %v，优雅关闭...", sig))
					cancel()
				}()

				// 启动 daemon（阻塞）
				sender.StartDaemon(ctx, interval)

				logInfo("✅ heartbeat daemon 已停止")
				return nil
			}

			// 单次模式：发送一次心跳后退出
			logInfo("▶ 单次心跳模式")
			logInfo(fmt.Sprintf("  主控端: %s", masterURL))
			logInfo(fmt.Sprintf("  实例ID: %s", instanceID))

			if err := sender.SendHeartbeat(ctx); err != nil {
				return fmt.Errorf("发送心跳失败: %w", err)
			}

			logInfo("✅ 心跳发送成功")
			return nil
		},
	}

	cmd.Flags().BoolVar(&daemon, "daemon", false, "后台运行（持续发送心跳）")
	cmd.Flags().DurationVar(&interval, "interval", 60*time.Second, "心跳间隔")
	cmd.Flags().StringVar(&masterURL, "master-url", "https://llm.kxpms.cn", "主控端地址")

	return cmd
}

// readInstanceToken 读取 ~/.kx-gateway/instance.token
func readInstanceToken() (string, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("获取 home 目录失败: %w", err)
	}

	tokenPath := filepath.Join(homeDir, ".kx-gateway", "instance.token")
	data, err := os.ReadFile(tokenPath)
	if err != nil {
		return "", fmt.Errorf("读取 %s 失败: %w", tokenPath, err)
	}

	token := strings.TrimSpace(string(data))
	if token == "" {
		return "", fmt.Errorf("instance.token 为空")
	}

	return token, nil
}

// readInstanceID 读取 ~/.kx-gateway/instance.id
func readInstanceID() (string, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("获取 home 目录失败: %w", err)
	}

	idPath := filepath.Join(homeDir, ".kx-gateway", "instance.id")
	data, err := os.ReadFile(idPath)
	if err != nil {
		return "", fmt.Errorf("读取 %s 失败: %w", idPath, err)
	}

	id := strings.TrimSpace(string(data))
	if id == "" {
		return "", fmt.Errorf("instance.id 为空")
	}

	return id, nil
}

// readVersion 读取应用版本（从 app/VERSION 或环境变量）
func readVersion() string {
	// 尝试读取 app/VERSION
	if data, err := os.ReadFile("app/VERSION"); err == nil {
		return strings.TrimSpace(string(data))
	}

	// 回退到环境变量
	if v := os.Getenv("APP_IMAGE_TAG"); v != "" {
		return v
	}

	return "unknown"
}
