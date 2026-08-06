package modelquality

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// FileStorage 基于文件的存储实现
type FileStorage struct {
	baseDir string
	mu      sync.RWMutex
}

// NewFileStorage 创建文件存储
func NewFileStorage(baseDir string) (*FileStorage, error) {
	// 创建目录结构
	dirs := []string{
		filepath.Join(baseDir, "reports"),
		filepath.Join(baseDir, "scores"),
	}
	
	for _, dir := range dirs {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return nil, fmt.Errorf("create directory %s: %w", dir, err)
		}
	}
	
	return &FileStorage{
		baseDir: baseDir,
	}, nil
}

// SaveReport 保存测试报告
func (s *FileStorage) SaveReport(ctx context.Context, report *BenchmarkReport) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	
	filename := fmt.Sprintf("%s_%s_%s_%s.json",
		report.Provider,
		report.ModelName,
		report.BenchmarkType,
		report.StartTime.Format("20060102_150405"),
	)
	
	path := filepath.Join(s.baseDir, "reports", filename)
	
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal report: %w", err)
	}
	
	if err := os.WriteFile(path, data, 0644); err != nil {
		return fmt.Errorf("write report file: %w", err)
	}
	
	fmt.Printf("[FileStorage] Report saved: %s\n", path)
	return nil
}

// SaveScore 保存质量评分
func (s *FileStorage) SaveScore(ctx context.Context, score *QualityScore) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	
	// 读取现有评分历史
	historyFile := filepath.Join(s.baseDir, "scores", fmt.Sprintf("%s_%s.jsonl", score.Provider, score.ModelName))
	
	// 追加写入
	f, err := os.OpenFile(historyFile, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("open score file: %w", err)
	}
	defer f.Close()
	
	data, err := json.Marshal(score)
	if err != nil {
		return fmt.Errorf("marshal score: %w", err)
	}
	
	if _, err := f.Write(append(data, '\n')); err != nil {
		return fmt.Errorf("write score: %w", err)
	}
	
	return nil
}

// GetLatestScore 获取最新评分
func (s *FileStorage) GetLatestScore(ctx context.Context, provider string, modelName string) (*QualityScore, error) {
	scores, err := s.GetScoreHistory(ctx, provider, modelName, 1)
	if err != nil {
		return nil, err
	}
	
	if len(scores) == 0 {
		return nil, nil
	}
	
	return scores[0], nil
}

// GetScoreHistory 获取历史评分
func (s *FileStorage) GetScoreHistory(ctx context.Context, provider string, modelName string, limit int) ([]*QualityScore, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	
	historyFile := filepath.Join(s.baseDir, "scores", fmt.Sprintf("%s_%s.jsonl", provider, modelName))
	
	data, err := os.ReadFile(historyFile)
	if err != nil {
		if os.IsNotExist(err) {
			return []*QualityScore{}, nil
		}
		return nil, fmt.Errorf("read score file: %w", err)
	}
	
	lines := []byte{}
	scores := []*QualityScore{}
	
	for _, b := range data {
		if b == '\n' {
			if len(lines) > 0 {
				var score QualityScore
				if err := json.Unmarshal(lines, &score); err == nil {
					scores = append(scores, &score)
				}
				lines = []byte{}
			}
		} else {
			lines = append(lines, b)
		}
	}
	
	// 按时间倒序排序
	sort.Slice(scores, func(i, j int) bool {
		return scores[i].Timestamp.After(scores[j].Timestamp)
	})
	
	// 限制返回数量
	if limit > 0 && len(scores) > limit {
		scores = scores[:limit]
	}
	
	return scores, nil
}

// ConsoleAlerter 控制台告警实现(简单实现)
type ConsoleAlerter struct{}

// NewConsoleAlerter 创建控制台告警器
func NewConsoleAlerter() *ConsoleAlerter {
	return &ConsoleAlerter{}
}

// Alert 发送告警
func (a *ConsoleAlerter) Alert(ctx context.Context, level string, title string, message string) error {
	timestamp := time.Now().Format("2006-01-02 15:04:05")
	fmt.Printf("\n")
	fmt.Printf("========================================\n")
	fmt.Printf("🚨 ALERT [%s] - %s\n", level, timestamp)
	fmt.Printf("========================================\n")
	fmt.Printf("Title: %s\n", title)
	fmt.Printf("----------------------------------------\n")
	fmt.Printf("%s\n", message)
	fmt.Printf("========================================\n")
	fmt.Printf("\n")
	return nil
}

// LogAlerter 日志告警实现(写入文件)
type LogAlerter struct {
	logFile string
	mu      sync.Mutex
}

// NewLogAlerter 创建日志告警器
func NewLogAlerter(logFile string) (*LogAlerter, error) {
	dir := filepath.Dir(logFile)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, err
	}
	return &LogAlerter{
		logFile: logFile,
	}, nil
}

// Alert 发送告警
func (a *LogAlerter) Alert(ctx context.Context, level string, title string, message string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	
	f, err := os.OpenFile(a.logFile, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	defer f.Close()
	
	alert := map[string]interface{}{
		"timestamp": time.Now().Format(time.RFC3339),
		"level":     level,
		"title":     title,
		"message":   message,
	}
	
	data, _ := json.Marshal(alert)
	f.Write(append(data, '\n'))
	
	// 同时输出到控制台
	consoleAlerter := NewConsoleAlerter()
	return consoleAlerter.Alert(ctx, level, title, message)
}

// MultiAlerter 多告警器组合
type MultiAlerter struct {
	alerters []Alerter
}

// NewMultiAlerter 创建多告警器
func NewMultiAlerter(alerters ...Alerter) *MultiAlerter {
	return &MultiAlerter{
		alerters: alerters,
	}
}

// Alert 发送告警到所有告警器
func (a *MultiAlerter) Alert(ctx context.Context, level string, title string, message string) error {
	var lastErr error
	for _, alerter := range a.alerters {
		if err := alerter.Alert(ctx, level, title, message); err != nil {
			lastErr = err
		}
	}
	return lastErr
}
