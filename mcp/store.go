package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// BootstrapRecord 是密钥引导（apply/claim）的辅助状态。
type BootstrapRecord struct {
	Kind string `json:"kind"`
	// Token apply 的幂等键或 claim 的一次性领取令牌。
	Token string `json:"token"`
	// KeyID 关联的凭证 ID。
	KeyID string `json:"key_id,omitempty"`
	// Plaintext 一次性明文（仅 claim 之前保存，领取后清空）。
	Plaintext string `json:"plaintext,omitempty"`
	// ClaimTokenHash binds the one-time claim to the token returned by Apply.
	ClaimTokenHash string   `json:"claim_token_hash,omitempty"`
	KeySpec        *KeySpec `json:"key_spec,omitempty"`
	// Status 状态：pending / approved / claimed / rejected。
	Status string `json:"status"`
	// TenantID / Device / IP 归属信息。
	TenantID string `json:"tenant_id,omitempty"`
	Device   string `json:"device,omitempty"`
	IP       string `json:"ip,omitempty"`
	// Bindings 领取时下发的后端地址。
	Bindings  map[string]string `json:"bindings,omitempty"`
	CreatedAt time.Time         `json:"created_at"`
	UpdatedAt time.Time         `json:"updated_at"`
}

// Store 是网关注册面（控制面）的持久化抽象。
//
// 默认实现是内存实现（NewMemoryStore）；需要落盘时用 NewFileStore。
// 之所以不直接落在 Postgres：注册面是低频写、高频读的小数据集，且网关需要
// 能作为「独立部署」的轻量进程运行（不牵动数据库迁移链）。
type Store interface {
	// ListAdapters 列出适配器（租户内 + 共享）。
	ListAdapters(ctx context.Context, tenantID string) ([]Adapter, error)
	// GetAdapter 按名取适配器；租户优先，回退共享。
	GetAdapter(ctx context.Context, tenantID, name string) (*Adapter, error)
	// PutAdapter 新增或更新适配器。
	PutAdapter(ctx context.Context, a Adapter) error
	// DeleteAdapter 删除适配器。
	DeleteAdapter(ctx context.Context, tenantID, name string) error

	// ListTools 列出工具（租户内 + 共享）。
	ListTools(ctx context.Context, tenantID string) ([]Tool, error)
	// GetTool 按名取工具（租户优先，回退共享）。
	GetTool(ctx context.Context, tenantID, name string) (*Tool, error)
	// GetToolAnyTenant 按名跨租户查找工具（用于判定「该名已被别的租户私有化」）。
	GetToolAnyTenant(ctx context.Context, name string) (*Tool, error)
	// PutTool 新增或更新工具。
	PutTool(ctx context.Context, t Tool) error
	// DeleteTool 删除工具。
	DeleteTool(ctx context.Context, tenantID, name string) error

	// GetKeyByHash 按明文哈希取凭证。
	GetKeyByHash(ctx context.Context, tenantID, hash string) (*Key, error)
	// GetKey 按 ID 取凭证。
	GetKey(ctx context.Context, tenantID, id string) (*Key, error)
	// ListKeys 列出凭证。
	ListKeys(ctx context.Context, tenantID string) ([]Key, error)
	// PutKey 新增或更新凭证。
	PutKey(ctx context.Context, k Key) error
	// TouchKey updates usage without overwriting concurrent credential changes.
	TouchKey(ctx context.Context, tenantID, id string, at time.Time) error

	// ListAgents 列出智能体。
	ListAgents(ctx context.Context, tenantID string) ([]Agent, error)
	// GetAgent 按 ID 取智能体。
	GetAgent(ctx context.Context, tenantID, id string) (*Agent, error)
	// PutAgent 新增或更新智能体。
	PutAgent(ctx context.Context, a Agent) error
	// DeleteAgent 删除智能体。
	DeleteAgent(ctx context.Context, tenantID, id string) error

	// ListSkills 列出技能。
	ListSkills(ctx context.Context, tenantID string) ([]Skill, error)
	// GetSkill 按 ID 取技能。
	GetSkill(ctx context.Context, tenantID, id string) (*Skill, error)
	// PutSkill 新增或更新技能。
	PutSkill(ctx context.Context, s Skill) error
	// DeleteSkill 删除技能。
	DeleteSkill(ctx context.Context, tenantID, id string) error

	// GetApproval 按 ID 取审批。
	GetApproval(ctx context.Context, id string) (*Approval, error)
	// PutApproval 新增或更新审批。
	PutApproval(ctx context.Context, a Approval) error
	// ListApprovals 列出审批。
	ListApprovals(ctx context.Context, tenantID string) ([]Approval, error)

	// PutBootstrap 写入引导状态。
	PutBootstrap(ctx context.Context, rec BootstrapRecord) error
	// GetBootstrap 读取引导状态。
	GetBootstrap(ctx context.Context, kind, token string) (*BootstrapRecord, error)
	// ClaimBootstrap validates the claim token and consumes plaintext atomically.
	ClaimBootstrap(ctx context.Context, requestID, tokenHash string, at time.Time) (*BootstrapRecord, error)
	// DeleteBootstrap 删除引导状态。
	DeleteBootstrap(ctx context.Context, kind, token string) error
}

