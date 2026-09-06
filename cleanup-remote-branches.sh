#!/bin/bash

# 远程分支清理脚本
# 用途: 清理远程仓库中已合并的分支

set -e

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m'

echo -e "${BLUE}=== 远程分支清理工具 ===${NC}\n"

# 确保在 Git 仓库中
if [ ! -d ".git" ]; then
    echo -e "${RED}错误: 当前目录不是 Git 仓库${NC}"
    exit 1
fi

echo -e "${BLUE}更新远程分支信息...${NC}"
git fetch origin --prune

echo -e "${BLUE}分析远程分支...${NC}\n"

# 获取已合并到 origin/main 的远程分支
merged_remote_branches=$(git branch -r --merged origin/main | \
    grep "origin/" | \
    grep -v "origin/main" | \
    grep -v "origin/HEAD" | \
    sed 's/origin\///' || true)

if [ -z "$merged_remote_branches" ]; then
    echo -e "${GREEN}没有需要清理的远程分支！${NC}"
    exit 0
fi

branch_count=$(echo "$merged_remote_branches" | wc -l | tr -d ' ')
echo -e "${YELLOW}发现 $branch_count 个已合并到 origin/main 的远程分支${NC}\n"

echo -e "${BLUE}已合并的远程分支:${NC}"
echo "$merged_remote_branches" | nl

echo -e "\n${RED}警告: 删除远程分支会影响所有团队成员！${NC}"
echo -e "${YELLOW}建议与团队协调后再执行此操作${NC}\n"

echo "请选择操作:"
echo "1) 删除所有已合并的远程分支 (危险操作)"
echo "2) 生成删除命令到文件，手动执行"
echo "3) 仅显示列表"
echo "4) 取消"
echo

read -p "请输入选项 (1-4): " choice

case $choice in
    1)
        echo -e "\n${RED}确认删除远程分支？这个操作无法撤销！${NC}"
        read -p "输入 'DELETE' 确认: " confirm
        
        if [ "$confirm" = "DELETE" ]; then
            echo -e "\n${BLUE}开始删除远程分支...${NC}\n"
            deleted=0
            failed=0
            
            while IFS= read -r branch; do
                branch=$(echo "$branch" | xargs)
                if [ -n "$branch" ]; then
                    echo "正在删除: $branch"
                    if git push origin --delete "$branch" 2>/dev/null; then
                        echo -e "${GREEN}✓${NC} 已删除: $branch\n"
                        ((deleted++))
                    else
                        echo -e "${RED}✗${NC} 删除失败: $branch\n"
                        ((failed++))
                    fi
                fi
            done <<< "$merged_remote_branches"
            
            echo -e "\n${GREEN}清理完成！${NC}"
            echo -e "成功删除: ${GREEN}$deleted${NC} 个远程分支"
            if [ $failed -gt 0 ]; then
                echo -e "删除失败: ${RED}$failed${NC} 个分支"
            fi
        else
            echo -e "${YELLOW}已取消${NC}"
        fi
        ;;
        
    2)
        output_file="delete-remote-branches.sh"
        echo "#!/bin/bash" > "$output_file"
        echo "# 远程分支删除命令" >> "$output_file"
        echo "# 生成时间: $(date)" >> "$output_file"
        echo "" >> "$output_file"
        
        while IFS= read -r branch; do
            branch=$(echo "$branch" | xargs)
            if [ -n "$branch" ]; then
                echo "git push origin --delete '$branch'" >> "$output_file"
            fi
        done <<< "$merged_remote_branches"
        
        chmod +x "$output_file"
        echo -e "${GREEN}删除命令已保存到: $output_file${NC}"
        echo -e "${YELLOW}请检查该文件，然后手动执行${NC}"
        ;;
        
    3)
        echo -e "\n${BLUE}仅显示列表${NC}"
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

echo -e "\n${GREEN}脚本执行完毕！${NC}"
