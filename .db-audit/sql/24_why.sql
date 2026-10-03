export PGPAGER=cat
export TERM=dumb
LOG=$(ls -t /var/lib/postgresql/data/log/*.log | head -1)
echo "log file: $LOG"
echo ""
echo "=== 崩溃前后关键行（PANIC/FATAL/ERROR/terminating/segmentation）==="
grep -nE 'PANIC|segmentation|terminat|was terminated|shm_mq|out of memory|invalid record|redo done|starting PostgreSQL|recovery' "$LOG" | tail -40
echo ""
echo "=== 崩溃前 25 行 ==="
grep -n 'starting PostgreSQL' "$LOG" | tail -1
LINE=$(grep -n 'starting PostgreSQL' "$LOG" | tail -1 | cut -d: -f1)
sed -n "$((LINE>25 ? LINE-25 : 1)),$((LINE+15))p" "$LOG"