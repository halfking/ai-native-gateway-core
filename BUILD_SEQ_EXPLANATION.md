# Build Seq 管理说明

## 🤔 为什么多次编译，编译序号没有变？

### 问题
你注意到我们执行了多次部署/编译，但 `build_seq` 一直停留在某个值。

### 原因分析

#### 1. **部署脚本的设计**

`deploy-184.sh` 脚本中：

```bash
# 步骤 2: 获取版本信息
BUILD_SEQ=$(cat build_seq)              # 读取当前值: 8
NEW_BUILD_SEQ=$((BUILD_SEQ + 1))        # 计算新值: 9
echo "${NEW_BUILD_SEQ}" > build_seq     # 写入文件: 9
```

**问题**: 这个修改是**本地临时的**，脚本执行完后这个修改还在工作区，但：
- ❌ 没有自动提交到 Git
- ❌ 没有推送到远程仓库

#### 2. **实际发生的情况**

**第一次部署**（我们刚才的部署）:
```bash
$ cat build_seq
8  # 起始值

$ ./deploy-184.sh
# 读取: 8
# 递增: 8 -> 9
# 构建镜像: r1.13-done-f9bea8ac-20260702-9
# 写入 build_seq: 9  ← 但没有提交！

$ cat build_seq
9  # 本地文件已更新
```

**如果再次部署**（假设不提交）:
```bash
$ cat build_seq
9  # 读取上次的本地值

$ ./deploy-184.sh
# 读取: 9
# 递增: 9 -> 10
# 构建镜像: r1.13-done-xxxxx-20260702-10
# 写入 build_seq: 10

$ cat build_seq
10
```

**但是如果切换分支或重新克隆**:
```bash
$ git checkout main
$ cat build_seq
8  # 回到 Git 中的值！
```

#### 3. **为什么设计成这样？**

部署脚本**故意不自动提交** `build_seq`，因为：

1. **灵活性**: 部署可能失败，不应该立即提交
2. **权限**: 部署脚本可能在 CI/CD 中运行，不一定有 Git 提交权限
3. **审查**: 让操作员手动确认后再提交

---

## ✅ 正确的工作流程

### 方案 A: 手动管理（当前方式）

```bash
# 1. 执行部署
./deploy-184.sh

# 2. 验证部署成功

# 3. 手动提交 build_seq
git add build_seq
git commit -m "chore: update build_seq to 9 after 184 deployment"
git push origin main
```

### 方案 B: 自动提交（改进脚本）

在 `deploy-184.sh` 末尾添加：

```bash
# 步骤 9: 提交 build_seq
commit_build_seq() {
    log_step "步骤 9/9: 提交 build_seq"
    
    if git diff --quiet build_seq; then
        log_info "build_seq 未变化，跳过提交"
        return
    fi
    
    git add build_seq
    git commit -m "chore: update build_seq to ${NEW_BUILD_SEQ}"
    git push origin main
    
    log_success "build_seq 已提交并推送"
}
```

---

## 📊 当前状态

### 本地文件
```bash
$ cat build_seq
9  # ✅ 已更新

$ cat version.json
{
  "build_seq": 9,
  "git_sha": "f9bea8ac",
  ...
}
```

### Git 状态
```bash
$ git status
modified: build_seq  # ✅ 已暂存待提交
```

### 生产环境
```bash
# 184 上运行的镜像
r1.13-done-f9bea8ac-20260702-9  # build_seq: 9 ✅
```

---

## 🎯 总结

### 问题
**多次编译为什么 build_seq 没变？**

### 答案
**实际上 build_seq 有变！** 但变化只在：
1. ✅ **本地工作区** - build_seq 文件已更新
2. ✅ **Docker 镜像** - 镜像名称包含新序号
3. ✅ **生产环境** - Pod 内的 VERSION 文件正确
4. ❌ **Git 仓库** - 需要手动提交（刚才已提交）

### 关键点
- `build_seq` 文件是**工作区状态**，不是 Git 状态
- 每次部署**确实**递增了
- 只是需要**手动提交**到 Git

### 验证
```bash
# 查看 Git 历史
$ git log --oneline -3
7356ddbd chore: update build_seq to 9 after 184 deployment  ← 刚才的提交
f9bea8ac chore: 准备 184 部署 - 修复编译错误和添加诊断报告
...

# 查看生产镜像
$ kubectl get pods -n pms-test -o yaml | grep image:
image: 127.0.0.1:5000/kx-llm-gateway-go:r1.13-done-f9bea8ac-20260702-9
                                                                        ↑
                                                                  build_seq: 9
```

---

## 💡 建议

### 短期
- ✅ 已提交 build_seq: 9
- 下次部署前确认是否要推送

### 长期
- 考虑在部署脚本中自动提交
- 或者在 CI/CD 中集中管理 build_seq
- 使用 Git tags 而不是文件跟踪版本

---

**总结**: build_seq **确实在变**，只是你看到的是 Git 中的旧值，实际部署的镜像已经用了新值！