// ---------------------------------------------------------------------------
// 内存实现
// ---------------------------------------------------------------------------

type memoryStore struct {
	mu sync.RWMutex

	adapters  map[string]Adapter
	tools     map[string]Tool
	keys      map[string]Key // id → key
	keyHash   map[string]string
	agents    map[string]Agent
	skills    map[string]Skill
	approvals map[string]Approval
	bootstrap map[string]BootstrapRecord
}

// NewMemoryStore 构造内存注册面（默认实现；重启不保留）。
func NewMemoryStore() Store {
	return &memoryStore{
		adapters:  map[string]Adapter{},
		tools:     map[string]Tool{},
		keys:      map[string]Key{},
		keyHash:   map[string]string{},
		agents:    map[string]Agent{},
		skills:    map[string]Skill{},
		approvals: map[string]Approval{},
		bootstrap: map[string]BootstrapRecord{},
	}
}

func storeKey(tenantID, name string) string {
	return tenantID + "\x00" + name
}

// visible 判定某租户是否可见该资源。
//
// tenantID 为空表示「不按租户过滤」（管理面/账本视角）；否则可见共享资源
// （owner 为空）与本租户自有资源。
func visible(tenantID, owner string) bool {
	if tenantID == "" {
		return true
	}
	return owner == "" || owner == tenantID
}

