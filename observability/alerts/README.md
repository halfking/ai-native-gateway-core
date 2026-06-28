# Availability & Suspicious-Exit Alert Rules

This directory holds Prometheus alert rules for the llm-gateway-go
`llmgw_availability_*` and `llmgw_suspicious_*` metric families.

## Layout

- `availability.rules.yml` — two alert groups covering the
  Redis-availability-cache and the routing-layer suspicious-exit
  hot path.

## Loading

For a stock `prometheus` server, drop the YAML into the
`rule_files` directory and reload:

```yaml
# /etc/prometheus/prometheus.yml
rule_files:
  - /etc/prometheus/rules/availability.rules.yml
```

For `prometheus-operator` (kube-prometheus-stack), mount the file
into the rule ConfigMap and reference it via a `PrometheusRule`
custom resource:

```yaml
apiVersion: monitoring.coreos.com/v1
kind: PrometheusRule
metadata:
  name: llmgw-availability
spec:
  groups:
    # mirror of availability.rules.yml — keep them in sync.
```

`promtool check rules availability.rules.yml` should pass before
deploying; the YAML is structured so every rule carries `alert`,
`expr`, `for`, `labels.severity`, and `annotations.summary`.

## Severity ladder

- `info`     — informational; expected to fire occasionally during
              blue/green deploys or maintenance windows.
- `warning`  — page the on-call after `for:` elapses; suspect a real
              regression in either the probe workers, the Redis
              layer, or the routing hot path.

The rules above use only `warning` and `info`. Any alert that
should page the on-call is `warning`.

## Pairing with runbooks

Each `warning` rule carries an `annotations.runbook:` field with a
short triage checklist. The runbook text is deliberately terse — the
goal is to get the responder to the right metric / log / SQL query
within ~2 minutes, not to substitute for an actual incident review.