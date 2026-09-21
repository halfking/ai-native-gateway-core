# PG17 (252) WAL backup design

> **Status**: draft (2026-07-20)
> **Author**: gateway SRE
> **Trigger**: P2-#3 in 154 stability report — "当前 DB 0 backup 策略是高风险. 一次 disk corruption 154 完全恢复不了 request 历史 (2466 MB)"
> **Goal**: define a low-ops, point-in-time-recoverable backup for the shared 252 PG17 cluster

## 1. Current state

| metric | value |
|---|---|
| Database size | 14 GB (heap data 172 MB, total with indexes 2.8 GB) |
| Tables | ~30 hot tables; `request_logs_hot` (2.5 GB) is the biggest |
| Write rate | 14 M commits, 2.3 M rollbacks (lifetime); daily Δ +5-10 GB WAL |
| Backup today | **none** |
| Recovery point | none — only live data |

What that means operationally:

- A disk corruption on `/data` would lose every `request_logs_hot` row,
  every `candidate_failure_logs` row, every audit row since 2026-06.
- A bad migration or `DELETE` run could similarly wipe data with no recovery path.
- Compliance / forensic work (which row ID X wrote Y) currently has no
  point-in-time anchor.

## 2. Goals / non-goals

### 2.1 Goals
- **Continuous WAL archiving** to off-host storage with < 5 min lag.
- **Point-in-time recovery** for any time in the last 14 days.
- **Cheap enough** to run alongside the existing 14 GB DB without doubling
  the storage cost.
- **Restorable in < 30 min** from a fresh container.
- **Compatible** with the shared multi-process deployment — the gateway
  and `cmd/gateway/migrate` processes both need to be quiesced during
  full restores but not for incremental WAL replay.

### 2.2 Non-goals
- Cross-region replication. Single-region backup is enough for now;
  one extra hop in front of S3 (Aliyun OSS) is fine.
- Logical-only backups. We want physical + WAL — `pg_dump` is too slow
  and too large for a 14 GB daily-DB.
- Replacing existing logical exports. Anything that already runs cron
  can keep running.

## 3. Tool choice: Litestream

`litestream` is a Go-based WAL archiver. Properties that fit us:

- **Streaming replication** to S3-compatible object storage, local disk,
  or SFTP. We have no S3 credentials, so we start with **local disk via
  docker volume mount**, and add a SFTP / S3 sync later if needed.
- **Docker-friendly**: `litestream` ships a 9 MB static binary; we add
  it as a sidecar container to the `pg-252-pg17` podman pod.
- **No PG-side config**: reads `pg_wal` via the same `postgres` superuser
  the rest of the system already uses. No `wal_level=replica` change
  needed (default is `replica` for PG17, fine).
- **Hot restore**: re-apply WAL on top of a base backup and produce a
  working PG cluster in seconds.
- **Cheap**: 14 GB heap + ~1 GB WAL/day. Even daily full backups are
  under 5 GB compressed.

Alternatives we considered and rejected:

| tool | reason rejected |
|---|---|
| `pgbackrest` | C; needs PG install + PITR-aware setup; more ops overhead than Litestream for our scale. Worth revisiting if we ever do cross-region async replicas. |
| `barman` | Same — adds another daemon, more cognitive load. |
| `pg_dump` cron | Logical only; 14 GB → 30 min dump window every 6h, too coarse. |
| Filesystem snapshots on `/data` | The container sees the volume via overlay — snapshotting from host doesn't capture the inner FS atomically with WAL. |

## 4. Target architecture

```
podman pod (already exists: pg-252-pg17)
├─ container: pg-252-pg17       (image: kx-citus-pg17:amd64, port 5432)
│     volumes:
│       - /data/pg-data-252-pg17 → host:/data/pg-data-252-pg17
│       - /var/run/litestream     → host:/data/pg-data-252-pg17/.litestream    (NEW)
└─ container: litestream         (image: litestream/litestream:0.3, sidecar) (NEW)
      command: replicate -config /etc/litestream.yml
      env:
        - LITESTREAM_REPLICA_URL=file:///var/run/litestream/db
      mounts:
        - /var/run/litestream
        - /etc/litestream.yml:ro
```

The shared volume `/var/run/litestream` carries `meta.json` + numbered
WAL segments + snapshots, written by the sidecar, read by off-host
shipping.

For off-host delivery we have two candidates:

| target | status | notes |
|---|---|---|
| `file://` (host `/data/backups/llm-pg17/`) | **P2-#3 v1** | host-local; survives container crash but NOT host disk failure. Cheap, immediate. |
| SFTP to 154 (`kxpms.cn` home server) | **P2-#3 v2** | survives host disk failure; small additional ops. Deploys once SRE provides `kxpms-backups@154` SSH key. |
| S3 / Aliyun OSS | not yet — needs `LITESTREAM_REPLICA_URL=s3://…` + bucket provisioning. | Defer until P3. |

