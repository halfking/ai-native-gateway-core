// FileBodiesStore 基于文件系统的会话内容（大对象）存储实现。
//
// 存储布局（分层目录，避免单目录文件过多）：
//
//	{baseDir}/{tenantID}/{sessionID前2位}/{sessionID}/turn_{turnNo}.json.{gz|zst}
//
// 每个轮次为一个独立文件，内容为 SessionBody 的 JSON 序列化后再经压缩
// 的字节流（LLM 请求/响应原文重复度高，压缩通常可节省 70% 以上空间）。
// 压缩编码由 codec 决定（gzip 为历史默认；zstd 为 2026-09 起的推荐默认，
// 后缀即编码标识，详见 codec.go），写入经由 AsyncFileWriter 异步落盘
// （临时文件 + rename 原子写），读取为同步操作（os.ReadFile + 解压 +
// json 反序列化），读路径按编码兜底顺序依次尝试，gzip 存量无需迁移。
package file

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/kaixuan/llm-gateway-go/monitoring"
	"github.com/kaixuan/llm-gateway-go/storage"
)

// 编译期断言：FileBodiesStore 必须实现一致性对账相关的全部接口。
var (
	_ storage.BodiesStore     = (*FileBodiesStore)(nil)
	_ storage.BodiesLister    = (*FileBodiesStore)(nil)
	_ storage.TurnFileDeleter = (*FileBodiesStore)(nil)
	_ storage.TurnFileStater  = (*FileBodiesStore)(nil)
)

// FileBodiesStore 会话内容文件存储。
type FileBodiesStore struct {
	baseDir string           // 存储根目录
	writer  *AsyncFileWriter // 复用异步写入器完成落盘
	codec   Codec            // 新写内容的压缩编码（读路径始终兼容全部已知编码）
}

// StoreOption 定制 FileBodiesStore 的可选项。
type StoreOption func(*FileBodiesStore)

// WithCodec 指定新写内容的压缩编码；零值构造函数默认 CodecGzip（历史行为），
// 生产装配经 factory 传入 config（默认 CodecZstd）。
func WithCodec(c Codec) StoreOption {
	return func(s *FileBodiesStore) { s.codec = c }
}

// NewFileBodiesStore 创建会话内容文件存储。
// workers 透传给底层 AsyncFileWriter（<=0 时由其取默认值 4）。
func NewFileBodiesStore(baseDir string, workers int, opts ...StoreOption) *FileBodiesStore {
	s := &FileBodiesStore{
		baseDir: baseDir,
		writer:  NewAsyncFileWriter(workers),
		codec:   CodecGzip,
	}
	for _, opt := range opts {
		if opt != nil {
			opt(s)
		}
	}
	return s
}

// readCodecs 返回读路径的编码尝试顺序：配置编码优先，其余按 AllCodecs
// 兜底（去重），保证新旧编码混存的目录可无迁移读取。
func (s *FileBodiesStore) readCodecs() []Codec {
	order := []Codec{s.codec}
	for _, c := range AllCodecs() {
		if c != s.codec {
			order = append(order, c)
		}
	}
	return order
}

// sessionPrefix 返回 sessionID 的前 2 位作为分层目录名；不足 2 位时用全量。
// 按 rune 切分，避免多字节字符被截断成非法的路径片段。
func sessionPrefix(sessionID string) string {
	runes := []rune(sessionID)
	if len(runes) <= 2 {
		return sessionID
	}
	return string(runes[:2])
}

