package stats

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"
)

// InboxConsumerSchemaReady verifies that the durable inbox state contract is
// present before a gateway switches its writer to asynchronous projection.
func InboxConsumerSchemaReady(ctx context.Context, pool *pgxpool.Pool) (bool, error) {
	if pool == nil {
		return false, nil
	}
	var ready bool
	err := pool.QueryRow(ctx, `
		SELECT to_regclass('stats_event_inbox') IS NOT NULL
		   AND to_regclass('usage_facts') IS NOT NULL
		   AND (
			 SELECT count(*) = 5
			 FROM information_schema.columns
			 WHERE table_schema = current_schema()
			   AND table_name = 'stats_event_inbox'
			   AND column_name IN (
				 'processing_status', 'next_attempt_at', 'fencing_token',
				 'dead_lettered_at', 'dead_letter_reason'
			   )
		   )`).Scan(&ready)
	return ready, err
}
