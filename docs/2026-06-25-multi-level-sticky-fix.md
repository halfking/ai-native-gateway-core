# llm-gateway-go 多级 Sticky 路由修复方案

## 问题描述

2026-06-25 用户报告：
- 手工选择 `claude-opus-4-8` (__USER_2__组第3个)
- 实际使用了 `minimax-m2.7-quickspeed` (__USER_2__组第2个)
- 两次失败后才用上正确的模型
- 第一个成功的claude请求看起来像是"第二回合"

## 根因

**Sticky key 不包含 model 和 session_id**，导致：
1. 同一客户端的不同会话共享同一个 credential
2. 用户切换模型时，仍然被路由到之前模型的 credential
3. 跨模型、跨会话的 sticky 污染

## 解决方案

### 三级 Sticky 优先级体系

```
L1: Session + Model (最高优先级)
    格式: {tenant}:{app}:{key}:{profile}:{session_id}:{model}
    TTL: 1小时
    用途: 同一会话内的模型粘性

L2: Client + Model (中等优先级)  
    格式: {tenant}:{app}:{key}:{profile}:{model}
    TTL: 24小时
    用途: 跨会话的模型偏好

L3: Client Baseline (最低优先级)
    格式: {tenant}:{app}:{key}:{profile}
    TTL: 7天
    用途: 客户端级别的默认供应商（兜底）
```

### 查找逻辑

1. 优先查找 L1 (session+model)
2. 如果 L1 不存在，查找 L2 (client+model)
3. 如果 L2 不存在，查找 L3 (client)
4. 如果 L3 也不存在，执行正常路由决策

### 记录逻辑

成功时同时记录所有适用的级别：
- 如果有 session_id + model → 记录 L1
- 如果有 model → 记录 L2
- 总是记录 L3

## 实现状态

### 已完成
1. ✅ `routing/sticky.go` - 新增 `GetMultiLevel` 和 `RecordSuccessMultiLevel` 方法
2. ✅ `routing/sticky.go` - 新增 `buildStickyKeys` 内部函数构建三级key
3. ✅ `routing/executor.go` - 添加 SessionID 和 Model 字段到 ExecParams
4. ✅ `routing/executor.go` - 新增 `stickyCredentialIDMultiLevel` 方法
5. ✅ `routing/executor.go` - 修改 `recordStickySuccess` 支持多级记录
6. ✅ `relay/handler.go` - 传递 SessionID 和 Model 到 ExecParams

### 待完成
1. ⏸️ **编译错误修复** - ExecParams 字段似乎没有被正确添加
2. ⏸️ 测试验证
3. ⏸️ 部署到 71/184

## 编译错误

当前错误：
```
routing/executor.go:1653:10: params.SessionID undefined
routing/executor.go:1654:10: params.Model undefined
```

**问题**: 虽然我编辑了 ExecParams 添加了 SessionID 和 Model 字段，但编译器仍然报错说找不到这些字段。

**需要**: 重新确认 ExecParams 结构体的完整定义并确保字段添加成功。

## 下一步

1. 修复编译错误（确保 SessionID 和 Model 字段正确添加）
2. 编译通过后，编写测试用例验证多级 sticky 逻辑
3. 本地测试通过后，部署到 184 和 71
4. 清空现有 sticky_sessions 表，避免旧数据干扰
5. 用实际请求验证修复效果

---
**创建时间**: 2026-06-25 03:40
**状态**: 实现中，遇到编译错误需要解决
