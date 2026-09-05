package credentialfpslot

import (
	"context"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/identity"
	"github.com/redis/go-redis/v9"
)

// getTestRedis returns a Redis client for testing, or nil if unavailable
func getTestRedis() *redis.Client {
	redisAddr := os.Getenv("REDIS_ADDR")
	if redisAddr == "" {
		redisAddr = "localhost:6379"
	}

	client := redis.NewClient(&redis.Options{
		Addr: redisAddr,
	})

	// Test connection
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()

	if err := client.Ping(ctx).Err(); err != nil {
		return nil
	}

	return client
}

// TestMetricsRecording 测试指标记录函数
func TestMetricsRecording(t *testing.T) {
	// 测试各种指标记录函数不应 panic
	recordAcquireSuccess()
	recordAcquireSaturated()
	recordAcquireRedisError()
	recordReleaseSuccess()
	recordReleaseFailure()
	recordPreempt()
	recordReclaim()
	updateUtilization(123, 10, 5) // credentialID, limit, used
}

// TestApplyEgressHeaders 测试 Egress 头部应用
func TestApplyEgressHeaders(t *testing.T) {
	tests := []struct {
		name   string
		egress *identity.EgressIdentity
	}{
		{
			name:   "nil egress",
			egress: nil,
		},
		{
			name: "valid egress",
			egress: &identity.EgressIdentity{
				EgressSeed:      "seed123",
				VirtualClientID: "client456",
				VirtualIP:       "192.168.1.1",
				VirtualMAC:      "00:11:22:33:44:55",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			header := http.Header{}
			ApplyEgressHeaders(header, tt.egress)

			if tt.egress != nil {
				if header.Get("X-Device-Seed") != tt.egress.EgressSeed {
					t.Errorf("X-Device-Seed mismatch")
				}
				if header.Get("X-Virtual-Client-Id") != tt.egress.VirtualClientID {
					t.Errorf("X-Virtual-Client-Id mismatch")
				}
				if header.Get("X-Virtual-IP") != tt.egress.VirtualIP {
					t.Errorf("X-Virtual-IP mismatch")
				}
				if header.Get("X-Virtual-MAC") != tt.egress.VirtualMAC {
					t.Errorf("X-Virtual-MAC mismatch")
				}
			}
		})
	}
}

// TestReleaseSlot 测试单个槽位释放
func TestReleaseSlot(t *testing.T) {
	redis := getTestRedis()
	if redis == nil {
		t.Skip("Redis not available")
	}

	mgr := New(Config{
		DefaultLimit: 10,
		Enabled:      true,
	}, redis)

	ctx := context.Background()
	credentialID := 789
	limit := 10

	// 先获取一个槽位
	lease, ok := mgr.Acquire(ctx, credentialID, &limit, "holder-release-test", "default")
	if !ok {
		t.Fatal("failed to acquire slot")
	}

	// 释放单个槽位
	released, err := mgr.ReleaseSlot(ctx, credentialID, lease.SlotIndex)
	if err != nil {
		t.Fatalf("ReleaseSlot failed: %v", err)
	}
	if !released {
		t.Error("expected slot to be released")
	}

	// 再次释放同一槽位应该返回 false（已经释放）
	released2, err := mgr.ReleaseSlot(ctx, credentialID, lease.SlotIndex)
	if err != nil {
		t.Fatalf("ReleaseSlot failed: %v", err)
	}
	if released2 {
		t.Error("expected slot to already be free")
	}
}

// TestNewZeroNodeState 测试零值节点状态创建
func TestNewZeroNodeState(t *testing.T) {
	state := newZeroNodeState(123, "gpt-4")
	if state == nil {
		t.Fatal("newZeroNodeState returned nil")
	}
	if state.CredentialID != 123 {
		t.Errorf("CredentialID = %d, want 123", state.CredentialID)
	}
	if state.Model != "gpt-4" {
		t.Errorf("Model = %s, want gpt-4", state.Model)
	}
	if state.SlideWindow == nil {
		t.Error("SlideWindow should not be nil")
	}
	if len(state.SlideWindow) != 0 {
		t.Errorf("SlideWindow length = %d, want 0", len(state.SlideWindow))
	}
}