We ship **v1 (file://) by default** because the next failure we're
protecting against is a single corrupted WAL / DELETE, not a host-level
disaster. v2 is a follow-up.

## 5. litestream.yml (v1)

```yaml
# /data/pg-data-252-pg17/litestream.yml
dbs:
  - name: llm_gateway
    url: postgres://llm_gateway:***REDACTED***@127.0.0.1:5432/llm_gateway?sslmode=disable
    #   user created during install (NOT the gateway app user) — needs
    #   REPLICATION privilege; PG17 default `postgres` superuser works.
    replica:
      # v1: host-local file
      url: file:///data/backups/llm-pg17
      # v2 (future): sftp://kxpms-backups@backup.kxpms.cn/llm-pg17
      retention: 336h   # 14 days, matches goal §2.1
      snapshot:
        interval: 6h
        threshold: 2 GB of accumulated WAL
    # Validate against the 14 GB DB — full snapshot every 6h + WAL
    # archiving; restores should fit in < 30 min.
```

## 6. Podman sidecar manifest

The existing `pg-252-pg17` is run via plain `docker run` (per the
inventory in the stability report). We'll convert it to a
`podman play` Kubernetes-style YAML **only if needed** for the sidecar
injection; otherwise we just `docker run` a second container in
the same network.

```yaml
# litestream-sidecar.yml  (plain docker run equivalent, not a pod)
docker run -d --name litestream-pg17 \
  --network container:pg-252-pg17 \
  -v /data/pg-data-252-pg17/.litestream:/var/run/litestream \
  -v /etc/litestream-pg17.yml:/etc/litestream.yml:ro \
  litestream/litestream:0.3 \
  replicate -config /etc/litestream.yml
```

`--network container:pg-252-pg17` shares the existing pod's network
namespace, so `127.0.0.1:5432` inside the sidecar is the same
`5432/tcp` that the gateway talks to. We don't expose any new ports.

## 7. Verification

### 7.1 Day-0 smoke
1. Start the sidecar.
2. `docker logs litestream-pg17 | head` — see "replicating …".
3. `ls -la /data/pg-data-252-pg17/.litestream/db/` — meta + segments.
4. `curl -sL http://127.0.0.1:20202/metrics` (litestream metrics port
   exposed in 127.0.0.1 only) — should show `litestream_replica_received_bytes_total > 0`.

### 7.2 Daily
- `/data/pg-data-252-pg17/.litestream/db/snapshots/` should grow at
  most 2-3 GB/day.
- A cron check (`/etc/cron.d/litestream-pg17-health`) verifies
  the latest snapshot is < 7 h old and the latest WAL segment is
  < 30 min old.

### 7.3 Restore drill (quarterly, scheduled in `runbook-2026-07-20.md`)
1. Stop gateway to quiesce writes.
2. Stop pg-252-pg17 container.
3. Move `/data/pg-data-252-pg17/` aside (`mv … …bak-2026-07-20`).
4. Use `litestream restore -config /etc/litestream.yml -timestamp
   2026-07-15T00:00:00Z` to populate a new data directory.
5. Start `pg-252-pg17` (same env / same image). Verify `SELECT
   count(*) FROM request_logs_hot` matches the expected range for the
   chosen timestamp.
6. Start gateway, verify healthz.
7. Document the timestamp + result in `docs/ops/restore-drills.md`.

## 8. Failure modes & mitigations

| failure | impact | mitigation |
|---|---|---|
| sidecar container dies | no new snapshots / WAL until restart | systemd `Restart=always` + `RestartSec=5`; alertmanager alert on `litestream_replica_last_synced_at > 1h ago`. |
| `/data` filesystem full | writes fail | `prune-releases.sh` already runs weekly; add WAL archive prune to > 14 d old. |
| host disk dies | backups gone | v2 SFTP to 154 covers this. |
| restore produces a corrupted cluster | `pg_dumpall --schema-only` from a fresh cluster + replay WAL | n/a — litestream validates checksum per segment. |
| gateway writes during restore | split brain | gateway must be stopped before restore (§7.3). Document in runbook. |

## 9. Open questions

1. **Who has the `kxpms-backups@154` SSH key?** If SRE can't issue
   in 1 day, stay on v1 (file://) for a quarter, revisit.
2. **What about WAL archive compression?** Litestream supports
   `compression: gzip` — at ~1 GB/day WAL, 5-7× compression = saves
   ~5 GB. Worth enabling from day 1.
3. **Should we add PITR to other env DBs** (e.g. the redis on 252)?
   Litestream is PG-specific. Redis has its own AOF dump; separate ticket.

## 10. Cost & ops

- Storage: ~1 GB WAL/day + ~2 GB snapshot/day = ~3 GB/day compressed
  = ~45 GB / 14 days. Within 47 % free disk of `/data` (197 GB).
- CPU: litestream is single-thread Go; on a 2-vCPU host it
  consumes < 1 % steady, bursts to 10 % during snapshot.
- Memory: < 50 MB RSS.
- Time to recover: 1-2 min for recent PITR (WAL replay), 5-15 min for
  a 14-day-old restore (full snapshot + WAL).

## 11. Reference

- Litestream docs: https://litestream.io/
- PG17 WAL archive: https://www.postgresql.org/docs/17/wal-internals.html
- 154 stability report §11.2 (item 4 — DB WAL backup)
