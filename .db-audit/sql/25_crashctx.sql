export PGPAGER=cat
export TERM=dumb
LOG=/var/lib/postgresql/data/log/postgresql-2026-10-02_000000.log
echo "=== 551230-551310（恢复爆发前的上下文）==="
sed -n '551230,551310p' "$LOG"