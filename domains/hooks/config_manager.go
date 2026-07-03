package hooks

import (
	"fmt"
	"os"
	"sync"
	"time"

	"gopkg.in/yaml.v3"
)

// HookConfig 定义单个Hook的配置
type HookConfig struct {
	Name     string                 `yaml:"name"`
	Enabled  bool                   `yaml:"enabled"`
	Priority int                    `yaml:"priority"`
	Phase    string                 `yaml:"phase"`
	Timeout  string                 `yaml:"timeout"`
	Config   map[string]interface{} `yaml:"config"`
}

// HooksConfig 定义hooks.yaml的整体结构
type HooksConfig struct {
	Hooks []HookConfig `yaml:"hooks"`
}

// FileConfigManager 实现基于文件的ConfigManager
type FileConfigManager struct {
	configFile string
	config     *HooksConfig
	mu         sync.RWMutex

	// 监控相关
	stopCh   chan struct{}
	watching bool
	lastMod  time.Time
}

// NewConfigManager 创建新的ConfigManager实例
func NewConfigManager(configFile string) (*FileConfigManager, error) {
	cm := &FileConfigManager{
		configFile: configFile,
		config:     &HooksConfig{Hooks: []HookConfig{}},
		stopCh:     make(chan struct{}),
	}

	// 初始加载配置
	if err := cm.Load(); err != nil {
		// 配置文件不存在时使用默认配置
		if os.IsNotExist(err) {
			cm.config = &HooksConfig{Hooks: []HookConfig{}}
			return cm, nil
		}
		return nil, err
	}

	return cm, nil
}

// Load 加载配置文件
func (cm *FileConfigManager) Load() error {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	// 读取文件
	data, err := os.ReadFile(cm.configFile)
	if err != nil {
		// 保留原始错误类型，以便NewConfigManager可以检测IsNotExist
		return err
	}

	// 解析YAML
	var config HooksConfig
	if err := yaml.Unmarshal(data, &config); err != nil {
		return fmt.Errorf("failed to parse config file: %w", err)
	}

	// 更新配置
	cm.config = &config

	// 更新文件修改时间
	if stat, err := os.Stat(cm.configFile); err == nil {
		cm.lastMod = stat.ModTime()
	}

	return nil
}

// GetHookConfig 获取指定Hook的配置
func (cm *FileConfigManager) GetHookConfig(hookName string) map[string]interface{} {
	cm.mu.RLock()
	defer cm.mu.RUnlock()

	for _, hc := range cm.config.Hooks {
		if hc.Name == hookName {
			// 返回副本，避免外部修改
			result := make(map[string]interface{})
			for k, v := range hc.Config {
				result[k] = v
			}
			return result
		}
	}

	return nil
}

// GetHookTimeout 获取指定Hook的超时时间
func (cm *FileConfigManager) GetHookTimeout(hookName string) time.Duration {
	cm.mu.RLock()
	defer cm.mu.RUnlock()

	for _, hc := range cm.config.Hooks {
		if hc.Name == hookName && hc.Timeout != "" {
			if duration, err := time.ParseDuration(hc.Timeout); err == nil {
				return duration
			}
		}
	}

	// 默认超时5秒
	return 5 * time.Second
}

// IsHookEnabled 检查Hook是否启用
func (cm *FileConfigManager) IsHookEnabled(hookName string) bool {
	cm.mu.RLock()
	defer cm.mu.RUnlock()

	for _, hc := range cm.config.Hooks {
		if hc.Name == hookName {
			return hc.Enabled
		}
	}

	// 默认启用
	return true
}

// Watch 监控配置文件变化
func (cm *FileConfigManager) Watch(callback func()) error {
	cm.mu.Lock()
	if cm.watching {
		cm.mu.Unlock()
		return fmt.Errorf("already watching")
	}
	cm.watching = true
	cm.mu.Unlock()

	// 启动监控goroutine
	go cm.watchLoop(callback)

	return nil
}

// watchLoop 监控循环
func (cm *FileConfigManager) watchLoop(callback func()) {
	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-cm.stopCh:
			return
		case <-ticker.C:
			if cm.checkFileChanged() {
				if err := cm.Load(); err == nil {
					if callback != nil {
						callback()
					}
				}
			}
		}
	}
}

// checkFileChanged 检查文件是否已修改
func (cm *FileConfigManager) checkFileChanged() bool {
	stat, err := os.Stat(cm.configFile)
	if err != nil {
		return false
	}

	cm.mu.RLock()
	lastMod := cm.lastMod
	cm.mu.RUnlock()

	return stat.ModTime().After(lastMod)
}

// Stop 停止监控
func (cm *FileConfigManager) Stop() {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	if cm.watching {
		close(cm.stopCh)
		cm.watching = false
		// 重新创建channel以便后续Watch调用
		cm.stopCh = make(chan struct{})
	}
}

// GetAllHooks 获取所有Hook配置
func (cm *FileConfigManager) GetAllHooks() []HookConfig {
	cm.mu.RLock()
	defer cm.mu.RUnlock()

	// 返回副本
	result := make([]HookConfig, len(cm.config.Hooks))
	copy(result, cm.config.Hooks)
	return result
}

// NoOpConfigManager 实现无操作的ConfigManager
type NoOpConfigManager struct{}

func NewNoOpConfigManager() *NoOpConfigManager {
	return &NoOpConfigManager{}
}

func (cm *NoOpConfigManager) Load() error {
	return nil
}

func (cm *NoOpConfigManager) GetHookConfig(hookName string) map[string]interface{} {
	return nil
}

func (cm *NoOpConfigManager) GetHookTimeout(hookName string) time.Duration {
	return 5 * time.Second
}

func (cm *NoOpConfigManager) IsHookEnabled(hookName string) bool {
	return true
}

func (cm *NoOpConfigManager) Watch(callback func()) error {
	return nil
}

func (cm *NoOpConfigManager) Stop() {}
