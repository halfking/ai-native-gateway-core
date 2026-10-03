export PGPAGER=cat
export TERM=dumb
echo "=== log dir ==="
ls -lt /var/lib/postgresql/data/log/ | head -6
echo ""
NEW=$(ls -t /var/lib/postgresql/data/log/*.log | head -1)
echo "newest log: $NEW"
echo ""
echo "=== 新日志文件开头 40 行（崩溃现场）==="
head -40 "$NEW"
echo ""
echo "=== 旧日志文件最后 30 行（崩溃前）==="
OLD=$(ls -t /var/lib/postgresql/data/log/*.log | sed -n '2p')
echo "prev log: $OLD"
tail -30 "$OLD"