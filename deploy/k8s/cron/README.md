# pricing-monthly-refresh CronJob
> Monthly pricing refresh. Pulls latest tier_1/2 vendor pricing, computes diff, applies via Go admin API, notifies via Feishu webhook.

## Schedule
- `0 3 1 * *` (1st of every month at 03:00 UTC)

## Required Kubernetes resources (one-time setup)
1. `Secret: llm-gateway-pg-pass` (key: `pg-password` = stockuser password from Casdoor secret)
2. `Secret: llm-gateway-secret` (key: `admin-api-key` = admin JWT for `/api/pricing/import`)
3. `Secret: pricing-refresh-secret` (key: `feishu-webhook` = optional Feishu incoming webhook URL)
4. `ConfigMap: pricing-refresh-src` (key: `refresh-bundle.tar.gz` = git-archived scripts)
5. `PVC: pricing-refresh-work` (10Gi for log retention)
6. SQL migration: `pricing_refresh_log` audit table

## Refresh pipeline
1. `git clone` the llm-gateway-go repo to get `fetch-pricing.sh` + `diff-pricing.py`
2. `curl /api/pricing/summary` → `summary_before.json`
3. `bash fetch-pricing.sh` → `raw/<vendor>.md` (uses agent-reach to bypass fetch issues)
4. `python3 diff-pricing.py` → `tier-pricing.csv` + `token-plan-pricing.sql`
5. If CSV non-empty: `POST /api/pricing/import` (multipart) + apply SQL via `psql` (within pod)
6. `curl /api/pricing/summary` → `summary_after.json`
7. INSERT `pricing_refresh_log` row (status, diff_count, before/after)
8. If diff_count > 5: POST Feishu webhook (compact summary)
9. Artifacts preserved in PVC for 90 days

## Diff threshold
- `<= 5` changed offers → silent (regular monthly)
- `> 5` changed offers → Feishu notification
- `> 20` → also @-mention on-call

## Rollout
- `kubectl apply -f deploy/k8s/cron/pricing-monthly-refresh.yaml`
- Manual trigger: `kubectl create job --from=cronjob/pricing-monthly-refresh manual-$(date +%s) -n pms-test`
- History: `kubectl -n pms-test get jobs -l app=pricing-monthly-refresh`
