//go:build integration

package bg

import (
	"context"
	"testing"
	"time"
)

func TestSessionTurnDetailsExpiredHotClassification(t *testing.T) {
	pool := startProbeSemanticsPG(t) // disposable postgres:16-alpine container
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, err := pool.Exec(ctx, `
		CREATE TABLE public.session_turn_details (
			id bigint NOT NULL, tenant_id text NOT NULL, request_id text NOT NULL,
			session_id text NOT NULL, turn_no integer NOT NULL,
			partition_date date NOT NULL,
			PRIMARY KEY (partition_date, id),
			UNIQUE (tenant_id, request_id, partition_date),
			UNIQUE (session_id, turn_no, partition_date)
		) PARTITION BY RANGE (partition_date);
		CREATE TABLE public.session_turn_details_default
			PARTITION OF public.session_turn_details DEFAULT;
		CREATE TABLE public.session_turn_details_hot (
			id bigint PRIMARY KEY, tenant_id text NOT NULL, request_id text NOT NULL,
			session_id text NOT NULL, turn_no integer NOT NULL,
			partition_date date NOT NULL, ts timestamptz NOT NULL
		);
		INSERT INTO public.session_turn_details
		(id,tenant_id,request_id,session_id,turn_no,partition_date) VALUES
		(101,'tenant','same-request','s1',1,current_date),
		(102,'tenant','other-request','s2',2,current_date),
		(3,'tenant','id-owner','s3',3,current_date);
		INSERT INTO public.session_turn_details_hot
		(id,tenant_id,request_id,session_id,turn_no,partition_date,ts) VALUES
		(11,'tenant','same-request','s1',1,current_date,now()-interval '1 day'),
		(12,'tenant','session-conflict','s2',2,current_date,now()-interval '1 day'),
		(3,'tenant','id-conflict','s4',4,current_date,now()-interval '1 day'),
		(14,'tenant','ready','s5',5,current_date,now()-interval '1 day'),
		(15,'tenant','fresh','s6',6,current_date,now());
	`)
	if err != nil {
		t.Fatalf("fixture: %v", err)
	}
	stats, err := QuerySessionTurnDetailsExpiredHotRows(ctx, pool, 8*time.Hour)
	if err != nil {
		t.Fatalf("classify: %v", err)
	}
	if stats.Total != 4 || stats.RequestDuplicate != 1 ||
		stats.SessionTurnConflict != 1 || stats.IDDateConflict != 1 ||
		stats.ReadyUnmoved != 1 {
		t.Fatalf("unexpected exclusive classification: %+v", stats)
	}
	if stats.Total != stats.RequestDuplicate+stats.SessionTurnConflict+stats.IDDateConflict+stats.ReadyUnmoved {
		t.Fatalf("categories do not cover all expired rows: %+v", stats)
	}
	stats, err = QuerySessionTurnDetailsExpiredHotRows(ctx, pool, 48*time.Hour)
	if err != nil || stats.Total != 0 {
		t.Fatalf("retention cutoff ignored: stats=%+v err=%v", stats, err)
	}
}
