package sensitive

import (
	"container/list"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"time"
)

// acNode AC 自动机节点
type acNode struct {
	children map[rune]*acNode
	fail     *acNode
	outputs  []*MatchResult
}

// SensitiveWordEngine AC 自动机敏感词引擎
//
// 基于 Aho-Corasick 多模式匹配算法的敏感词检测引擎。
// 支持：O(n) 线性时间匹配、热加载、线程安全读。
type SensitiveWordEngine struct {
	root       *acNode
	categories map[string]*WordCategory
	mu         sync.RWMutex
	logger     *slog.Logger
	configPath string // 记录配置文件路径，供 ReloadFromFile 使用
}

// NewSensitiveWordEngine 创建空引擎
func NewSensitiveWordEngine() *SensitiveWordEngine {
	return &SensitiveWordEngine{
		root:       newACNode(),
		categories: make(map[string]*WordCategory),
	}
}

// Build 构建或重建 AC 自动机
//
//	config: 敏感词配置文件顶层结构，categories 中各分类下的 words 会被构建为匹配模式。
func (e *SensitiveWordEngine) Build(config *SensitiveWordConfig) error {
	if config == nil {
		return fmt.Errorf("sensitive word config is nil")
	}
	root := newACNode()
	cats := make(map[string]*WordCategory)
	for key, cc := range config.Categories {
		level := defaultLevel(key)
		cats[key] = &WordCategory{
			Name:  cc.Name,
			Key:   key,
			Level: level,
		}
		for _, word := range cc.Words {
			insertWord(root, []rune(word), &MatchResult{
				Word:     word,
				Category: cats[key],
			})
		}
	}
	buildFailLinks(root)

	e.mu.Lock()
	e.root = root
	e.categories = cats
	e.mu.Unlock()
	return nil
}

// BuildFromFile 从 JSON 文件路径构建，并记录路径供 ReloadFromFile 使用
func (e *SensitiveWordEngine) BuildFromFile(path string) error {
	e.configPath = path
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read config failed: %w (path=%s)", err, path)
	}
	var config SensitiveWordConfig
	if err := json.Unmarshal(data, &config); err != nil {
		return fmt.Errorf("parse config failed: %w (path=%s)", err, path)
	}
	return e.Build(&config)
}

// ReloadFromFile 重新加载配置文件（热加载）
//
//	读取同一路径的最新内容并原子替换 AC 自动机。
//	并发安全：读取中的 Match 不受影响（写锁仅用于替换指针）。
func (e *SensitiveWordEngine) ReloadFromFile() error {
	if e.configPath == "" {
		return fmt.Errorf("no config path set; call BuildFromFile first")
	}
	return e.BuildFromFile(e.configPath)
}

// Match 在文本中搜索所有敏感词
//
//	返回去重、按位置排序的匹配结果。
//	并发安全：读锁保护。
func (e *SensitiveWordEngine) Match(text string) []*MatchResult {
	e.mu.RLock()
	defer e.mu.RUnlock()

	root := e.root
	if root == nil {
		return nil
	}

	runes := []rune(text)
	node := root
	seen := make(map[string]*MatchResult)

	for i, r := range runes {
		for node.children[r] == nil && node != root {
			node = node.fail
		}
		if next, ok := node.children[r]; ok {
			node = next
		}
		for _, out := range node.outputs {
			end := len([]byte(string(runes[:i+1])))
			begin := end - len([]byte(out.Word))
			res := &MatchResult{
				Word:     out.Word,
				Begin:    begin,
				End:      end,
				Category: out.Category,
			}
			key := fmt.Sprintf("%s:%d", out.Word, begin)
			if _, dup := seen[key]; !dup {
				seen[key] = res
			}
		}
	}
	results := make([]*MatchResult, 0, len(seen))
	for _, r := range seen {
		results = append(results, r)
	}
	sortResults(results)
	return results
}

// WatchConfig 按固定间隔轮询配置文件，发生变化时自动热加载。
//
//	ctx: 控制生命周期（cancel 或超时即停止）
//	interval: 轮询间隔（建议 30s–300s，避免过于频繁的磁盘读）
//	返回一个 error channel，异步 reload 失败时写入。
//
//	并发安全：ReloadFromFile 内部使用写锁，Match 使用读锁。
func (e *SensitiveWordEngine) WatchConfig(ctx context.Context, interval time.Duration) <-chan error {
	errCh := make(chan error, 1)
	if e.configPath == "" {
		errCh <- fmt.Errorf("watch: no config path set")
		return errCh
	}
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		var lastMod time.Time
		if fi, err := os.Stat(e.configPath); err == nil {
			lastMod = fi.ModTime()
		}
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				fi, err := os.Stat(e.configPath)
				if err != nil {
					continue
				}
				if fi.ModTime().After(lastMod) {
					lastMod = fi.ModTime()
					if rErr := e.ReloadFromFile(); rErr != nil {
						select {
						case errCh <- rErr:
						default:
						}
					}
				}
			}
		}
	}()
	return errCh
}

// Categories 返回当前加载的分类信息
func (e *SensitiveWordEngine) Categories() map[string]*WordCategory {
	e.mu.RLock()
	defer e.mu.RUnlock()
	out := make(map[string]*WordCategory, len(e.categories))
	for k, v := range e.categories {
		out[k] = v
	}
	return out
}

// LoadedWordCount 返回已加载的敏感词总数
func (e *SensitiveWordEngine) LoadedWordCount() int {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return countWords(e.root)
}

// ---- internal ----

func newACNode() *acNode {
	return &acNode{children: make(map[rune]*acNode)}
}

func insertWord(root *acNode, runes []rune, result *MatchResult) {
	node := root
	for _, r := range runes {
		if next, ok := node.children[r]; ok {
			node = next
		} else {
			next = newACNode()
			node.children[r] = next
			node = next
		}
	}
	node.outputs = append(node.outputs, result)
}

func buildFailLinks(root *acNode) {
	queue := list.New()
	for _, child := range root.children {
		child.fail = root
		queue.PushBack(child)
	}
	for queue.Len() > 0 {
		elem := queue.Front()
		queue.Remove(elem)
		node := elem.Value.(*acNode)
		for r, child := range node.children {
			fail := node.fail
			for fail != root && fail.children[r] == nil {
				fail = fail.fail
			}
			if next, ok := fail.children[r]; ok && next != child {
				child.fail = next
			} else {
				child.fail = root
			}
			child.outputs = append(child.outputs, child.fail.outputs...)
			queue.PushBack(child)
		}
	}
}

func countWords(node *acNode) int {
	if node == nil {
		return 0
	}
	count := len(node.outputs)
	for _, child := range node.children {
		count += countWords(child)
	}
	return count
}

func defaultLevel(key string) AlertLevel {
	switch key {
	case "terrorism", "sexual_violence", "drugs_weapons":
		return LevelP0
	case "political", "financial_crime":
		return LevelP1
	default:
		return LevelP2
	}
}

func sortResults(results []*MatchResult) {
	for i := 0; i < len(results); i++ {
		for j := i + 1; j < len(results); j++ {
			if results[i].Begin > results[j].Begin ||
				(results[i].Begin == results[j].Begin && results[i].End > results[j].End) {
				results[i], results[j] = results[j], results[i]
			}
		}
	}
}
