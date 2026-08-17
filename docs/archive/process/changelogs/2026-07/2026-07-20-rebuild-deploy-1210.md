# 2026-07-20: Rebuild + Deploy 1210 to 245 (llm.kxpms.cn)

## What
- Rebuilt llm-gateway-go from HEAD cd9ee3bd (seq 1210)
- Deployed to 245 via deploy-seamless.sh (atomic symlink switch, 41s downtime)

## Why
- Previous build 1209 had concurrent-agent fixes merged; fresh build needed
- User requested full rebuild + deploy cycle with verification

## Verification
- /healthz returns `2.4.7-cd9ee3bd-20260720-1210`
- /api/system/version reports `build_seq=1210`, `git_sha=cd9ee3bd`
- browser-use confirmed dashboard shows all 4 tabs: 看板/实时请求流/会话与统计/系统监测
- Admin login (POST /api/auth/token) successful

## Notes
- 154 (47.97.111.154) SSH remains fully blocked (all ports) — cannot deploy there
- llm-gateway-go service for llm.kxpms.cn runs on 245 (172.16.2.241), proxied via 252 nginx
- 154 "deploy" would route to same 245 host; already deployed
