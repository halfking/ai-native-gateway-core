package config

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/fsnotify/fsnotify"
	"gopkg.in/yaml.v3"
)

// ConfigReloader watches and reloads configuration files.
type ConfigReloader interface {
	// Watch starts watching the configuration file for changes.
	Watch(ctx context.Context, configPath string) error

	// OnReload registers a handler to be called when config is reloaded.
	OnReload(handler ReloadHandler) error

	// Reload manually triggers a configuration reload.
	Reload(ctx context.Context) error

	// GetConfig returns the current configuration.
	GetConfig() *Config

	// Stop stops watching for configuration changes.
	Stop() error
}

// MetricsProvider 是实现了可观测指标的组件所满足的**可选**接口（P3-14）。
//
// ★ 为什么用可选接口而不是直接给 ConfigReloader 加方法：
//
//	ConfigReloader 是导出接口，给它加方法会**破坏**任何外部实现者
//	（接口方法集变大 ⇒ 旧实现不再满足该接口 ⇒ 编译失败）。
//	可选接口是 Go 里的惯例：调用方用类型断言探测能力，不满足就不取。
type MetricsProvider interface {
	// GetMetrics 返回当前指标的快照（键为指标名，值为计数）。
	GetMetrics() map[string]int64
}

// MetricsOf 探测 reloader 是否实现了 MetricsProvider，返回指标快照。
//
// 未实现时返回 nil（调用方需判空）。这是把 P3-14 的 watch 指标**暴露出去**的
// 正规入口：configReloader 是未导出类型，外部包拿不到它，只能经由接口取。
func MetricsOf(reloader ConfigReloader) map[string]int64 {
	if mp, ok := reloader.(MetricsProvider); ok {
		return mp.GetMetrics()
	}
	return nil
}

// ReloadHandler applies configuration before the new snapshot is published.
// It applies newConfig. On failure, handlers already attempted are invoked in
// reverse with the arguments swapped to restore the old state. Handlers must
// therefore be idempotent and support applying either configuration.
type ReloadHandler func(ctx context.Context, oldConfig, newConfig *Config) error

// Rejection identifies a completed failed reload while the rejected bytes
// still match the file on disk. ID is for local audit deduplication.
type Rejection struct {
	ID      [32]byte
	Invalid bool
}

type rejectedConfig struct {
	rejection Rejection
	fileHash  [32]byte
}

func RejectionOf(reloader ConfigReloader) (Rejection, bool) {
	if source, ok := reloader.(interface{ rejection() (Rejection, bool) }); ok {
		return source.rejection()
	}
	return Rejection{}, false
}

type configReloader struct {
	holder     *ConfigHolder
	validator  Validator
	handlers   []ReloadHandler
	watcher    *fsnotify.Watcher
	configPath string

	// rejectMu protects `rejected` **on its own**.
	//
	// ★ 2026-11-26 12h 审计轮：★ 原来 `rejected` 挂在 `reloadMu` 下，而读侧
	// `rejection()` 用的是 **`TryLock`** ⇒ **只要有一轮 reload 在飞，读侧就
	// 直接返回「没有拒收」** ⇒ 拒收状态**整轮漏报**（配置漂移检测不可靠，
	// 且对外可见性不确定）★★。
	//   ⇒ 现在读写各用 `rejectMu`（**独立的叶子锁**）⇒ ★ 读侧不再被
	//     「reload 进行中」挡住 ★；★ 读侧**先取路径、再取锁**⇒ 锁序只有
	//     「reloadMu → rejectMu」与「rejectMu」两条 ⇒ 无环 ★。
	rejectMu sync.Mutex
	mu       sync.RWMutex
	reloadMu sync.Mutex
	rejected *rejectedConfig // protected by reloadMu
	wg       sync.WaitGroup
	watching bool
	stopCh   chan struct{}
	stopped  bool
	lifetime context.Context
	cancel   context.CancelFunc
	stopOnce sync.Once
	stopErr  error

	// P3-14: Metrics for watch failures
	watchRebuildTotal    atomic.Int64
	watchRebuildFailures atomic.Int64
}

