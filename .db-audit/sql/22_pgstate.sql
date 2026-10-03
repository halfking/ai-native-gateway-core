export PGPAGER=cat
export TERM=dumb
ps -eo pid,etime,rss,args 2>/dev/null | grep -E 'bin/postgres|postgres:' | grep -v grep | head -5; echo '---'; df -h /var/lib/postgresql/data | tail -1