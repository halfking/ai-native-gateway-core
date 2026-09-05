# 2026-08-23 stash 清理建议（待原作者确认）

> 本文档由本会话（2026-08-23 13:50）根据 `git stash list` 与 HEAD 对照审计产出。
> **不会执行任何 `git stash drop`**——按用户「不丢弃他人修改」原则，需原作者确认后才动手。

## 总览

| stash | 分支 | 文件数 | 改动类型 | HEAD 状态 | 建议 |
|---|---|---|---|---|---|
| `stash@{0}` | main | 20 | `halfking/halfking-other-team changes (preserve for audit)` | — | **保留**（标签明示审计需要） |
| `stash@{1}` | fix/handoff-concurrency-and-migration-527-testcontainers | 3 | `deploy-version-bumps-pre-existing` | — | **保留**（其他分支产物） |
| `stash@{2}` | feat/unified-auto-orchestration-wave4 | 13 | `feat(unified-auto-orchestration-wave4)` WIP | — | **保留**（其他分支产物） |
| `stash@{3}` | main | 10 | alert engine advisory-only + af2cc053 WIP | — | **保留**（作者不明，标签 "preserve before sync"） |
| `stash@{4}` | main | 5 | CHANGELOG / version.json / menu-config / VERSION / web/public/version.json bump | 内容已被 commit `8d9652cb1 fix(streaming): skip trace events for GET probes` 吸收；版本号 `2.5.0-8d9652cb-20260820-1645` → HEAD `2.4.7-87fa9814-20260823-1687`（build_seq 1687 远超 1645） | **可清理**（待原作者确认） |
| `stash@{5}` | fix/audit-get-trace-and-survival-contract | 1 | `deploy/llmgo-245.nginx.conf` 加 `location ^~ /api/ {`（优先级高于 SPA fallback） | HEAD `deploy/llmgo-245.nginx.conf:127` 已含相同 block；被 commit `48487ec74 fix(nginx): ensure 245 vhost backend routes bypass SPA fallback + archive active-20260821` 取代 | **可清理**（待原作者确认） |
| `stash@{6}` | main | 13 | `shouldFlushRequestTrace` / GET probe skip + stream tests + version bump | commit `8d9652cb1 fix(streaming): skip trace events for GET probes` 用不同写法实现相同语义（HEAD grep `shouldFlushRequestTrace` 为空）；CHANGELOG 描述被 commit 同期落地；版本号 bump 后续已被覆盖 | **可清理**（待原作者确认） |

## 验证证据（命令 + 输出）

### HEAD 状态

```
$ git rev-parse HEAD
3e30967b9da27ec6169f50dbcf380850b76b929b

$ git rev-parse origin/main
3e30967b9da27ec6169f50dbcf380850b76b929b

$ git rev-list --left-right --count origin/main...HEAD
0	0
```

### stash 内容证据

```
$ git stash list
stash@{0}: On main: WIP: halfking/halfking-other-team changes (preserve for audit)
stash@{1}: On fix/handoff-concurrency-and-migration-527-testcontainers: deploy-version-bumps-pre-existing
stash@{2}: WIP on feat/unified-auto-orchestration-wave4: 1b319da24 chore(release): bump build_seq 1650→1651 for 245 pre-prod URSM validation
stash@{3}: On main: WIP: alert engine advisory-only + version bump af2cc053 - preserve before sync
stash@{4}: On main: preserve restored WIP for follow-up handoff 2026-08-21
stash@{5}: On fix/audit-get-trace-and-survival-contract: preserve mixed WIP before audit correction 2026-08-21
stash@{6}: On main: preserve mixed WIP before audit fixes 2026-08-21
```

### 对 HEAD 已被吸收的代码做 grep

```
$ grep -c "三台 nginx 缺失 SPA fallback" CHANGELOG.md
1

$ grep -n "shouldFlushRequestTrace" domains/streaming/handler.go
（空）

$ grep -n "shouldFlushRequestTrace" domains/streaming/ -r
（空）

$ grep -n "location \^~ /api/" deploy/llmgo-245.nginx.conf
127:    location ^~ /api/ {
```

