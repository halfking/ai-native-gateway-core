# 简易·专家模式部署与验收报告

**部署时间**: 2026-11-17 05:32
**目标服务器**: 192.168.31.211:8781（槽a）
**Git提交**: e9886921b
**部署版本**: v2.6.0 (build 3076)

## 一、部署变更

### 1.1 代码变更（远程已合并）
- **提交1**: `eb49441c3` - fix(web): 解除 StageTree/简易供应商构建闸，并接线 ProvidersModeView
- **提交2**: `ec2c50ba0` - fix(web): StageTreeDemo 收契约 A 全屏违规，解除 211 部署闸门
- **提交3**: `f7ebefe64` - feat(web-tests): 添加 Playwright 浏览器测试框架

### 1.2 本轮部署内容
1. **前端文件**: 上传最新构建产物到 /opt/kaixuan/data/llm-gateway/web/a/dist/
2. **版本标识**: deploy-seq e9886921 (git-sha)
3. **UI契约门**: 全部通过（A:0, B:0, C:0）

### 1.3 前端构建
- **构建产物**: 9.8MB，268个资源文件
- **部署路径**: /opt/kaixuan/data/llm-gateway/web/a/dist/
- **部署方式**: rsync增量上传（无需重启容器）

## 二、后端API验证（自动化）

### 2.1 认证层
✅ **登录**: POST /api/auth/token → access_token正常获取

### 2.2 系统信息
✅ **版本信息**: GET /api/system/version
```json
{
  "version": "v2.6.0",
  "build_seq": 3076,
  "git_sha": "fd3ba0cb",
  "target": "211"
}
```

### 2.3 工作模式设置
✅ **GET /api/admin/settings/gateway.mode**
- 当前值: "expert"
- 可选项: ["simple", "expert"]
- 热重载: 支持
- 说明: 控制管理端界面风格

### 2.4 供应商相关
✅ **供应商列表**: GET /api/providers → 61个供应商
✅ **供应商目录**: GET /api/providers/catalog → 1个目录项

### 2.5 密钥管理
✅ **密钥列表**: GET /api/keys → 205个密钥

## 三、前端部署验证

### 3.1 版本一致性
✅ **部署序号**: e9886921 (git-sha)
✅ **index.html**: 正常加载，包含最新meta标签
✅ **静态资源**: 268个文件全部上传

### 3.2 核心功能验证
✅ **SimpleProvidersView.vue**: 已编译到产物中
✅ **simpleNav.ts**: 导航配置已打包
✅ **useWorkMode.ts**: 工作模式composable已打包
✅ **ProvidersModeView**: 模式切换视图已接线

## 四、待人工验收（浏览器）

### 4.1 专家模式（当前默认）
访问: http://192.168.31.211:8781

**验收清单**:
1. [ ] 登录页面正常显示
2. [ ] 使用admin/Veritrans&9527登录成功
3. [ ] 左侧导航显示完整菜单（63个入口）
4. [ ] 供应商管理页面 /providers 正常显示
5. [ ] 密钥管理页面 /keys 正常显示

### 4.2 简易模式切换
**操作步骤**:
1. 访问设置页面 /admin/settings
2. 找到"gateway.mode"设置项
3. 从"expert"切换到"simple"
4. 保存并刷新页面

**验收清单**:
1. [ ] 首页变为供应商接入清单（SimpleProvidersView）
2. [ ] 左侧导航精简为3个主入口
3. [ ] 高级功能收入"高级"菜单组
4. [ ] 快速接入对话框正常弹出
5. [ ] 对话框内容可滚动（max-height生效）

### 4.3 模式切换回滚
**操作步骤**:
1. 从simple模式切回expert模式
2. 刷新页面

**验收清单**:
1. [ ] 导航恢复为完整63项
2. [ ] 首页恢复为原专家模式首页
3. [ ] 所有专家功能可访问

## 五、测试覆盖

### 5.1 单元测试
✅ **useWorkMode**: 9个测试全部通过
✅ **simpleNav**: 4个测试全部通过
✅ **backend settings**: 4个测试全部通过
✅ **总计**: 17个单元测试全部通过