// validPathID 校验 tenantID/sessionID 可安全地作为路径段：ID 直接拼入文件
// 路径，必须拒绝空串、"."、".." 及含路径分隔符的值，否则 `filepath.Join` 的
// Clean 会解析 "../"，攻击者可借 Read/Write/Delete（os.RemoveAll）逃逸 baseDir
// 读写删任意路径（审计 P1：路径遍历）。
func validPathID(id string) bool {
	return id != "" && id != "." && id != ".." && !strings.ContainsAny(id, `/\`)
}

// sessionDir 返回某个会话的轮次文件所在目录。
func (s *FileBodiesStore) sessionDir(tenantID, sessionID string) string {
	return filepath.Join(s.baseDir, tenantID, sessionPrefix(sessionID), sessionID)
}

// turnPath 返回指定编码下某轮次内容文件的存储路径：
// {baseDir}/{tenantID}/{sessionID前2位}/{sessionID}/turn_{turnNo}{suffix}
func (s *FileBodiesStore) turnPath(codec Codec, tenantID, sessionID string, turnNo int) string {
	return filepath.Join(s.sessionDir(tenantID, sessionID),
		fmt.Sprintf("turn_%d%s", turnNo, codec.Suffix()))
}

// Write 序列化并压缩一条会话内容后交由异步写入器落盘。
// Write 阻塞直到该文件写入完成（成功或失败），便于调用方确认落盘结果。
func (s *FileBodiesStore) Write(ctx context.Context, body *storage.SessionBody) error {
	if body == nil {
		return errors.New("file bodies store: body is nil")
	}
	if !validPathID(body.TenantID) {
		return fmt.Errorf("file bodies store: invalid tenantID %q", body.TenantID)
	}
	if !validPathID(body.SessionID) {
		return fmt.Errorf("file bodies store: invalid sessionID %q", body.SessionID)
	}

	// JSON 序列化（ctx 当前为预留参数，本地文件写暂不支持取消）
	data, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("file bodies store: marshal body turn %d of %s/%s: %w",
			body.TurnNo, body.TenantID, body.SessionID, err)
	}

	// 压缩（编码由 s.codec 决定，后缀即编码标识）
	compressed, err := s.codec.encode(data)
	if err != nil {
		return fmt.Errorf("file bodies store: compress turn %d of %s/%s: %w",
			body.TurnNo, body.TenantID, body.SessionID, err)
	}

	// 写入指标（审计 P1：/metrics/storage 的 writes 此前恒为零）
	writeErr := s.writer.Write(s.turnPath(s.codec, body.TenantID, body.SessionID, body.TurnNo), compressed)
	monitoring.Default().RecordWrite(len(compressed), writeErr)
	if writeErr != nil {
		return fmt.Errorf("file bodies store: write turn %d of %s/%s: %w",
			body.TurnNo, body.TenantID, body.SessionID, writeErr)
	}
	return nil
}

// Read 读取并解压指定轮次的会话内容。
// 文件不存在时返回 storage.ErrNotFound（可用 errors.Is 判定）；读取按
// readCodecs 顺序尝试各编码后缀（配置编码优先），历史 gzip 文件无需迁移。
// 压缩流或 JSON 损坏时返回带路径上下文的错误（不做跨编码重试：后缀即
// 编码标识，命中即定性）。
func (s *FileBodiesStore) Read(ctx context.Context, tenantID, sessionID string, turnNo int) (*storage.SessionBody, error) {
	if !validPathID(tenantID) {
		return nil, fmt.Errorf("file bodies store: invalid tenantID %q", tenantID)
	}
	if !validPathID(sessionID) {
		return nil, fmt.Errorf("file bodies store: invalid sessionID %q", sessionID)
	}

	for _, codec := range s.readCodecs() {
		path := s.turnPath(codec, tenantID, sessionID, turnNo)
		compressed, err := os.ReadFile(path)
		if err != nil {
			if os.IsNotExist(err) {
				continue // 尝试下一编码后缀
			}
			return nil, fmt.Errorf("file bodies store: read %s: %w", path, err)
		}

		data, err := codec.decode(compressed)
		if err != nil {
			return nil, fmt.Errorf("file bodies store: decompress %s: %w", path, err)
		}

		var body storage.SessionBody
		if err := json.Unmarshal(data, &body); err != nil {
			return nil, fmt.Errorf("file bodies store: unmarshal %s: %w", path, err)
		}
		return &body, nil
	}
	return nil, fmt.Errorf("file bodies store: turn %d of %s/%s: %w",
		turnNo, tenantID, sessionID, storage.ErrNotFound)
}

// ReadRange 按轮次区间 [startTurn, endTurn] 逐个读取会话内容。
// 缺失的轮次（仅 ErrNotFound）自动跳过；其他错误（如压缩流损坏）立即中止返回。
// startTurn > endTurn 时返回空切片。
func (s *FileBodiesStore) ReadRange(ctx context.Context, tenantID, sessionID string, startTurn, endTurn int) ([]*storage.SessionBody, error) {
	if startTurn > endTurn {
		return []*storage.SessionBody{}, nil
	}

	result := make([]*storage.SessionBody, 0, endTurn-startTurn+1)
	for turnNo := startTurn; turnNo <= endTurn; turnNo++ {
		body, err := s.Read(ctx, tenantID, sessionID, turnNo)
		if err != nil {
			if errors.Is(err, storage.ErrNotFound) {
				continue // 缺失轮次跳过，保证稀疏区间可读
			}
			return nil, err // 损坏等其他错误直接中止
		}
		result = append(result, body)
	}
	return result, nil
}

// Delete 删除整个会话目录（含全部轮次文件）。
// 目录不存在时同样返回 nil（幂等）。
func (s *FileBodiesStore) Delete(ctx context.Context, tenantID, sessionID string) error {
	if !validPathID(tenantID) {
		return fmt.Errorf("file bodies store: invalid tenantID %q", tenantID)
	}
	if !validPathID(sessionID) {
		return fmt.Errorf("file bodies store: invalid sessionID %q", sessionID)
	}

	dir := s.sessionDir(tenantID, sessionID)
	// os.RemoveAll 对不存在的路径返回 nil，天然幂等
	if err := os.RemoveAll(dir); err != nil {
		return fmt.Errorf("file bodies store: remove session dir %s: %w", dir, err)
	}
	return nil
}

// Close 关闭底层异步写入器（优雅排空队列后返回）。重复调用安全。
func (s *FileBodiesStore) Close() error {
	return s.writer.Close()
}

// ListTurns 返回某会话已落盘的 turn 编号（升序），供跨介质一致性对账
// （storage.ReconcileTurnArtifacts）使用。目录不存在视为空会话。
func (s *FileBodiesStore) ListTurns(_ context.Context, tenantID, sessionID string) ([]int, error) {
	if !validPathID(tenantID) {
		return nil, fmt.Errorf("file bodies store: invalid tenantID %q", tenantID)
	}
	if !validPathID(sessionID) {
		return nil, fmt.Errorf("file bodies store: invalid sessionID %q", sessionID)
	}
	entries, err := os.ReadDir(s.sessionDir(tenantID, sessionID))
	if err != nil {
		if os.IsNotExist(err) {
			return []int{}, nil
		}
		return nil, fmt.Errorf("file bodies store: read session dir %s: %w",
			s.sessionDir(tenantID, sessionID), err)
	}
	turns := make([]int, 0, len(entries))
	seen := make(map[int]struct{}, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		for _, codec := range AllCodecs() {
			var turnNo int
			if _, err := fmt.Sscanf(e.Name(), "turn_%d"+codec.Suffix(), &turnNo); err == nil {
				if _, ok := seen[turnNo]; !ok {
					turns = append(turns, turnNo)
					seen[turnNo] = struct{}{}
				}
				break
			}
		}
	}
	sort.Ints(turns)
	return turns, nil
}

// DeleteTurnFile 删除单个 turn 的内容文件（孤儿清理），实现
// storage.TurnFileDeleter。新旧编码后缀都删（同轮混存时避免清残留），
// 文件不存在时返回 nil（幂等）。
func (s *FileBodiesStore) DeleteTurnFile(_ context.Context, tenantID, sessionID string, turnNo int) error {
	if !validPathID(tenantID) {
		return fmt.Errorf("file bodies store: invalid tenantID %q", tenantID)
	}
	if !validPathID(sessionID) {
		return fmt.Errorf("file bodies store: invalid sessionID %q", sessionID)
	}
	for _, codec := range AllCodecs() {
		path := s.turnPath(codec, tenantID, sessionID, turnNo)
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("file bodies store: remove turn file %s: %w", path, err)
		}
	}
	return nil
}

// TurnFileModTime 返回单个 turn 内容文件的修改时间（mtime），实现
// storage.TurnFileStater，供孤儿删除的宽限判定（storage.RepairTurnArtifacts
// 的 G-#9 第二道保险）使用。按 readCodecs 顺序探测各编码后缀；全部不存在时
// 返回包装 storage.ErrNotFound 的错误（errors.Is 可命中）；其他 stat 失败
// 原样带上下文返回，由调用方保守处理。
func (s *FileBodiesStore) TurnFileModTime(_ context.Context, tenantID, sessionID string, turnNo int) (time.Time, error) {
	if !validPathID(tenantID) {
		return time.Time{}, fmt.Errorf("file bodies store: invalid tenantID %q", tenantID)
	}
	if !validPathID(sessionID) {
		return time.Time{}, fmt.Errorf("file bodies store: invalid sessionID %q", sessionID)
	}
	for _, codec := range s.readCodecs() {
		path := s.turnPath(codec, tenantID, sessionID, turnNo)
		info, err := os.Stat(path)
		if err != nil {
			if os.IsNotExist(err) {
				continue // 尝试下一编码后缀
			}
			return time.Time{}, fmt.Errorf("file bodies store: stat %s: %w", path, err)
		}
		return info.ModTime(), nil
	}
	return time.Time{}, fmt.Errorf("file bodies store: turn %d of %s/%s: %w",
		turnNo, tenantID, sessionID, storage.ErrNotFound)
}