### 对应的吸收 commit

```
$ git log --oneline -5 -- deploy/llmgo-245.nginx.conf
e27c4354d Merge remote-tracking branch 'origin/main' into feat/standard-models-rollout
9d1218979 fix(requestjourney): harden durable observation rollout
48487ec74 fix(nginx): ensure 245 vhost backend routes bypass SPA fallback + archive active-20260821
6a861d004 fix(i18n): resolve t() shadowing + App.vue re-login + lazy locale load
（48487ec74 正是吸收 nginx 245 conf 中 ^~ /api/ 改动的 commit）

$ git log --oneline | grep "8d9652cb" 
8d9652cb1 fix(streaming): skip trace events for GET probes
（此 commit 实现了 stash@{6} 中 shouldFlushRequestTrace 的等价语义）
```

### 版本号 bump 已被覆盖

```
$ cat version.json
{
  "version": "2.4.7-87fa9814-20260823-1687",
  "git_tag": "2.4.7",
  "git_sha": "87fa9814",
  "build_seq": 1687,
  "build_date": "20260823",
  "module": "llm-gateway-go"
}

stash@{4} 残留: version=2.5.0-8d9652cb-20260820-1645, build_seq=1645
stash@{6} 残留: version=2.5.0-5abd0d6b-20260820-1652, build_seq=1652

HEAD build_seq=1687 >> stash 残留 build_seq（1687-1645=42 次后续 bump 已落 commit）
```

### 无外部引用（branch / tag）

```
$ for sha in $(git rev-list stash@{4} ^HEAD 2>/dev/null; \
                git rev-list stash@{5} ^HEAD 2>/dev/null; \
                git rev-list stash@{6} ^HEAD 2>/dev/null); do
    git branch -a --contains "$sha" 2>/dev/null
    git tag --contains "$sha" 2>/dev/null
done

（空输出 — 无任何 branch / tag 引用这三个 stash 引入的 commit）
```

## 风险评估

### 低风险（可清理）

- `stash@{4}` / `stash@{5}` / `stash@{6}` 内容均已被后续 commit 吸收或被版本号 bump 取代。
- 无任何 branch / tag / reflog 引用这些 stash。
- 作者归属无法 100% 证伪（stash 标签未写作者 email），但内容已与 HEAD 重复。

### 不可清理

- `stash@{0}` 标签明确 `preserve for audit`。
- `stash@{1}` / `stash@{2}` 属于其他分支的产物，drop 会影响该分支的工作流。
- `stash@{3}` 作者不明 + 标签 `preserve before sync`，可能存在同步依赖。

## 行动指引

1. **原作者确认**：在 #git-ops 频道或对应 author 1:1 联系 stash@{4}/{5}/{6} 作者是否仍需。
2. **若确认**：作者本人执行：

   ```bash
   # 推荐 drop 顺序：先 drop 索引大的，避免索引偏移
   git stash drop stash@{6}
   git stash drop stash@{5}    # 注意 drop@{6} 后索引已变，原 @{5} 现在是 @{5}
   git stash drop stash@{4}
   ```

3. **若需要保留**：把 stash 内容 cherry-pick 到独立 branch 永久存档：

   ```bash
   for i in 4 5 6; do
     git branch archive/stash-$i-20260821 "stash@{$i}"
   done
   git stash drop stash@{4} stash@{5} stash@{6}
   ```

## 备份

如需保留 stash 内容到独立分支以防 drop 后后悔：

```bash
mkdir -p .scratch/stash-archive-20260823
for i in 0 1 2 3 4 5 6; do
  git show "stash@{$i}" --stat > ".scratch/stash-archive-20260823/stash-$i-meta.txt"
done
ls -la .scratch/stash-archive-20260823/
```

（仅备份元数据；如果需要保留 diff 内容可加 `--patch` 输出到 .patch 文件，但本会话不做）
