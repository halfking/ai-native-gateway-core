export PGPAGER=cat
export TERM=dumb
grep -rnE "^\s*shared_buffers" /var/lib/postgresql/data/postgresql.conf /var/lib/postgresql/data/conf.d/*.conf /docker-entrypoint-initdb.d/*.conf 2>/dev/null | head