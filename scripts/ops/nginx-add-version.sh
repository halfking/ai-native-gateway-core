#!/usr/bin/env bash
set -euo pipefail
CONF=/etc/nginx/conf.d/llm-kxpms-cn.conf
BAK="${CONF}.bak-$(date +%Y%m%d_%H%M%S)"
cp "$CONF" "$BAK"
if ! grep -q 'location = /version.json' "$CONF"; then
  awk '
    /location = \/api\/admin\/live-stream \{/ && !done {
      print "    location = /version.json {"
      print "        root /opt/llm-gateway-go/web;"
      print "        try_files $uri =404;"
      print "        expires 1h;"
      print "        add_header Cache-Control \"public\";"
      print "    }"
      print ""
      done=1
    }
    { print }
  ' "$CONF" > "$CONF.new" && mv "$CONF.new" "$CONF"
fi
nginx -t
systemctl reload nginx
echo "---"
curl -sS https://llm.kxpms.cn/version.json