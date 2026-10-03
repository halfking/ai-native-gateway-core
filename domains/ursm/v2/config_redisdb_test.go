package v2

import "testing"

// URSM_V2_REDIS_DB 的契约（2026-10-03）:
//
//	留空  → RedisDB = -1 → main.go 沿用网关共享 client，行为与配置前**完全一致**
//	0..15 → 生效；与网关 db 相同时 main.go 仍复用共享 client（不无谓多建连接）
//	其它  → loadErr，配置被标记为错误（不静默接受）
//
// 「默认不变」是这个开关能被安全引入的前提：未设置 env 的部署
// 行为与加这个字段之前逐字节一致。
func TestURSMRedisDBAbsentMeansShareGatewayClient(t *testing.T) {
	t.Setenv("URSM_V2_REDIS_DB", "")
	c := LoadFromEnv()
	if c.RedisDB != -1 {
		t.Fatalf("URSM_V2_REDIS_DB unset must yield -1 (share gateway client), got %d", c.RedisDB)
	}
}

func TestURSMRedisDBExplicitValue(t *testing.T) {
	for _, want := range []string{"0", "2", "15"} {
		t.Run("db="+want, func(t *testing.T) {
			t.Setenv("URSM_V2_REDIS_DB", want)
			c := LoadFromEnv()
			if c.RedisDB < 0 {
				t.Fatalf("RedisDB=%d, want the explicit value", c.RedisDB)
			}
			if c.loadErr != nil {
				t.Fatalf("valid value %q rejected: %v", want, c.loadErr)
			}
		})
	}
}

func TestURSMRedisDBRejectsOutOfRange(t *testing.T) {
	for _, bad := range []string{"16", "-1", "abc", "2.5"} {
		t.Run("bad="+bad, func(t *testing.T) {
			t.Setenv("URSM_V2_REDIS_DB", bad)
			c := LoadFromEnv()
			if c.loadErr == nil {
				t.Fatalf("invalid value %q accepted silently (RedisDB=%d)", bad, c.RedisDB)
			}
		})
	}
}

// RedisKeyPrefix 与 RedisDB 是同一组「让 URSM 自成一体」的旋钮，
// 这条钉住二者的默认前缀没被本次改动带偏。
func TestURSMKeyPrefixDefaultUnchanged(t *testing.T) {
	t.Setenv("URSM_V2_REDIS_KEY_PREFIX", "")
	t.Setenv("URSM_V2_REDIS_DB", "")
	c := LoadFromEnv()
	if c.RedisKeyPrefix != "ursm:v2:" {
		t.Fatalf("RedisKeyPrefix=%q, want \"ursm:v2:\"", c.RedisKeyPrefix)
	}
}