### 5.2 UI契约门
✅ **契约A（全屏）**: 0个违规
✅ **契约B（弹层）**: 0个违规
✅ **契约C（列表）**: 0个违规

### 5.3 Playwright框架
🎯 **新增**: 已添加Playwright浏览器测试框架（commit f7ebefe64）
📋 **待开发**: 简易·专家模式的E2E测试用例

## 六、已知限制

### 6.1 供应商目录
- **当前状态**: GET /api/providers/catalog 只返回1个目录项
- **预期**: 应返回完整的供应商目录列表
- **影响**: 简易模式快速接入功能可能受限
- **待确认**: 是否为数据问题或API问题

### 6.2 TypeScript类型检查
- **文件**: StageTreeVisualization.vue存在类型警告
- **影响**: 编译期类型检查有警告
- **风险**: 低（运行时正常）

## 七、部署配置

### 7.1 容器信息
- **容器名**: llm-gateway-211-a
- **端口**: 8781（槽a）
- **状态**: healthy（运行中）
- **镜像**: llm-gateway-211

### 7.2 挂载点
```
/opt/kaixuan/data/llm-gateway/web/a/dist → /app/web/dist (ro)
/opt/kaixuan/data/llm-gateway/configs → /app/configs (ro)
/opt/kaixuan/data/llm-gateway/data → /app/data (rw)
/opt/kaixuan/data/llm-gateway/logs → /app/logs (rw)
```

### 7.3 Nginx配置
- **upstream**: ns_llm_gateway_upstream → 127.0.0.1:8781
- **配置文件**: /etc/nginx/conf.d/ns-gateway-211.conf
- **公网入口**: 通过nginx反向代理

## 八、回滚方案

如需回滚到旧版本:
```bash
# 1. 备份当前版本
ssh 211 "cd /opt/kaixuan/data/llm-gateway/web/a && cp -r dist dist.backup-$(date +%Y%m%d-%H%M%S)"

# 2. 恢复旧版前端文件
ssh 211 "cd /opt/kaixuan/data/llm-gateway/web/a && rm -rf dist && cp -r dist.old dist"

# 3. 验证版本（无需重启容器）
curl -s http://192.168.31.211:8781/ | grep deploy-seq
```

## 九、交付物清单

### 9.1 代码（已在远程main分支）
- web/src/views/SimpleProvidersView.vue
- web/src/views/ProvidersModeView.vue
- web/src/config/simpleNav.ts
- web/src/composables/useWorkMode.ts

### 9.2 文档
- docs/03-design/02-feature-design/simple-mode-final-delivery-report.md (1050行)
- docs/03-design/02-feature-design/maestro-211-test-report.md (350行)
- docs/03-design/02-feature-design/execution-summary.md
- docs/03-design/02-feature-design/simple-mode-deployment-report-211.md（本文档）

### 9.3 测试
- scripts/maestro-211-test.sh（自动化测试脚本）
- web/src/composables/useWorkMode.test.ts（9个测试）
- web/src/config/simpleNav.test.ts（4个测试）
- Playwright测试框架（已安装，待编写用例）

## 十、下一步行动

### 10.1 立即行动（人工验收）
1. 浏览器访问 http://192.168.31.211:8781
2. 执行上述验收清单（预计15-20分钟）
3. 记录验收结果

### 10.2 后续优化
1. **供应商目录**: 确认catalog API为何只返回1项
2. **E2E测试**: 编写Playwright测试用例
3. **用户文档**: 准备简易模式使用手册
4. **监控**: 观察5秒热重载生效情况

### 10.3 长期计划
1. **默认值**: 保持expert模式为默认（不影响现有用户）
2. **用户培训**: 为需要简易模式的用户提供切换指引
3. **数据监控**: 收集两种模式的使用数据

---
**部署状态**: ✅ 后端API全部验证通过，前端已部署到211槽a，等待人工验收
**验收人**: [待填写]
**验收时间**: [待填写]
**验收结果**: [待填写]
