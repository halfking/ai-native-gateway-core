package sensitive

import (
	"container/list"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/kaixuan/llm-gateway-go/settings"
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
	// cfgMu guards configPath only. It is deliberately separate from mu:
	// Match never touches it, so hot-reload bookkeeping cannot contend with
	// in-flight matching, and vice versa. It used to be written with no lock
	// at all while WatchConfig and the admin reload handler read it.
	cfgMu      sync.RWMutex
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
			if strings.TrimSpace(word) == "" {
				// An empty rule reaches the root node and yields Begin == End,
				// violating the 0 <= Begin < End invariant every position-based
				// consumer relies on — and inflating LoadedWordCount.
				continue
			}
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
	// Record the path BEFORE reading the file: a failed load must still leave
	// a reloadable engine, and admin reload / WatchConfig poll this field
	// concurrently with this write. It used to be written outside every lock,
	// which -race flags and which can tear a Go string header (ptr/len) into a
	// mismatched value under contention.
	e.setConfigPath(path)
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

func (e *SensitiveWordEngine) setConfigPath(path string) {
	e.cfgMu.Lock()
	e.configPath = path
	e.cfgMu.Unlock()
}

func (e *SensitiveWordEngine) currentConfigPath() string {
	e.cfgMu.RLock()
	defer e.cfgMu.RUnlock()
	return e.configPath
}

// ReloadFromFile 重新加载配置文件（热加载）
//
//	读取同一路径的最新内容并原子替换 AC 自动机。
//	并发安全：Match 走 mu 读锁且只读取已构建的指针，热加载在写锁内整体
//	替换；配置路径另由 cfgMu 保护。两者互不嵌套。
func (e *SensitiveWordEngine) ReloadFromFile() error {
	path := e.currentConfigPath()
	if path == "" {
		return fmt.Errorf("no config path set; call BuildFromFile first")
	}
	return e.BuildFromFile(path)
}

// Match 在文本中搜索所有敏感词
//
//	返回去重、按位置排序的匹配结果。区间按 Begin 升序，但**允许相互重叠**
//	（词表里 "abcd"/"cde" 同时存在时，"abcdef" 会产出 [0,4) 与 [2,5)）。
//	调用方若要按 Begin/End 切片，必须自行消解重叠。
//	并发安全：读锁保护；热加载只原子替换指针，在途 Match 不受影响。
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

	// prefixLen[i] is the byte length of the first i runes. One O(n) pass up
	// front replaces what used to be `len([]byte(string(runes[:i+1])))` per
	// match — that copied the whole prefix on every hit, making Match O(n²) in
	// the number of hits (a 127 KB body of dictionary words burned 2.8 s of
	// CPU on a single request, and the check runs on the synchronous
	// governance path).
	prefixLen := make([]int, len(runes)+1)
	off := 0
	for i, r := range runes {
		off += utf8.RuneLen(r)
		prefixLen[i+1] = off
	}

	for i, r := range runes {
		for node.children[r] == nil && node != root {
			node = node.fail
		}
		if next, ok := node.children[r]; ok {
			node = next
		}
		for _, out := range node.outputs {
			wordRunes := utf8.RuneCountInString(out.Word)
			if wordRunes == 0 {
				continue // empty rule: would yield Begin == End
			}
			end := prefixLen[i+1]
			begin := end - (prefixLen[i+1] - prefixLen[i+1-wordRunes])
			if begin < 0 {
				continue // defensive: rule longer than the scanned prefix
			}
			res := &MatchResult{
				Word:     out.Word,
				Begin:    begin,
				End:      end,
				Category: out.Category,
			}
			key := out.Word + ":" + strconv.Itoa(begin)
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
	// Snapshot the path once; reload may re-point it later, and every use
	// below must read a consistent string rather than a racing field.
	path := e.currentConfigPath()
	if path == "" {
		errCh <- fmt.Errorf("watch: no config path set")
		return errCh
	}
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		var lastMod time.Time
		if fi, err := os.Stat(path); err == nil {
			lastMod = fi.ModTime()
		}
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				fi, err := os.Stat(path)
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

// sortResults orders by (Begin, End) ascending. This used to be a
// hand-rolled O(m²) selection sort; with ~44k matches on a 256 KB body it
// dominated everything else and kept Match quadratic even after the
// per-match offset recomputation was removed. A real sort is O(m log m).
func sortResults(results []*MatchResult) {
	sort.Slice(results, func(i, j int) bool {
		if results[i].Begin != results[j].Begin {
			return results[i].Begin < results[j].Begin
		}
		return results[i].End < results[j].End
	})
}

// EvaluateSafety 评估文本安全性，返回评分和建议动作
func (e *SensitiveWordEngine) EvaluateSafety(text string) *SafetyResult {
	matches := e.Match(text)
	if len(matches) == 0 {
		return &SafetyResult{
			Score:        0.0,
			MatchedWords: []string{},
			Matches:      []*MatchResult{},
			Category:     "clean",
			Action:       ActionAllow,
			Reason:       "no sensitive words detected",
		}
	}

	score := 0.0
	categoryCount := make(map[string]int)
	categoryLevel := make(map[string]AlertLevel)
	matchedWords := make([]string, 0, len(matches))

	for _, match := range matches {
		matchedWords = append(matchedWords, match.Word)
		if match.Category != nil {
			cat := match.Category.Key
			categoryCount[cat]++
			if existingLevel, ok := categoryLevel[cat]; !ok || match.Category.Level < existingLevel {
				categoryLevel[cat] = match.Category.Level
			}
			switch match.Category.Level {
			case LevelP0:
				score += 0.5
			case LevelP1:
				score += 0.3
			case LevelP2:
				score += 0.1
			}
		}
	}

	score = math.Min(score, 1.0)

	// LevelP2=0 < LevelP1=1 < LevelP0=2, i.e. a LARGER value is MORE severe.
	// This loop used to look for `level < highestLevel` seeded with the least
	// severe level, so the condition never held and mainCategory was pinned to
	// "mixed" for every non-empty result — the audit field carried no
	// information at all. Compare with `>`, and break ties by hit count inside
	// the worst level so map iteration order cannot change the answer.
	mainCategory := "mixed"
	highestLevel := LevelP2
	maxCount := 0
	for cat, level := range categoryLevel {
		count := categoryCount[cat]
		if level > highestLevel || (level == highestLevel && count > maxCount) {
			highestLevel = level
			maxCount = count
			mainCategory = cat
		}
	}

	action := ActionAllow
	reason := ""
	// Wave 3 B5② (2026-09-22): thresholds were hardcoded 0.6/0.3; now
	// settings-driven (≤5s cache, admin-writable) with a defensive clamp
	// keeping warn below block.
	blockScore, warnScore := settings.CachedSensitiveScores()
	if score >= blockScore {
		action = ActionBlock
		reason = fmt.Sprintf("high risk score %.2f, %d sensitive words detected", score, len(matches))
	} else if score >= warnScore {
		action = ActionWarn
		reason = fmt.Sprintf("medium risk score %.2f, %d sensitive words detected", score, len(matches))
	} else {
		action = ActionAllow
		reason = fmt.Sprintf("low risk score %.2f, %d sensitive words detected", score, len(matches))
	}

	return &SafetyResult{
		Score:        score,
		MatchedWords: matchedWords,
		Matches:      matches,
		Category:     mainCategory,
		Action:       action,
		Reason:       reason,
	}
}

// GetHighestAlertLevel 获取匹配结果中最高的告警等级
func (e *SensitiveWordEngine) GetHighestAlertLevel(matches []*MatchResult) AlertLevel {
	if len(matches) == 0 {
		return LevelP2
	}
	// Same direction fix as EvaluateSafety: a bigger level is more severe, so
	// this must scan for the maximum. It previously scanned for the minimum
	// against a LevelP2 seed and therefore reported PASS for everything,
	// including BLOCK-level P0 hits.
	highest := LevelP2
	for _, match := range matches {
		if match.Category != nil && match.Category.Level > highest {
			highest = match.Category.Level
		}
	}
	return highest
}
