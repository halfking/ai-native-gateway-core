#!/bin/sh
# 2026-08-18 render alertmanager config for native deploy.
#
# 2026-10-15 observability audit: extended from one receiver to both.
#   credential-team  <- CREDENTIAL_TEAM_WEBHOOK_URL
#   default          <- DEFAULT_TEAM_WEBHOOK_URL
#
# The `default` receiver previously had its webhook_configs fully commented
# out, so Alertmanager MATCHED the root route and then dropped every
# non-credential alert with no integration and no error — 55 alert rules
# dead-ended there while `amtool config show` still looked healthy.
#
# Each placeholder token uses an RFC-2606 reserved host, so an unconfigured
# deploy fails loudly ("webhook delivery failed") instead of silently. That
# is the same contract the credential-team receiver already relied on.
set -eu

# DIR is overridable so the rendering can be exercised outside the deploy
# root (the /opt path needs root, which is exactly where you do NOT want to
# be experimenting with a sed that rewrites config files).
DIR="${ALERTMANAGER_DIR:-/opt/monitoring/alertmanager}"
SRC="$DIR/alertmanager.yml"
DST="$DIR/alertmanager.rendered.yml"
ENV="$DIR/webhook.env"

cp "$SRC" "$DST"

render_one() {
  key="$1"
  url=""
  if [ -f "$ENV" ]; then
    # POSIX sh has no indirect expansion (${!key} is a bashism), so the
    # variable named by $key has to be reached through eval. Getting this
    # wrong substitutes the variable's NAME into the URL, which still
    # renders a syntactically valid file — amtool only catches it because
    # the placeholder host is reserved and fails the config check.
    url=$(. "$ENV"; eval "echo \"\${$key:-}\"")
  fi
  if [ -n "$url" ]; then
    # `sed -i` is not portable: BSD sed (macOS) consumes the next argument as
    # the backup suffix, so `sed -i 's|...|...|'` silently becomes
    # "write to a file named s|...|...|". Render via a temp file instead.
    sed "s|https://placeholder.invalid/##${key}##|$url|g" "$DST" > "$DST.tmp"
    mv "$DST.tmp" "$DST"
    echo "[render] substituted $key (len=${#url})"
  else
    echo "[render] $key empty; leaving placeholder (delivery will fail loudly)"
  fi
}

render_one CREDENTIAL_TEAM_WEBHOOK_URL
render_one DEFAULT_TEAM_WEBHOOK_URL