// TestParseSlotKey 测试槽位键解析
func TestParseSlotKey(t *testing.T) {
	tests := []struct {
		name        string
		key         string
		wantCredID  int
		wantSlotIdx int
		wantOK      bool
	}{
		{
			name:        "valid key",
			key:         "llmgw:tenant:default:cred_fp_slot:123:4",
			wantCredID:  123,
			wantSlotIdx: 4,
			wantOK:      true,
		},
		{
			name:   "invalid key - no marker",
			key:    "llmgw:some:other:key",
			wantOK: false,
		},
		{
			name:   "invalid key - no slot index",
			key:    "llmgw:cred_fp_slot:123",
			wantOK: false,
		},
		{
			name:   "invalid key - bad credential ID",
			key:    "llmgw:cred_fp_slot:abc:4",
			wantOK: false,
		},
		{
			name:   "invalid key - bad slot index",
			key:    "llmgw:cred_fp_slot:123:xyz",
			wantOK: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			credID, slotIdx, ok := parseSlotKey(tt.key)
			if ok != tt.wantOK {
				t.Errorf("parseSlotKey() ok = %v, want %v", ok, tt.wantOK)
			}
			if ok {
				if credID != tt.wantCredID {
					t.Errorf("credID = %d, want %d", credID, tt.wantCredID)
				}
				if slotIdx != tt.wantSlotIdx {
					t.Errorf("slotIdx = %d, want %d", slotIdx, tt.wantSlotIdx)
				}
			}
		})
	}
}

// TestResolveReclaimIdleSeconds 测试回收空闲秒数解析
func TestResolveReclaimIdleSeconds(t *testing.T) {
	tests := []struct {
		name   string
		config Config
		want   int
	}{
		{
			name:   "default value",
			config: Config{ReclaimIdleSeconds: 0},
			want:   DefaultReclaimIdleSeconds,
		},
		{
			name:   "negative value",
			config: Config{ReclaimIdleSeconds: -1},
			want:   DefaultReclaimIdleSeconds,
		},
		{
			name:   "custom value",
			config: Config{ReclaimIdleSeconds: 3600},
			want:   3600,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.config.resolveReclaimIdleSeconds()
			if got != tt.want {
				t.Errorf("resolveReclaimIdleSeconds() = %d, want %d", got, tt.want)
			}
		})
	}
}

// TestResolveActiveGateSeconds 测试活跃门限秒数解析
func TestResolveActiveGateSeconds(t *testing.T) {
	tests := []struct {
		name   string
		config Config
		want   int
	}{
		{
			name:   "default value",
			config: Config{ActiveGateSeconds: 0},
			want:   DefaultActiveGateSeconds,
		},
		{
			name:   "negative value",
			config: Config{ActiveGateSeconds: -1},
			want:   DefaultActiveGateSeconds,
		},
		{
			name:   "custom value",
			config: Config{ActiveGateSeconds: 600},
			want:   600,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.config.resolveActiveGateSeconds()
			if got != tt.want {
				t.Errorf("resolveActiveGateSeconds() = %d, want %d", got, tt.want)
			}
		})
	}
}

// TestStartReclaim 测试启动回收循环
func TestStartReclaim(t *testing.T) {
	redis := getTestRedis()
	if redis == nil {
		t.Skip("Redis not available")
	}

	mgr := New(Config{
		DefaultLimit:       10,
		Enabled:            true,
		ReclaimIdleSeconds: 1, // 短时间用于测试
	}, redis)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	// 启动回收循环
	mgr.StartReclaim(ctx)

	// 等待一小段时间让循环运行
	time.Sleep(500 * time.Millisecond)

	// 再次启动应该是幂等的（不会创建第二个循环）
	mgr.StartReclaim(ctx)

	// 停止回收循环
	mgr.reclaimLoopStop()
}

// TestReclaimConfigFromManager 测试从 Manager 生成 reclaim 配置
func TestReclaimConfigFromManager(t *testing.T) {
	mgr := &Manager{
		cfg: Config{
			ReclaimIdleSeconds: 1800,
		},
	}

	cfg := mgr.reclaimConfigFromManager()
	if cfg.idleAfter != 1800*time.Second {
		t.Errorf("idleAfter = %v, want %v", cfg.idleAfter, 1800*time.Second)
	}
	if cfg.scanInterval != 30*time.Second {
		t.Errorf("scanInterval = %v, want %v", cfg.scanInterval, 30*time.Second)
	}
}

// TestNodeStateIsUsable 测试节点可用性判断边缘情况
func TestNodeStateIsUsable(t *testing.T) {
	now := time.Now()

	tests := []struct {
		name  string
		state *NodeState
		want  bool
	}{
		{
			name:  "nil state",
			state: nil,
			want:  true,
		},
		{
			name: "disabled but cooldown expired",
			state: &NodeState{
				Disabled:      true,
				DisabledUntil: now.Add(-1 * time.Hour).Unix(),
			},
			want: true,
		},
		{
			name: "disabled and cooldown not expired",
			state: &NodeState{
				Disabled:      true,
				DisabledUntil: now.Add(1 * time.Hour).Unix(),
			},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.state.IsUsable(now)
			if got != tt.want {
				t.Errorf("IsUsable() = %v, want %v", got, tt.want)
			}
		})
	}
}
