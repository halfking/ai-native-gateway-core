# 2026-07-20: Rebuild + Deploy 1212 — fix frontend regression

## What
- Rebuilt from HEAD cdb185f25 (seq 1212) including `b3c209713` (routingDefault i18n complete) and `cdb185f25` (nav align)
- Deployed to 245 via deploy-seamless.sh

## Why
- Previous build 1210 (cd9ee3bd) stashed concurrent agent's routingDefault.ts changes, missing 58 locale keys
- SmartRoutingConfigPanel and routing-v2 page rendered incorrectly without those keys
- Also missing nav alignment from cdb185f25

## Root Cause
Build 1210 stashed work-in-progress routingDefault.ts changes from concurrent agent (commit b3c209713). The build used committed versions which lacked the complete i18n translations needed by SmartRoutingConfigPanel.

## Verification
- /healthz returns `2.4.7-cdb185f2-20260720-1212`
- /api/system/version reports `build_seq=1212`, `git_sha=cdb185f2`
- browser-use confirmed: 4 dashboard tabs (看板/实时请求流/会话与统计/系统监测) all visible
- browser-use confirmed: routing-v2 SmartRoutingConfigPanel renders correctly (task type buttons, model entries, form fields all visible)
