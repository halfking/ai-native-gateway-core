#!/bin/sh
D=/tmp/d252t
podman exec -i -e PGPAGER=cat pg-252-pg17 \
  psql -U postgres -d llm_gateway -X -q -A -t -f - < $D/wrapped.sql > $D/out.txt 2> $D/err.txt
echo $? > $D/rc
touch $D/done