// NewConfigReloader creates a new configuration reloader.
func NewConfigReloader(initialConfig *Config, validator Validator) (ConfigReloader, error) {
	if initialConfig == nil || validator == nil {
		return nil, fmt.Errorf("initial config and validator are required")
	}
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, fmt.Errorf("create watcher: %w", err)
	}

	lifetime, cancel := context.WithCancel(context.Background())
	return &configReloader{
		holder:    NewConfigHolder(initialConfig),
		validator: validator,
		watcher:   watcher,
		stopCh:    make(chan struct{}),
		lifetime:  lifetime, cancel: cancel,
	}, nil
}

// Watch starts watching the configuration file.
func (r *configReloader) Watch(ctx context.Context, configPath string) error {
	path, err := filepath.Abs(configPath)
	if err != nil {
		return err
	}
	r.mu.Lock()
	if r.stopped || r.watching {
		r.mu.Unlock()
		return fmt.Errorf("reloader is stopped or already watching")
	}
	// Watch the directory so atomic replacement retains the watch on Linux too.
	if err := r.watcher.Add(filepath.Dir(path)); err != nil {
		r.mu.Unlock()
		return fmt.Errorf("watch config file: %w", err)
	}
	r.configPath = path
	r.watching = true
	r.wg.Add(1)
	r.mu.Unlock()

	log.Printf("INFO: Watching configuration file: %s", configPath)

	// Start watching goroutine
	go r.watch(ctx)

	return nil
}

// watch monitors file changes and triggers reloads.
func (r *configReloader) watch(ctx context.Context) {
	defer r.wg.Done()
	// Debounce timer to avoid multiple reloads on rapid file changes
	var timer *time.Timer
	var timerCh <-chan time.Time
	defer func() {
		if timer != nil {
			timer.Stop()
		}
	}()

	for {
		select {
		case event, ok := <-r.watcher.Events:
			if !ok {
				return
			}

			if filepath.Clean(event.Name) == r.configPath &&
				event.Op&(fsnotify.Write|fsnotify.Create|fsnotify.Rename|fsnotify.Remove) != 0 {
				log.Printf("INFO: Config file changed: %s (op: %s)", event.Name, event.Op)

				// P3-14: Rename/Remove 后重建 watch（防 atomic replace 后 inode 变化导致失效）。
				//
				// ★ 重建的**正确机制是「先 Remove 再 Add」**：
				//   fsnotify 按**路径字符串**去重，对同一路径再次 Add 会拿到
				//   **已有的那个 watch**（Linux/inotify 下返回同一个 wd，指向**旧 inode**）
				//   ⇒ 只 Add 不 Remove 根本不会重新挂到新 inode 上，等于没重建。
				//   （这正是 2026-11-15 那版 Remove+Add 的用意。）
				// ★ 因此本轮保留 Remove+Add 为主路径，把「指数退避重试」降级为
				//   **Add 失败时的兜底**，而不是替代。
				if event.Op&(fsnotify.Rename|fsnotify.Remove) != 0 {
					dir := filepath.Dir(r.configPath)
					r.watchRebuildTotal.Add(1)
					r.rebuildWatch(ctx, dir)
				}

				// Debounce: wait 100ms before reloading
				if timer != nil {
					timer.Stop()
				}
				timer = time.NewTimer(100 * time.Millisecond)
				timerCh = timer.C
			}

		case <-timerCh:
			timerCh = nil
			// Trigger reload after debounce
			if err := r.Reload(ctx); err != nil {
				log.Printf("ERROR: Config reload failed: %v", err)
			}

		case err, ok := <-r.watcher.Errors:
			if !ok {
				return
			}
			log.Printf("ERROR: Watcher error: %v", err)

		case <-r.stopCh:
			return

		case <-ctx.Done():
			return
		}
	}
}

