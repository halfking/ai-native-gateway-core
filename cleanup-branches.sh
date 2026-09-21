#!/bin/bash

# Git 分支清理脚本
# 生成时间: 2026-09-04
# 用途: 清理已合并到 main 的本地分支

set -e

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

echo -e "${BLUE}=== Git 分支清理工具 ===${NC}\n"

# 确保在正确的目录
if [ ! -d ".git" ]; then
    echo -e "${RED}错误: 当前目录不是 Git 仓库${NC}"
    exit 1
fi

# 确保在 main 分支
current_branch=$(git branch --show-current)
if [ "$current_branch" != "main" ]; then
    echo -e "${YELLOW}当前分支: $current_branch${NC}"
    echo -e "${YELLOW}建议切换到 main 分支后再执行清理${NC}"
    read -p "是否现在切换到 main 分支? (y/n) " -n 1 -r
    echo
    if [[ $REPLY =~ ^[Yy]$ ]]; then
        git checkout main
        git pull origin main
    else
        echo -e "${RED}已取消${NC}"
        exit 1
    fi
fi

# 更新 main 分支
echo -e "${BLUE}更新 main 分支...${NC}"
git pull origin main

# 获取已合并的分支
merged_branches=$(git branch --merged main | grep -v "^\*" | grep -v "main" | grep -v "^\+" || true)

if [ -z "$merged_branches" ]; then
    echo -e "${GREEN}没有需要清理的分支！${NC}"
    exit 0
fi

# 统计
branch_count=$(echo "$merged_branches" | wc -l | tr -d ' ')
echo -e "${YELLOW}发现 $branch_count 个已合并到 main 的分支${NC}\n"

# 显示将要删除的分支
echo -e "${BLUE}以下分支将被删除:${NC}"
echo "$merged_branches" | nl

echo -e "\n${YELLOW}注意: 这些分支已经合并到 main，删除它们是安全的${NC}"
echo -e "${YELLOW}删除的是本地分支，远程分支不受影响${NC}\n"

# 提供选项
echo "请选择操作:"
echo "1) 删除所有已合并的分支"
echo "2) 逐个确认删除"
echo "3) 仅显示列表，不删除"
echo "4) 取消"
echo

read -p "请输入选项 (1-4): " choice

case $choice in
    1)
        echo -e "\n${BLUE}开始批量删除...${NC}\n"
        deleted=0
        failed=0
        
        while IFS= read -r branch; do
            branch=$(echo "$branch" | sed 's/^[+ ]*//')
            if [ -n "$branch" ]; then
                if git branch -d "$branch" 2>/dev/null; then
                    echo -e "${GREEN}✓${NC} 已删除: $branch"
                    ((deleted++))
                else
                    echo -e "${RED}✗${NC} 删除失败: $branch"
                    ((failed++))
                fi
            fi
        done <<< "$merged_branches"
        
        echo -e "\n${GREEN}清理完成！${NC}"
        echo -e "成功删除: ${GREEN}$deleted${NC} 个分支"
        if [ $failed -gt 0 ]; then
            echo -e "删除失败: ${RED}$failed${NC} 个分支"
        fi
        ;;
        
    2)
        echo -e "\n${BLUE}逐个确认删除模式${NC}\n"
        deleted=0
        skipped=0
        
        while IFS= read -r branch; do
            branch=$(echo "$branch" | sed 's/^[+ ]*//')
            if [ -n "$branch" ]; then
                read -p "删除分支 '$branch'? (y/n/q) " -n 1 -r
                echo
                if [[ $REPLY =~ ^[Yy]$ ]]; then
                    if git branch -d "$branch" 2>/dev/null; then
                        echo -e "${GREEN}✓${NC} 已删除\n"
                        ((deleted++))
                    else
                        echo -e "${RED}✗${NC} 删除失败\n"
                    fi
                elif [[ $REPLY =~ ^[Qq]$ ]]; then
                    echo -e "${YELLOW}已取消剩余操作${NC}"
                    break
                else
                    echo -e "${YELLOW}已跳过${NC}\n"
                    ((skipped++))
                fi
            fi
        done <<< "$merged_branches"
        
        echo -e "\n${GREEN}操作完成！${NC}"
        echo -e "已删除: ${GREEN}$deleted${NC} 个分支"
        echo -e "已跳过: ${YELLOW}$skipped${NC} 个分支"
        ;;
        
    3)
        echo -e "\n${BLUE}仅显示列表，未执行删除操作${NC}"
        ;;
        
    4)
        echo -e "\n${YELLOW}已取消${NC}"
        exit 0
        ;;
        
    *)
        echo -e "\n${RED}无效的选项${NC}"
        exit 1
        ;;
esac

# 显示剩余分支
echo -e "\n${BLUE}当前剩余的本地分支:${NC}"
git branch | wc -l | xargs echo "总数:"
echo -e "\n未合并的分支:"
git branch --no-merged main | grep -v "^\*" || echo "无"

echo -e "\n${GREEN}脚本执行完毕！${NC}"
