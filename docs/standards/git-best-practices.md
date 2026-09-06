# Git 分支管理最佳实践

## 分支命名规范

### 推荐的命名格式

```
<类型>/<简短描述>
```

**类型前缀:**
- `feat/` - 新功能
- `fix/` - Bug 修复
- `refactor/` - 重构
- `docs/` - 文档
- `test/` - 测试
- `chore/` - 杂项任务

**示例:**
- `feat/user-authentication`
- `fix/login-timeout`
- `refactor/api-client`

### 避免的命名模式

❌ **不要带日期后缀** (除非是临时快照分支)
- `fix/redis-audit-20260827` → `fix/redis-audit`

❌ **不要使用过于具体的编号**
- `fix/issue-1234-5678` → `fix/user-login-error`

## 分支生命周期管理

### 1. 创建分支
```bash
# 从最新的 main 创建
git checkout main
git pull origin main
git checkout -b feat/my-feature
```

### 2. 开发过程
```bash
# 定期同步 main 的更新
git fetch origin
git merge origin/main

# 或使用 rebase 保持提交历史整洁
git rebase origin/main
```

### 3. 合并后立即删除
```bash
# 合并到 main 后
git checkout main
git pull origin main

# 删除本地分支
git branch -d feat/my-feature

# 删除远程分支
git push origin --delete feat/my-feature
```

## 自动化清理配置

### Git 配置优化

```bash
# 在 pull 时自动清理已删除的远程分支
git config --global fetch.prune true

# 在 pull 时自动清理已删除的远程标签
git config --global fetch.pruneTags true
```

### Git Aliases 快捷命令

添加到 `~/.gitconfig`:

```ini
[alias]
    # 显示已合并的分支
    merged = branch --merged main
    
    # 显示未合并的分支
    unmerged = branch --no-merged main
    
    # 删除所有已合并的本地分支
    cleanup = "!git branch --merged main | grep -v '\\*\\|main\\|master' | xargs -n 1 git branch -d"
    
    # 显示最近活动的分支
    recent = "!git for-each-ref --sort=-committerdate refs/heads/ --format='%(refname:short)|%(committerdate:relative)|%(authorname)' | column -t -s '|' | head -20"
    
    # 更新并清理远程分支
    sync = "!git fetch origin --prune && git pull origin main"
```

### 使用别名

```bash
# 查看最近活动的分支
git recent

# 查看已合并的分支
git merged

# 清理已合并的本地分支
git cleanup

# 同步并清理
git sync
```

## Pre-push Hook (自动提醒)

创建 `.git/hooks/pre-push`:

```bash
#!/bin/bash

# 统计已合并的分支数量
merged_count=$(git branch --merged main | grep -v "^\*" | grep -v "main" | wc -l | tr -d ' ')

if [ "$merged_count" -gt 10 ]; then
    echo "⚠️  警告: 你有 $merged_count 个已合并的本地分支"
    echo "建议运行: git cleanup"
    echo ""
    read -p "是否继续 push? (y/n) " -n 1 -r
    echo
    if [[ ! $REPLY =~ ^[Yy]$ ]]; then
        exit 1
    fi
fi
```

## 团队协作规范

### 1. 分支策略

- **main**: 生产就绪代码，受保护
- **feature branches**: 短期开发分支，合并后立即删除
- **release branches**: 仅在需要长期维护多版本时使用

### 2. Pull Request 规范

- PR 合并后，**立即删除源分支**（GitHub/GitLab 可自动配置）
- PR 标题清晰说明变更内容
- 添加必要的标签和里程碑

### 3. 定期清理计划

**每周一次:**
- 清理已合并的本地分支

**每月一次:**
- 团队协调清理远程分支
- 检查超过 30 天未活动的分支

### 4. 例外情况

保留长期分支的场景：
- **实验性分支**: 长期探索的功能，使用 `exp/` 前缀
- **版本维护分支**: `release/v1.x`
- **文档分支**: `docs/` (如果与主分支分离维护)

## GitHub/GitLab 配置

### GitHub

在仓库设置中启用：
1. **Automatically delete head branches**: PR 合并后自动删除源分支
2. **Branch protection rules**: 保护 main 分支
3. **Require pull request reviews**: 强制代码审查

### GitLab

在项目设置中：
1. **Remove source branch when merge request is accepted**: 自动删除
2. **Protected branches**: 保护主分支
3. **Merge request approvals**: 配置审批规则

## 监控脚本

创建定期提醒脚本 `~/.local/bin/git-branch-monitor`:

```bash
#!/bin/bash

# 检查所有 Git 仓库的分支状态
find ~/workspace -name ".git" -type d | while read gitdir; do
    repo_dir=$(dirname "$gitdir")
    cd "$repo_dir"
    
    merged_count=$(git branch --merged main 2>/dev/null | grep -v "^\*" | grep -v "main" | wc -l | tr -d ' ')
    
    if [ "$merged_count" -gt 5 ]; then
        echo "📁 $repo_dir"
        echo "   ⚠️  $merged_count 个已合并的分支需要清理"
        echo ""
    fi
done
```

添加到 crontab (每周一早上 9 点提醒):
```bash
0 9 * * 1 /path/to/git-branch-monitor | mail -s "Git 分支清理提醒" your@email.com
```

## 总结

**关键原则:**
1. **合并后立即删除** - 最重要的习惯
2. **命名规范一致** - 方便识别和管理
3. **定期清理** - 预防累积
4. **自动化配置** - 减少手动操作
5. **团队协作** - 统一规范和流程

保持仓库整洁不仅提高效率，还能避免误操作和心智负担。