func (s *memoryStore) ListAdapters(_ context.Context, tenantID string) ([]Adapter, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Adapter, 0, len(s.adapters))
	for _, a := range s.adapters {
		if visible(tenantID, a.TenantID) {
			out = append(out, a)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func (s *memoryStore) GetAdapter(_ context.Context, tenantID, name string) (*Adapter, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if a, ok := s.adapters[storeKey(tenantID, name)]; ok {
		return &a, nil
	}
	if a, ok := s.adapters[storeKey("", name)]; ok {
		return &a, nil
	}
	return nil, nil
}

func (s *memoryStore) PutAdapter(_ context.Context, a Adapter) error {
	if a.Name == "" {
		return fmt.Errorf("%w: adapter name required", ErrConflict)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.adapters[storeKey(a.TenantID, a.Name)] = a
	return nil
}

func (s *memoryStore) DeleteAdapter(_ context.Context, tenantID, name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.adapters, storeKey(tenantID, name))
	return nil
}

func (s *memoryStore) ListTools(_ context.Context, tenantID string) ([]Tool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Tool, 0, len(s.tools))
	for _, t := range s.tools {
		if visible(tenantID, t.TenantID) {
			out = append(out, t)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func (s *memoryStore) GetTool(_ context.Context, tenantID, name string) (*Tool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if t, ok := s.tools[storeKey(tenantID, name)]; ok {
		return &t, nil
	}
	if t, ok := s.tools[storeKey("", name)]; ok {
		return &t, nil
	}
	return nil, nil
}

func (s *memoryStore) GetToolAnyTenant(_ context.Context, name string) (*Tool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for key, t := range s.tools {
		if strings.HasSuffix(key, "\x00"+name) {
			tt := t
			return &tt, nil
		}
	}
	return nil, nil
}

func (s *memoryStore) PutTool(_ context.Context, t Tool) error {
	if t.Name == "" {
		return fmt.Errorf("%w: tool name required", ErrConflict)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tools[storeKey(t.TenantID, t.Name)] = t
	return nil
}

func (s *memoryStore) DeleteTool(_ context.Context, tenantID, name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.tools, storeKey(tenantID, name))
	return nil
}

func (s *memoryStore) GetKeyByHash(_ context.Context, tenantID, hash string) (*Key, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	id, ok := s.keyHash[tenantID+"\x00"+hash]
	if !ok {
		id, ok = s.keyHash["\x00"+hash]
	}
	if !ok {
		return nil, nil
	}
	k, ok := s.keys[id]
	if !ok {
		return nil, nil
	}
	return &k, nil
}

func (s *memoryStore) GetKey(_ context.Context, tenantID, id string) (*Key, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	k, ok := s.keys[id]
	if !ok {
		return nil, nil
	}
	if k.TenantID != "" && k.TenantID != tenantID {
		return nil, nil
	}
	return &k, nil
}

func (s *memoryStore) ListKeys(_ context.Context, tenantID string) ([]Key, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Key, 0, len(s.keys))
	for _, k := range s.keys {
		if visible(tenantID, k.TenantID) {
			out = append(out, k)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func (s *memoryStore) PutKey(_ context.Context, k Key) error {
	if k.ID == "" {
		return fmt.Errorf("%w: key id required", ErrConflict)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.keys[k.ID] = k
	if k.Hash != "" {
		s.keyHash[k.TenantID+"\x00"+k.Hash] = k.ID
		s.keyHash["\x00"+k.Hash] = k.ID
	}
	return nil
}

func (s *memoryStore) ListAgents(_ context.Context, tenantID string) ([]Agent, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Agent, 0, len(s.agents))
	for _, a := range s.agents {
		if visible(tenantID, a.TenantID) {
			out = append(out, a)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func (s *memoryStore) GetAgent(_ context.Context, tenantID, id string) (*Agent, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	a, ok := s.agents[id]
	if !ok {
		return nil, nil
	}
	if a.TenantID != "" && a.TenantID != tenantID {
		return nil, nil
	}
	return &a, nil
}

func (s *memoryStore) PutAgent(_ context.Context, a Agent) error {
	if a.ID == "" {
		return fmt.Errorf("%w: agent id required", ErrConflict)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.agents[a.ID] = a
	return nil
}

func (s *memoryStore) DeleteAgent(_ context.Context, tenantID, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.agents, id)
	return nil
}

func (s *memoryStore) ListSkills(_ context.Context, tenantID string) ([]Skill, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Skill, 0, len(s.skills))
	for _, sk := range s.skills {
		if visible(tenantID, sk.TenantID) {
			out = append(out, sk)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func (s *memoryStore) GetSkill(_ context.Context, tenantID, id string) (*Skill, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	sk, ok := s.skills[id]
	if !ok {
		return nil, nil
	}
	if sk.TenantID != "" && sk.TenantID != tenantID {
		return nil, nil
	}
	return &sk, nil
}

func (s *memoryStore) PutSkill(_ context.Context, sk Skill) error {
	if sk.ID == "" {
		return fmt.Errorf("%w: skill id required", ErrConflict)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.skills[sk.ID] = sk
	return nil
}

func (s *memoryStore) DeleteSkill(_ context.Context, tenantID, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.skills, id)
	return nil
}

func (s *memoryStore) GetApproval(_ context.Context, id string) (*Approval, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	a, ok := s.approvals[id]
	if !ok {
		return nil, nil
	}
	return &a, nil
}

func (s *memoryStore) PutApproval(_ context.Context, a Approval) error {
	if a.ID == "" {
		return fmt.Errorf("%w: approval id required", ErrConflict)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.approvals[a.ID] = a
	return nil
}

func (s *memoryStore) ListApprovals(_ context.Context, tenantID string) ([]Approval, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Approval, 0, len(s.approvals))
	for _, a := range s.approvals {
		if visible(tenantID, a.TenantID) {
			out = append(out, a)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out, nil
}

func (s *memoryStore) PutBootstrap(_ context.Context, rec BootstrapRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.bootstrap[rec.Kind+"\x00"+rec.Token] = rec
	return nil
}

func (s *memoryStore) GetBootstrap(_ context.Context, kind, token string) (*BootstrapRecord, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	rec, ok := s.bootstrap[kind+"\x00"+token]
	if !ok {
		return nil, nil
	}
	return &rec, nil
}

func (s *memoryStore) DeleteBootstrap(_ context.Context, kind, token string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.bootstrap, kind+"\x00"+token)
	return nil
}

// ---------------------------------------------------------------------------
// 文件实现（原子落盘）
// ---------------------------------------------------------------------------

type fileStore struct {
	mu   sync.RWMutex
	path string
	data fileStoreData
}

type fileStoreData struct {
	Adapters  map[string]Adapter         `json:"adapters"`
	Tools     map[string]Tool            `json:"tools"`
	Keys      map[string]Key             `json:"keys"`
	Agents    map[string]Agent           `json:"agents"`
	Skills    map[string]Skill           `json:"skills"`
	Approvals map[string]Approval        `json:"approvals"`
	Bootstrap map[string]BootstrapRecord `json:"bootstrap"`
}

// NewFileStore 构造文件持久化的注册面。
//
// 落盘采用「临时文件 + rename」保证原子性；进程内所有写操作串行化。
func NewFileStore(path string) (Store, error) {
	s := &fileStore{
		path: path,
		data: fileStoreData{
			Adapters:  map[string]Adapter{},
			Tools:     map[string]Tool{},
			Keys:      map[string]Key{},
			Agents:    map[string]Agent{},
			Skills:    map[string]Skill{},
			Approvals: map[string]Approval{},
			Bootstrap: map[string]BootstrapRecord{},
		},
	}
	raw, err := os.ReadFile(path)
	switch {
	case err == nil:
		if err := json.Unmarshal(raw, &s.data); err != nil {
			return nil, fmt.Errorf("mcp: read store %s: %w", path, err)
		}
		s.ensureMaps()
	case os.IsNotExist(err):
		// 首次启动：空注册面。
	default:
		return nil, fmt.Errorf("mcp: read store %s: %w", path, err)
	}
	return s, nil
}

func (s *fileStore) ensureMaps() {
	if s.data.Adapters == nil {
		s.data.Adapters = map[string]Adapter{}
	}
	if s.data.Tools == nil {
		s.data.Tools = map[string]Tool{}
	}
	if s.data.Keys == nil {
		s.data.Keys = map[string]Key{}
	}
	if s.data.Agents == nil {
		s.data.Agents = map[string]Agent{}
	}
	if s.data.Skills == nil {
		s.data.Skills = map[string]Skill{}
	}
	if s.data.Approvals == nil {
		s.data.Approvals = map[string]Approval{}
	}
	if s.data.Bootstrap == nil {
		s.data.Bootstrap = map[string]BootstrapRecord{}
	}
}

func (s *fileStore) persistLocked() error {
	raw, err := json.MarshalIndent(s.data, "", "  ")
	if err != nil {
		return fmt.Errorf("mcp: marshal store: %w", err)
	}
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("mcp: mkdir store dir: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".mcp-store-*.tmp")
	if err != nil {
		return fmt.Errorf("mcp: temp store file: %w", err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	if _, err := tmp.Write(raw); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("mcp: write store file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("mcp: sync store file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("mcp: close store file: %w", err)
	}
	if err := os.Rename(tmpName, s.path); err != nil {
		return fmt.Errorf("mcp: rename store file: %w", err)
	}
	return nil
}

func (s *fileStore) ListAdapters(_ context.Context, tenantID string) ([]Adapter, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Adapter, 0, len(s.data.Adapters))
	for _, a := range s.data.Adapters {
		if visible(tenantID, a.TenantID) {
			out = append(out, a)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func (s *fileStore) GetAdapter(_ context.Context, tenantID, name string) (*Adapter, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if a, ok := s.data.Adapters[storeKey(tenantID, name)]; ok {
		return &a, nil
	}
	if a, ok := s.data.Adapters[storeKey("", name)]; ok {
		return &a, nil
	}
	return nil, nil
}

func (s *fileStore) PutAdapter(_ context.Context, a Adapter) error {
	if a.Name == "" {
		return fmt.Errorf("%w: adapter name required", ErrConflict)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data.Adapters[storeKey(a.TenantID, a.Name)] = a
	return s.persistLocked()
}

func (s *fileStore) DeleteAdapter(_ context.Context, tenantID, name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.data.Adapters, storeKey(tenantID, name))
	return s.persistLocked()
}

func (s *fileStore) ListTools(_ context.Context, tenantID string) ([]Tool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Tool, 0, len(s.data.Tools))
	for _, t := range s.data.Tools {
		if visible(tenantID, t.TenantID) {
			out = append(out, t)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func (s *fileStore) GetTool(_ context.Context, tenantID, name string) (*Tool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if t, ok := s.data.Tools[storeKey(tenantID, name)]; ok {
		return &t, nil
	}
	if t, ok := s.data.Tools[storeKey("", name)]; ok {
		return &t, nil
	}
	return nil, nil
}

func (s *fileStore) GetToolAnyTenant(_ context.Context, name string) (*Tool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, t := range s.data.Tools {
		if t.Name == name {
			tt := t
			return &tt, nil
		}
	}
	return nil, nil
}

func (s *fileStore) PutTool(_ context.Context, t Tool) error {
	if t.Name == "" {
		return fmt.Errorf("%w: tool name required", ErrConflict)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data.Tools[storeKey(t.TenantID, t.Name)] = t
	return s.persistLocked()
}

func (s *fileStore) DeleteTool(_ context.Context, tenantID, name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.data.Tools, storeKey(tenantID, name))
	return s.persistLocked()
}

func (s *fileStore) GetKeyByHash(_ context.Context, tenantID, hash string) (*Key, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, k := range s.data.Keys {
		if k.Hash == hash && (k.TenantID == "" || k.TenantID == tenantID) {
			kk := k
			return &kk, nil
		}
	}
	return nil, nil
}

func (s *fileStore) GetKey(_ context.Context, tenantID, id string) (*Key, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	k, ok := s.data.Keys[id]
	if !ok {
		return nil, nil
	}
	if k.TenantID != "" && k.TenantID != tenantID {
		return nil, nil
	}
	return &k, nil
}

func (s *fileStore) ListKeys(_ context.Context, tenantID string) ([]Key, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Key, 0, len(s.data.Keys))
	for _, k := range s.data.Keys {
		if visible(tenantID, k.TenantID) {
			out = append(out, k)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func (s *fileStore) PutKey(_ context.Context, k Key) error {
	if k.ID == "" {
		return fmt.Errorf("%w: key id required", ErrConflict)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data.Keys[k.ID] = k
	return s.persistLocked()
}

func (s *fileStore) ListAgents(_ context.Context, tenantID string) ([]Agent, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Agent, 0, len(s.data.Agents))
	for _, a := range s.data.Agents {
		if visible(tenantID, a.TenantID) {
			out = append(out, a)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func (s *fileStore) GetAgent(_ context.Context, tenantID, id string) (*Agent, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	a, ok := s.data.Agents[id]
	if !ok {
		return nil, nil
	}
	if a.TenantID != "" && a.TenantID != tenantID {
		return nil, nil
	}
	return &a, nil
}

func (s *fileStore) PutAgent(_ context.Context, a Agent) error {
	if a.ID == "" {
		return fmt.Errorf("%w: agent id required", ErrConflict)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data.Agents[a.ID] = a
	return s.persistLocked()
}

func (s *fileStore) DeleteAgent(_ context.Context, tenantID, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.data.Agents, id)
	return s.persistLocked()
}

func (s *fileStore) ListSkills(_ context.Context, tenantID string) ([]Skill, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Skill, 0, len(s.data.Skills))
	for _, sk := range s.data.Skills {
		if visible(tenantID, sk.TenantID) {
			out = append(out, sk)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func (s *fileStore) GetSkill(_ context.Context, tenantID, id string) (*Skill, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	sk, ok := s.data.Skills[id]
	if !ok {
		return nil, nil
	}
	if sk.TenantID != "" && sk.TenantID != tenantID {
		return nil, nil
	}
	return &sk, nil
}

func (s *fileStore) PutSkill(_ context.Context, sk Skill) error {
	if sk.ID == "" {
		return fmt.Errorf("%w: skill id required", ErrConflict)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data.Skills[sk.ID] = sk
	return s.persistLocked()
}

func (s *fileStore) DeleteSkill(_ context.Context, tenantID, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.data.Skills, id)
	return s.persistLocked()
}

func (s *fileStore) GetApproval(_ context.Context, id string) (*Approval, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	a, ok := s.data.Approvals[id]
	if !ok {
		return nil, nil
	}
	return &a, nil
}

func (s *fileStore) PutApproval(_ context.Context, a Approval) error {
	if a.ID == "" {
		return fmt.Errorf("%w: approval id required", ErrConflict)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data.Approvals[a.ID] = a
	return s.persistLocked()
}

func (s *fileStore) ListApprovals(_ context.Context, tenantID string) ([]Approval, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Approval, 0, len(s.data.Approvals))
	for _, a := range s.data.Approvals {
		if visible(tenantID, a.TenantID) {
			out = append(out, a)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out, nil
}

func (s *fileStore) PutBootstrap(_ context.Context, rec BootstrapRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data.Bootstrap[rec.Kind+"\x00"+rec.Token] = rec
	return s.persistLocked()
}

func (s *fileStore) GetBootstrap(_ context.Context, kind, token string) (*BootstrapRecord, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	rec, ok := s.data.Bootstrap[kind+"\x00"+token]
	if !ok {
		return nil, nil
	}
	return &rec, nil
}

func (s *fileStore) DeleteBootstrap(_ context.Context, kind, token string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.data.Bootstrap, kind+"\x00"+token)
	return s.persistLocked()
}
