package store

import "github.com/redis/go-redis/v9"

// RawClient exposes the underlying redis client. It is used by callers
// (e.g. the config syncer) that need to perform operations the Store
// facade does not yet wrap. Treat it as a last-resort escape hatch.
func (s *Store) RawClient() *redis.Client { return s.rdb }