// rebuildWatch 重建对配置目录的 watch（P3-14）。
//
// 主路径：Remove(dir) → Add(dir)（Remove 幂等，失败不致命）。
// 兜底：Add 失败时按指数退避重试（100ms / 200ms / 400ms，最多 3 次）。
//
// ★ 为什么必须先 Remove：fsnotify 按**路径**去重，对同一路径再次 Add 会复用
//
//	**已有的 watch**（inotify 下指向旧 inode）⇒ 只 Add 不 Remove 不会重新挂载。
//
// ★ 为什么是**同步**而不是起 goroutine：
//
//	① 原实现把重试放进 `go r.retryAddWatch(...)`，而 Stop() 里是 `r.wg.Wait()`；
//	   当计数器已归零时再 `wg.Add(1)` ⇒ 命中 Go runtime 的
//	   「WaitGroup misuse: Add called concurrently with Wait」panic；
//	② 异步重试在 Stop() 之后仍可能动 watcher（生命周期不受控）。
//	同步执行把重建收敛在 watch 循环内，最长阻塞 700ms；而这期间 Reload 会
//	**重新读文件**，取到的仍是最新内容 ⇒ 语义无损失。
func (r *configReloader) rebuildWatch(ctx context.Context, dir string) {
	_ = r.watcher.Remove(dir) // 幂等，失败不致命
	if err := r.watcher.Add(dir); err != nil {
		log.Printf("WARN: Rebuild watch for %s failed: %v — retrying with backoff", dir, err)
		r.retryAddWatch(ctx, dir, 1)
		return
	}
	log.Printf("INFO: Rebuilt watch for %s", dir)
}

// retryAddWatch **同步**重试把目录加回 watcher（指数退避）。成功返回 true。
//
// P3-14: 处理 Add 的瞬时失败（例如目录刚被 rename 掉、尚未重新出现）。
func (r *configReloader) retryAddWatch(ctx context.Context, dir string, attempt int) bool {
	const maxRetries = 3
	backoff := time.Duration(1<<uint(attempt-1)) * 100 * time.Millisecond // 100ms, 200ms, 400ms

	timer := time.NewTimer(backoff)
	defer timer.Stop()
	select {
	case <-timer.C:
	case <-ctx.Done():
		return false
	case <-r.stopCh:
		return false
	}

	if err := r.watcher.Add(dir); err != nil {
		r.watchRebuildFailures.Add(1)
		if attempt < maxRetries {
			log.Printf("WARN: Watch re-add failed (attempt %d/%d, will retry in %v): %v",
				attempt, maxRetries, backoff, err)
			return r.retryAddWatch(ctx, dir, attempt+1)
		}
		log.Printf("ERROR: Watch re-add failed after %d attempts: %v", maxRetries, err)
		return false
	}
	log.Printf("INFO: Watch re-established for %s after %d attempt(s)", dir, attempt)
	return true
}

// OnReload registers a reload handler.
func (r *configReloader) OnReload(handler ReloadHandler) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.stopped || handler == nil {
		return fmt.Errorf("reloader stopped or nil handler")
	}
	r.handlers = append(r.handlers, handler)
	return nil
}

// Reload manually triggers a configuration reload.
func (r *configReloader) Reload(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	stopCancel := context.AfterFunc(r.lifetime, cancel)
	defer func() { stopCancel(); cancel() }()
	r.reloadMu.Lock()
	defer r.reloadMu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	r.mu.RLock()
	if r.stopped {
		r.mu.RUnlock()
		return fmt.Errorf("reloader is stopped")
	}
	configPath := r.configPath
	handlers := make([]ReloadHandler, len(r.handlers))
	copy(handlers, r.handlers)
	r.mu.RUnlock()

	if configPath == "" {
		return fmt.Errorf("no config path set")
	}

	// Load old config
	oldConfig := r.holder.Get()

	r.rejectMu.Lock()
	r.rejected = nil
	r.rejectMu.Unlock()
	data, err := os.ReadFile(configPath)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	var next Config
	newConfig := &next
	if err := yaml.Unmarshal(data, newConfig); err != nil {
		r.rememberRejection(oldConfig, nil, data, true)
		return fmt.Errorf("load config: parse yaml: %w", err)
	}
	if err := r.validator.Validate(newConfig); err != nil {
		r.rememberRejection(oldConfig, newConfig, data, true)
		return fmt.Errorf("validate config: %w", err)
	}

	// Publish only after every application succeeds. Include the failing handler
	// in rollback: it may have partially changed its own state before returning.
	for i, handler := range handlers {
		if err := handler(ctx, oldConfig, newConfig); err != nil {
			failures := []error{fmt.Errorf("reload handler %d: %w", i, err)}
			rollbackCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
			defer cancel()
			for j := i; j >= 0; j-- {
				if rollbackErr := handlers[j](rollbackCtx, newConfig, oldConfig); rollbackErr != nil {
					failures = append(failures, fmt.Errorf("rollback handler %d: %w", j, rollbackErr))
				}
			}
			if ctx.Err() == nil {
				r.rememberRejection(oldConfig, newConfig, data, false)
			}
			return errors.Join(failures...)
		}
	}
	r.holder.Set(newConfig)
	log.Printf("INFO: Configuration reloaded successfully")
	return nil
}

