#!/usr/bin/env bash
# Static and (when installed) promtool contract checks for proxy monitoring assets.
set -euo pipefail
ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)
RULES="$ROOT/deploy/prometheus/rules/proxy-rules.yml"
DASH="$ROOT/deploy/grafana"

python3 - "$ROOT" "$RULES" "$DASH" <<'PY'
import json, pathlib, re, sys
try:
    import yaml
except ImportError:
    raise SystemExit("PyYAML is required for proxy rule contract tests")
root, rules_file, dash_dir = map(pathlib.Path, sys.argv[1:])
rules = yaml.safe_load(rules_file.read_text())
assert rules and rules["groups"], "rules must contain groups"
assert all("receiver" not in r for g in rules["groups"] for r in g.get("rules", [])), "rules must not invent receivers"
text = rules_file.read_text()
assert "wiki.example.com" not in text
assert "nodes_total > 0 and llm_gateway_proxy_nodes_dialable == 0" in text
assert "clamp_min(llm_gateway_proxy_nodes_total, 1)" in text
assert 'status="timeout"' not in text
assert 'status="failure"' not in text
for p in rules["groups"]:
    for r in p.get("rules", []):
        url = r.get("annotations", {}).get("runbook_url", "")
        assert url and not re.match(r"https?://", url), f"runbook must be repository-relative: {url}"

allowed = {
    "llm_gateway_proxy_subscriptions_total": {"state"},
    "llm_gateway_proxy_subscription_refresh_total": {"status"},
    "llm_gateway_proxy_node_health_check_total": {"status"},
    "llm_gateway_proxy_node_selection_total": {"result"},
}
for f in sorted(dash_dir.glob("proxy-*-dashboard.json")):
    d = json.loads(f.read_text())
    raw = json.dumps(d, ensure_ascii=False)
    for forbidden in ("node_id", "subscription_id", "location", "error_type", "wiki.example.com"):
        assert forbidden not in raw, f"{f}: forbidden label/text {forbidden}"
    exprs = []
    def walk(x):
        if isinstance(x, dict):
            if isinstance(x.get("expr"), str): exprs.append(x["expr"])
            for v in x.values(): walk(v)
        elif isinstance(x, list):
            for v in x: walk(v)
    walk(d)
    for e in exprs:
        assert 'status="failure"' not in e and 'status="timeout"' not in e
        assert 'result="no_healthy"' not in e
        if "histogram_quantile" in e:
            assert "sum by (le) (rate(" in e, f"{f}: histogram must aggregate by le"
        if re.search(r"/\s*rate\([^)]*_count\[", e):
            raise AssertionError(f"{f}: unguarded histogram denominator: {e}")
        if "llm_gateway_proxy_subscription_refresh_total" in e:
            assert "subscription_id" not in e
    if f.name == "proxy-subscription-dashboard.json":
        assert not d.get("templating", {}).get("list"), "subscription dashboard must not filter by subscription ID"
print("proxy monitoring contract checks passed")
PY

if command -v promtool >/dev/null 2>&1; then
  promtool check rules "$RULES"
else
  echo "promtool not installed; YAML and dashboard contract checks passed"
fi