const maxDriftConfigBytes = 1 << 20

func (r *configReloader) rememberRejection(old, desired *Config, data []byte, invalid bool) {
	if len(data) > maxDriftConfigBytes {
		return
	}
	actualBytes, _ := json.Marshal(old) // Config contains only JSON-native values.
	actualHash := sha256.Sum256(actualBytes)
	fileHash := sha256.Sum256(data)
	desiredHash := fileHash
	if desired != nil {
		proposed, _ := json.Marshal(desired)
		desiredHash = sha256.Sum256(proposed)
	}
	pair := [64]byte{}
	copy(pair[:32], actualHash[:])
	copy(pair[32:], desiredHash[:])
	r.rejectMu.Lock()
	r.rejected = &rejectedConfig{
		rejection: Rejection{ID: sha256.Sum256(pair[:]), Invalid: invalid},
		fileHash:  fileHash,
	}
	r.rejectMu.Unlock()
}

func (r *configReloader) rejection() (Rejection, bool) {
	// ★ 2026-11-26 12h 审计轮：★ **不再用 TryLock 表达「没有拒收」** ★
	// （★ TryLock 在「reload 在飞」时返回 false ⇒ **拒收被静默吞掉** ★）。
	// ⇒ 现在：★ 先取路径（独立读锁）⇒ 再取 `rejectMu`（叶子锁）⇒
	//   **拒收状态不会因为一轮 reload 在飞而漏报** ★。
	r.mu.RLock()
	path := r.configPath
	r.mu.RUnlock()
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return Rejection{}, false
	}
	r.rejectMu.Lock()
	defer r.rejectMu.Unlock()
	if r.rejected == nil {
		return Rejection{}, false
	}
	file, err := os.Open(path)
	if err != nil {
		return Rejection{}, false
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxDriftConfigBytes+1))
	if err != nil || len(data) > maxDriftConfigBytes || sha256.Sum256(data) != r.rejected.fileHash {
		return Rejection{}, false
	}
	return r.rejected.rejection, true
}

// GetConfig returns the current configuration.
func (r *configReloader) GetConfig() *Config {
	return r.holder.Get()
}

// Stop stops watching for configuration changes.
func (r *configReloader) Stop() error {
	r.stopOnce.Do(func() {
		r.mu.Lock()
		r.stopped = true
		r.cancel()
		close(r.stopCh)
		r.mu.Unlock()
		r.stopErr = r.watcher.Close()
		r.wg.Wait()
		r.reloadMu.Lock()
		r.reloadMu.Unlock()
		log.Println("INFO: Config reloader stopped")
	})
	return r.stopErr
}

// GetMetrics returns the current watch metrics (P3-14).
// This is useful for monitoring watch rebuild failures.
func (r *configReloader) GetMetrics() map[string]int64 {
	return map[string]int64{
		"plugin_config_watch_rebuild_total":    r.watchRebuildTotal.Load(),
		"plugin_config_watch_rebuild_failures": r.watchRebuildFailures.Load(),
	}
}
