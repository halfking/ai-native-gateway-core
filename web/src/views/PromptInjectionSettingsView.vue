<template>
  <div class="prompt-injection-settings">
    <div class="settings-header">
      <h2>🛡️ 提示词注入检测</h2>
      <p class="description">多层防御体系：规则引擎 + LLM 智能检测 + 向量相似度 + Canary Token</p>
    </div>

    <!-- 标签页导航 -->
    <el-tabs v-model="activeTab" type="border-card" class="settings-tabs">
      <!-- Tab 1: 基础配置 -->
      <el-tab-pane label="基础配置" name="basic">
        <template #label>
          <span><el-icon><Setting /></el-icon> 基础配置</span>
        </template>

        <!-- 策略配置 -->
        <el-card class="section-card" shadow="never">
          <template #header>
            <div class="card-header">
              <span>检测策略</span>
              <el-switch
                v-model="policy.enabled"
                active-text="启用"
                inactive-text="禁用"
                @change="handlePolicyChange"
              />
            </div>
          </template>

          <el-form :model="policy" label-width="180px" label-position="left">
            <el-form-item label="检测模式">
              <el-radio-group v-model="policy.detection_mode" @change="handlePolicyChange">
                <el-radio value="observe">
                  <span>观察模式</span>
                  <el-tooltip content="仅记录检测结果，不阻断请求（推荐用于测试）" placement="top">
                    <el-icon class="info-icon"><QuestionFilled /></el-icon>
                  </el-tooltip>
                </el-radio>
                <el-radio value="enforce">
                  <span>强制模式</span>
                  <el-tooltip content="根据严重等级矩阵执行对应动作" placement="top">
                    <el-icon class="info-icon"><QuestionFilled /></el-icon>
                  </el-tooltip>
                </el-radio>
              </el-radio-group>
            </el-form-item>

            <el-divider content-position="left">检测层级</el-divider>

            <el-form-item label="基础规则检测">
              <el-switch v-model="policy.enable_basic_rules" @change="handlePolicyChange" />
              <span class="help-text">15+ 常见注入模式（角色劫持、指令泄漏）</span>
            </el-form-item>

            <el-form-item label="高级规则检测">
              <el-switch v-model="policy.enable_advanced_rules" @change="handlePolicyChange" />
              <span class="help-text">20+ 高级绕过技术（DAN、编码绕过、Unicode混淆）</span>
            </el-form-item>

            <el-form-item label="启发式检测">
              <el-switch v-model="policy.enable_heuristics" @change="handlePolicyChange" />
              <span class="help-text">基于行为特征的智能检测（角色切换频率、异常长句）</span>
            </el-form-item>

            <el-form-item label="LLM 智能检测">
              <el-switch v-model="policy.enable_llm_detection" @change="handlePolicyChange" />
              <span class="help-text">使用 LLM 进行深度语义分析</span>
            </el-form-item>

            <el-form-item label="Canary Token 检测">
              <el-switch v-model="policy.enable_canary_detection" @change="handlePolicyChange" />
              <span class="help-text">检测注入到提示词中的金丝雀令牌是否泄漏</span>
            </el-form-item>

            <el-form-item label="向量相似度检测">
              <el-switch v-model="policy.enable_vector_similarity" @change="handlePolicyChange" />
              <span class="help-text">基于历史攻击库的向量相似度匹配</span>
            </el-form-item>

            <el-divider content-position="left">高级配置</el-divider>

            <el-form-item label="内容替换策略">
              <el-select v-model="policy.content_replacement_strategy" @change="handlePolicyChange">
                <el-option label="LLM 智能重写" value="llm_rewrite" />
                <el-option label="正则脱敏" value="pattern_redact" />
                <el-option label="关键词移除" value="keyword_remove" />
              </el-select>
            </el-form-item>

            <el-form-item label="最大输入长度">
              <el-input-number v-model="policy.max_input_length" :min="1000" :max="200000" :step="1000" @change="handlePolicyChange" />
              <span class="help-text">超过此长度的输入将被截断</span>
            </el-form-item>

            <el-form-item label="检测超时(ms)">
              <el-input-number v-model="policy.detection_timeout_ms" :min="1000" :max="30000" :step="500" @change="handlePolicyChange" />
              <span class="help-text">LLM 检测的超时时间</span>
            </el-form-item>

            <el-form-item label="自动学习">
              <el-switch v-model="policy.auto_learn_enabled" @change="handlePolicyChange" />
              <span class="help-text">自动将检测到的攻击模式加入向量库</span>
            </el-form-item>
          </el-form>
        </el-card>

        <!-- 白名单配置 -->
        <el-card class="section-card" shadow="never">
          <template #header>
            <div class="card-header">
              <span>白名单配置</span>
            </div>
          </template>

          <el-form label-width="180px">
            <el-form-item label="白名单正则">
              <el-input
                v-model="whitelistPatternInput"
                placeholder="例如: ^测试.*$"
                @keyup.enter="addWhitelistPattern"
              >
                <template #append>
                  <el-button @click="addWhitelistPattern">添加</el-button>
                </template>
              </el-input>
              <div class="whitelist-tags">
                <el-tag
                  v-for="(pattern, index) in policy.whitelist_patterns"
                  :key="index"
                  closable
                  @close="removeWhitelistPattern(index)"
                  style="margin: 4px"
                >
                  {{ pattern }}
                </el-tag>
              </div>
            </el-form-item>

            <el-form-item label="白名单用户">
              <el-input
                v-model="whitelistUserInput"
                placeholder="用户邮箱或 ID"
                @keyup.enter="addWhitelistUser"
              >
                <template #append>
                  <el-button @click="addWhitelistUser">添加</el-button>
                </template>
              </el-input>
              <div class="whitelist-tags">
                <el-tag
                  v-for="(user, index) in policy.whitelist_users"
                  :key="index"
                  closable
                  @close="removeWhitelistUser(index)"
                  style="margin: 4px"
                >
                  {{ user }}
                </el-tag>
              </div>
            </el-form-item>
          </el-form>
        </el-card>

        <!-- 通知配置 -->
        <el-card class="section-card" shadow="never">
          <template #header>
            <div class="card-header">
              <span>通知配置</span>
              <el-switch
                v-model="policy.notify_on_detection"
                active-text="启用"
                inactive-text="禁用"
                @change="handlePolicyChange"
              />
            </div>
          </template>

          <el-form label-width="180px" v-if="policy.notify_on_detection">
            <el-form-item label="Webhook URL">
              <el-input
                v-model="policy.notification_webhook"
                placeholder="https://your-webhook.com/endpoint"
                @change="handlePolicyChange"
              />
            </el-form-item>

            <el-form-item label="通知邮箱">
              <el-input
                v-model="policy.notification_email"
                placeholder="admin@example.com"
                @change="handlePolicyChange"
              />
            </el-form-item>
          </el-form>
        </el-card>
      </el-tab-pane>

      <!-- Tab 2: LLM 引擎 -->
      <el-tab-pane label="LLM 引擎" name="engines">
        <template #label>
          <span><el-icon><Cpu /></el-icon> LLM 引擎</span>
        </template>

        <el-card class="section-card" shadow="never">
          <template #header>
            <div class="card-header">
              <span>检测引擎配置</span>
              <el-button type="primary" size="small" @click="showAddEngine = true">
                <el-icon><Plus /></el-icon> 添加引擎
              </el-button>
            </div>
          </template>

          <el-table :data="engines" style="width: 100%" stripe>
            <el-table-column prop="engine_name" label="引擎名称" width="150" />
            <el-table-column prop="model_name" label="模型" width="180">
              <template #default="scope">
                <el-tag size="small">{{ scope.row.model_name || '未配置' }}</el-tag>
              </template>
            </el-table-column>
            <el-table-column prop="priority" label="优先级" width="80" />
            <el-table-column prop="temperature" label="温度" width="80" />
            <el-table-column prop="timeout_ms" label="超时(ms)" width="100" />
            <el-table-column prop="total_calls" label="调用次数" width="100" />
            <el-table-column prop="total_detections" label="检测次数" width="100" />
            <el-table-column prop="avg_latency_ms" label="平均延迟" width="100">
              <template #default="scope">
                {{ scope.row.avg_latency_ms ? scope.row.avg_latency_ms.toFixed(0) + 'ms' : '-' }}
              </template>
            </el-table-column>
            <el-table-column prop="enabled" label="状态" width="80">
              <template #default="scope">
                <el-switch v-model="scope.row.enabled" @change="updateEngine(scope.row)" />
              </template>
            </el-table-column>
            <el-table-column label="操作" width="150">
              <template #default="scope">
                <el-button size="small" @click="editEngine(scope.row)">编辑</el-button>
                <el-button size="small" type="danger" @click="deleteEngine(scope.row)">删除</el-button>
              </template>
            </el-table-column>
          </el-table>
        </el-card>

        <!-- LLM 检测提示词模板 -->
        <el-card class="section-card" shadow="never">
          <template #header>
            <div class="card-header">
              <span>LLM 检测提示词模板</span>
            </div>
          </template>

          <el-alert type="info" :closable="false" style="margin-bottom: 16px">
            <template #title>
              <div>
                <p><strong>可用变量：</strong></p>
                <p><code>{user_input}</code> - 用户输入内容</p>
                <p><code>{system_prompt}</code> - 系统提示词（如有）</p>
                <p><code>{detection_categories}</code> - 检测类别列表</p>
              </div>
            </template>
          </el-alert>

          <el-input
            v-model="selectedEngine.system_prompt"
            type="textarea"
            :rows="4"
            placeholder="系统提示词"
          />
          <el-input
            v-model="selectedEngine.detection_prompt"
            type="textarea"
            :rows="8"
            placeholder="检测提示词"
            style="margin-top: 12px"
          />
        </el-card>
      </el-tab-pane>

      <!-- Tab 3: 严重等级矩阵 -->
      <el-tab-pane label="处理矩阵" name="severity">
        <template #label>
          <span><el-icon><Warning /></el-icon> 处理矩阵</span>
        </template>

        <el-card class="section-card" shadow="never">
          <template #header>
            <div class="card-header">
              <span>严重等级处理矩阵</span>
              <el-button type="primary" size="small" @click="saveSeverityMatrix">保存配置</el-button>
            </div>
          </template>

          <el-alert type="info" :closable="false" style="margin-bottom: 16px">
            <template #title>
              配置不同严重等级在观察模式和执行模式下的处理动作。审批超时设为 0 表示无限等待。
            </template>
          </el-alert>

          <el-table :data="severityMatrix" style="width: 100%" stripe border>
            <el-table-column prop="severity_level" label="严重等级" width="120">
              <template #default="scope">
                <el-tag :type="getSeverityTagType(scope.row.severity_level)" size="large">
                  {{ getSeverityLabel(scope.row.severity_level) }}
                </el-tag>
              </template>
            </el-table-column>

            <el-table-column label="观察模式动作" width="150">
              <template #default="scope">
                <el-select v-model="scope.row.observe_action" size="small">
                  <el-option label="记录日志" value="log" />
                  <el-option label="警告" value="warn" />
                </el-select>
              </template>
            </el-table-column>

            <el-table-column label="执行模式动作" width="150">
              <template #default="scope">
                <el-select v-model="scope.row.enforce_action" size="small">
                  <el-option label="记录日志" value="log" />
                  <el-option label="警告" value="warn" />
                  <el-option label="替换内容" value="replace" />
                  <el-option label="脱敏处理" value="redact" />
                  <el-option label="移除片段" value="remove" />
                  <el-option label="拒绝请求" value="reject" />
                  <el-option label="终止会话" value="terminate" />
                  <el-option label="人工审批" value="approve" />
                  <el-option label="阻断" value="block" />
                </el-select>
              </template>
            </el-table-column>

            <el-table-column label="需要审批" width="100">
              <template #default="scope">
                <el-switch v-model="scope.row.require_approval" />
              </template>
            </el-table-column>

            <el-table-column label="审批超时(分钟)" width="140">
              <template #default="scope">
                <el-input-number v-model="scope.row.approval_timeout_minutes" :min="0" :max="1440" size="small" :disabled="!scope.row.require_approval" />
                <div class="help-text-small">0=无限等待</div>
              </template>
            </el-table-column>

            <el-table-column label="通知" width="80">
              <template #default="scope">
                <el-switch v-model="scope.row.notify_on_detect" />
              </template>
            </el-table-column>

            <el-table-column label="会话健康扣分" width="130">
              <template #default="scope">
                <el-input-number v-model="scope.row.session_health_penalty" :min="0" :max="100" size="small" />
              </template>
            </el-table-column>

            <el-table-column label="重复触发终止" width="120">
              <template #default="scope">
                <el-switch v-model="scope.row.terminate_session_on_repeat" />
                <div v-if="scope.row.terminate_session_on_repeat" class="help-text-small">
                  阈值: <el-input-number v-model="scope.row.repeat_threshold" :min="1" :max="10" size="small" style="width: 60px" />
                </div>
              </template>
            </el-table-column>
          </el-table>
        </el-card>

        <!-- 流程说明 -->
        <el-card class="section-card" shadow="never">
          <template #header>
            <div class="card-header">
              <span>处理流程说明</span>
            </div>
          </template>

          <div class="flow-description">
            <el-descriptions :column="1" border>
              <el-descriptions-item label="pass">放行 - 无风险，正常处理</el-descriptions-item>
              <el-descriptions-item label="log">记录 - 仅记录日志，不影响请求</el-descriptions-item>
              <el-descriptions-item label="warn">警告 - 记录日志 + 响应头添加 X-Security-Warning</el-descriptions-item>
              <el-descriptions-item label="replace">替换 - 使用 LLM 重写安全版本后继续</el-descriptions-item>
              <el-descriptions-item label="redact">脱敏 - 将敏感信息替换为 [REDACTED]</el-descriptions-item>
              <el-descriptions-item label="remove">移除 - 从输入中移除恶意片段</el-descriptions-item>
              <el-descriptions-item label="reject">拒绝 - 返回 HTTP 403，拒绝请求</el-descriptions-item>
              <el-descriptions-item label="terminate">终止 - 终止当前会话</el-descriptions-item>
              <el-descriptions-item label="approve">审批 - 暂停请求，等待人工审批</el-descriptions-item>
              <el-descriptions-item label="block">阻断 - 直接阻断并记录</el-descriptions-item>
            </el-descriptions>
          </div>
        </el-card>
      </el-tab-pane>

      <!-- Tab 4: 检测规则 -->
      <el-tab-pane label="检测规则" name="rules">
        <template #label>
          <span><el-icon><List /></el-icon> 检测规则</span>
        </template>

        <el-card class="section-card" shadow="never">
          <template #header>
            <div class="card-header">
              <span>检测规则库</span>
              <div>
                <el-input v-model="ruleSearch" placeholder="搜索规则..." style="width: 200px; margin-right: 12px" size="small" @input="loadRules">
                  <template #prefix>
                    <el-icon><Search /></el-icon>
                  </template>
                </el-input>
                <el-button type="primary" size="small" @click="showAddRule = true">
                  <el-icon><Plus /></el-icon> 添加规则
                </el-button>
              </div>
            </div>
          </template>

          <!-- 分类筛选 -->
          <div class="category-filter">
            <el-tag
              v-for="cat in ruleCategories"
              :key="cat.value"
              :type="ruleCategoryFilter === cat.value ? 'primary' : ''"
              :effect="ruleCategoryFilter === cat.value ? 'dark' : 'plain'"
              @click="ruleCategoryFilter = cat.value; loadRules()"
              style="margin-right: 8px; margin-bottom: 8px; cursor: pointer"
            >
              {{ cat.label }} ({{ cat.count }})
            </el-tag>
          </div>

          <el-table :data="rules" style="width: 100%" stripe>
            <el-table-column prop="rule_name" label="规则名称" width="220" show-overflow-tooltip />
            <el-table-column prop="category_new" label="风险类别" width="150">
              <template #default="scope">
                <el-tag :type="getCategoryType(scope.row.category_new || scope.row.category)" size="small">
                  {{ getCategoryLabel(scope.row.category_new || scope.row.category) }}
                </el-tag>
              </template>
            </el-table-column>
            <el-table-column prop="severity" label="严重度" width="100">
              <template #default="scope">
                <el-tag :type="getSeverityType(scope.row.severity)" size="small">
                  {{ scope.row.severity }}/10
                </el-tag>
              </template>
            </el-table-column>
            <el-table-column prop="description" label="说明" show-overflow-tooltip />
            <el-table-column prop="is_system" label="类型" width="80">
              <template #default="scope">
                <el-tag :type="scope.row.is_system ? 'info' : 'success'" size="small">
                  {{ scope.row.is_system ? '系统' : '自定义' }}
                </el-tag>
              </template>
            </el-table-column>
            <el-table-column prop="enabled" label="状态" width="80">
              <template #default="scope">
                <el-switch v-model="scope.row.enabled" @change="toggleRule(scope.row)" />
              </template>
            </el-table-column>
            <el-table-column label="操作" width="100">
              <template #default="scope">
                <el-button v-if="!scope.row.is_system" size="small" type="danger" @click="deleteRule(scope.row)">删除</el-button>
              </template>
            </el-table-column>
          </el-table>
        </el-card>
      </el-tab-pane>

      <!-- Tab 5: Canary Token -->
      <el-tab-pane label="Canary Token" name="canary">
        <template #label>
          <span><el-icon><Key /></el-icon> Canary Token</span>
        </template>

        <el-card class="section-card" shadow="never">
          <template #header>
            <div class="card-header">
              <span>Canary Token 管理</span>
              <el-button type="primary" size="small" @click="showAddCanary = true">
                <el-icon><Plus /></el-icon> 创建 Token
              </el-button>
            </div>
          </template>

          <el-alert type="info" :closable="false" style="margin-bottom: 16px">
            <template #title>
              Canary Token 是注入到系统提示词中的特殊标记。如果在用户输入中检测到这些 token，说明系统提示词已被泄漏。
            </template>
          </el-alert>

          <el-table :data="canaryTokens" style="width: 100%" stripe>
            <el-table-column prop="token_name" label="名称" width="150" />
            <el-table-column prop="token_value" label="Token 值" width="300">
              <template #default="scope">
                <el-text truncated>{{ scope.row.token_value }}</el-text>
              </template>
            </el-table-column>
            <el-table-column prop="token_type" label="类型" width="80">
              <template #default="scope">
                <el-tag size="small">{{ scope.row.token_type }}</el-tag>
              </template>
            </el-table-column>
            <el-table-column prop="leak_action" label="泄漏动作" width="100">
              <template #default="scope">
                <el-tag :type="getActionType(scope.row.leak_action)" size="small">
                  {{ getActionLabel(scope.row.leak_action) }}
                </el-tag>
              </template>
            </el-table-column>
            <el-table-column prop="times_injected" label="注入次数" width="100" />
            <el-table-column prop="times_leaked" label="泄漏次数" width="100">
              <template #default="scope">
                <el-tag :type="scope.row.times_leaked > 0 ? 'danger' : 'success'" size="small">
                  {{ scope.row.times_leaked }}
                </el-tag>
              </template>
            </el-table-column>
            <el-table-column prop="active" label="状态" width="80">
              <template #default="scope">
                <el-switch v-model="scope.row.active" @change="updateCanaryToken(scope.row)" />
              </template>
            </el-table-column>
            <el-table-column label="操作" width="100">
              <template #default="scope">
                <el-button size="small" type="danger" @click="deleteCanaryToken(scope.row)">删除</el-button>
              </template>
            </el-table-column>
          </el-table>
        </el-card>
      </el-tab-pane>

      <!-- Tab 6: 审批队列 -->
      <el-tab-pane label="审批队列" name="approvals">
        <template #label>
          <span><el-icon><Checked /></el-icon> 审批队列</span>
        </template>

        <el-card class="section-card" shadow="never">
          <template #header>
            <div class="card-header">
              <span>审批管理</span>
              <el-button type="primary" @click="goToApprovalPage">
                <el-icon><Link /></el-icon> 前往审批中心
              </el-button>
            </div>
          </template>

          <el-alert type="info" :closable="false" style="margin-bottom: 16px">
            <template #title>
              <div>
                <p>提示词注入检测的高风险请求会自动进入审批队列。审批功能由统一的审批中心管理。</p>
                <p style="margin-top: 8px"><strong>审批流程：</strong></p>
                <ol style="margin: 8px 0; padding-left: 20px">
                  <li>检测到高风险请求 → 自动创建审批记录</li>
                  <li>审批人收到通知（飞书/钉钉/邮件）</li>
                  <li>审批人批准/拒绝请求</li>
                  <li>批准后自动继续处理，拒绝后返回错误</li>
                </ol>
              </div>
            </template>
          </el-alert>

          <!-- 快速链接 -->
          <el-row :gutter="20">
            <el-col :span="8">
              <el-card shadow="hover" class="quick-link-card" @click="goToApprovalPage">
                <el-icon size="48" color="#409eff"><Checked /></el-icon>
                <h3>审批队列</h3>
                <p>查看和处理待审批请求</p>
              </el-card>
            </el-col>
            <el-col :span="8">
              <el-card shadow="hover" class="quick-link-card" @click="goToApprovalConfig">
                <el-icon size="48" color="#67c23a"><Setting /></el-icon>
                <h3>审批配置</h3>
                <p>配置审批规则和审批人</p>
              </el-card>
            </el-col>
            <el-col :span="8">
              <el-card shadow="hover" class="quick-link-card" @click="goToSeverityMatrix">
                <el-icon size="48" color="#e6a23c"><Warning /></el-icon>
                <h3>处理矩阵</h3>
                <p>配置风险等级对应的处理动作</p>
              </el-card>
            </el-col>
          </el-row>

          <!-- 审批配置说明 -->
          <el-divider content-position="left">审批触发条件</el-divider>

          <el-descriptions :column="2" border>
            <el-descriptions-item label="高风险 (High)">
              <el-tag type="warning">score >= 8</el-tag>
              <span style="margin-left: 8px">需要审批（如果配置了审批人）</span>
            </el-descriptions-item>
            <el-descriptions-item label="严重风险 (Critical)">
              <el-tag type="danger">score >= 10</el-tag>
              <span style="margin-left: 8px">需要审批或直接阻断</span>
            </el-descriptions-item>
          </el-descriptions>

          <el-alert type="warning" :closable="false" style="margin-top: 16px">
            <template #title>
              请在"处理矩阵"标签页中配置哪些风险等级需要审批，以及审批超时后的处理方式。
            </template>
          </el-alert>
        </el-card>
      </el-tab-pane>

      <!-- Tab 7: 统计监控 -->
      <el-tab-pane label="统计监控" name="stats">
        <template #label>
          <span><el-icon><DataAnalysis /></el-icon> 统计监控</span>
        </template>

        <!-- 统计概览 -->
        <el-card class="section-card" shadow="never">
          <template #header>
            <div class="card-header">
              <span>今日统计</span>
              <el-button size="small" @click="refreshStats">刷新</el-button>
            </div>
          </template>

          <el-row :gutter="20">
            <el-col :span="4">
              <el-statistic title="总检测" :value="stats.total_detections">
                <template #suffix>次</template>
              </el-statistic>
            </el-col>
            <el-col :span="4">
              <el-statistic title="阻断" :value="stats.blocked_count">
                <template #suffix>次</template>
              </el-statistic>
            </el-col>
            <el-col :span="4">
              <el-statistic title="审批" :value="stats.approval_count">
                <template #suffix>次</template>
              </el-statistic>
            </el-col>
            <el-col :span="4">
              <el-statistic title="替换" :value="stats.replaced_count">
                <template #suffix>次</template>
              </el-statistic>
            </el-col>
            <el-col :span="4">
              <el-statistic title="终止" :value="stats.terminated_count">
                <template #suffix>次</template>
              </el-statistic>
            </el-col>
            <el-col :span="4">
              <el-statistic title="Canary泄漏" :value="stats.canary_leak_count">
                <template #suffix>次</template>
              </el-statistic>
            </el-col>
          </el-row>

          <el-divider />

          <el-row :gutter="20">
            <el-col :span="6">
              <el-statistic title="平均评分" :value="stats.avg_score" :precision="1">
                <template #suffix>/ 10</template>
              </el-statistic>
            </el-col>
            <el-col :span="6">
              <el-statistic title="最高评分" :value="stats.max_score">
                <template #suffix>/ 10</template>
              </el-statistic>
            </el-col>
            <el-col :span="6">
              <el-statistic title="LLM 平均置信度" :value="stats.avg_llm_confidence" :precision="2">
                <template #suffix>/ 1</template>
              </el-statistic>
            </el-col>
            <el-col :span="6">
              <el-statistic title="受影响会话" :value="stats.affected_sessions">
                <template #suffix>个</template>
              </el-statistic>
            </el-col>
          </el-row>

          <el-divider />

          <div class="risk-distribution">
            <h4>风险等级分布</h4>
            <el-row :gutter="10">
              <el-col :span="6">
                <div class="risk-item risk-critical">
                  <div class="risk-label">严重</div>
                  <div class="risk-count">{{ stats.critical_count }}</div>
                </div>
              </el-col>
              <el-col :span="6">
                <div class="risk-item risk-high">
                  <div class="risk-label">高</div>
                  <div class="risk-count">{{ stats.high_count }}</div>
                </div>
              </el-col>
              <el-col :span="6">
                <div class="risk-item risk-medium">
                  <div class="risk-label">中</div>
                  <div class="risk-count">{{ stats.medium_count }}</div>
                </div>
              </el-col>
              <el-col :span="6">
                <div class="risk-item risk-low">
                  <div class="risk-label">低</div>
                  <div class="risk-count">{{ stats.low_count }}</div>
                </div>
              </el-col>
            </el-row>
          </div>
        </el-card>

        <!-- 检测日志 -->
        <el-card class="section-card" shadow="never">
          <template #header>
            <div class="card-header">
              <span>检测日志</span>
              <el-button size="small" @click="loadDetections">刷新</el-button>
            </div>
          </template>

          <el-form :inline="true" class="filter-form">
            <el-form-item label="风险等级">
              <el-select v-model="detectionFilter.risk_level" @change="loadDetections" clearable>
                <el-option label="严重" value="critical" />
                <el-option label="高" value="high" />
                <el-option label="中" value="medium" />
                <el-option label="低" value="low" />
              </el-select>
            </el-form-item>

            <el-form-item label="动作">
              <el-select v-model="detectionFilter.action" @change="loadDetections" clearable>
                <el-option label="记录" value="log" />
                <el-option label="警告" value="warn" />
                <el-option label="替换" value="replace" />
                <el-option label="拒绝" value="reject" />
                <el-option label="阻断" value="block" />
                <el-option label="审批" value="approve" />
              </el-select>
            </el-form-item>

            <el-form-item label="会话">
              <el-input
                v-model="detectionFilter.session_key"
                placeholder="Session Key"
                @keyup.enter="loadDetections"
                clearable
              />
            </el-form-item>
          </el-form>

          <el-table :data="detections" style="width: 100%" stripe>
            <el-table-column prop="detected_at" label="时间" width="180" />
            <el-table-column prop="request_id" label="请求 ID" width="180" show-overflow-tooltip />
            <el-table-column prop="detection_score" label="评分" width="80">
              <template #default="scope">
                <el-tag :type="getSeverityType(scope.row.detection_score)" size="small">
                  {{ scope.row.detection_score }}
                </el-tag>
              </template>
            </el-table-column>
            <el-table-column prop="risk_level" label="风险等级" width="100">
              <template #default="scope">
                <el-tag :type="getRiskLevelType(scope.row.risk_level)" size="small">
                  {{ getRiskLevelLabel(scope.row.risk_level) }}
                </el-tag>
              </template>
            </el-table-column>
            <el-table-column prop="categories" label="风险类别" width="200">
              <template #default="scope">
                <el-tag v-for="cat in scope.row.categories" :key="cat" size="small" style="margin-right: 4px">
                  {{ getCategoryLabel(cat) }}
                </el-tag>
              </template>
            </el-table-column>
            <el-table-column prop="action_taken" label="动作" width="100">
              <template #default="scope">
                <el-tag :type="getActionType(scope.row.action_taken)" size="small">
                  {{ getActionLabel(scope.row.action_taken) }}
                </el-tag>
              </template>
            </el-table-column>
            <el-table-column prop="llm_confidence" label="LLM置信度" width="100">
              <template #default="scope">
                {{ scope.row.llm_confidence ? (scope.row.llm_confidence * 100).toFixed(0) + '%' : '-' }}
              </template>
            </el-table-column>
            <el-table-column prop="evidence_text" label="证据" show-overflow-tooltip />
          </el-table>

          <el-pagination
            v-model:current-page="detectionPagination.page"
            v-model:page-size="detectionPagination.page_size"
            :total="detectionPagination.total"
            :page-sizes="[10, 20, 50, 100]"
            layout="total, sizes, prev, pager, next, jumper"
            @current-change="loadDetections"
            @size-change="loadDetections"
            style="margin-top: 20px"
          />
        </el-card>
      </el-tab-pane>
    </el-tabs>

    <!-- 添加引擎对话框 -->
    <el-dialog v-model="showAddEngine" title="添加 LLM 引擎" width="600px">
      <el-form :model="newEngine" label-width="120px">
        <el-form-item label="引擎名称" required>
          <el-input v-model="newEngine.engine_name" placeholder="例如: gpt4o-detection" />
        </el-form-item>
        <el-form-item label="描述">
          <el-input v-model="newEngine.description" placeholder="引擎用途描述" />
        </el-form-item>
        <el-form-item label="模型">
          <el-select v-model="newEngine.model_canonical_id" placeholder="选择模型">
            <el-option v-for="m in availableModels" :key="m.id" :label="m.canonical_name" :value="m.id" />
          </el-select>
        </el-form-item>
        <el-form-item label="温度">
          <el-slider v-model="newEngine.temperature" :min="0" :max="2" :step="0.1" />
        </el-form-item>
        <el-form-item label="最大 Token">
          <el-input-number v-model="newEngine.max_tokens" :min="100" :max="4096" />
        </el-form-item>
        <el-form-item label="超时(ms)">
          <el-input-number v-model="newEngine.timeout_ms" :min="1000" :max="30000" />
        </el-form-item>
        <el-form-item label="优先级">
          <el-input-number v-model="newEngine.priority" :min="0" :max="100" />
          <span class="help-text">越高越优先</span>
        </el-form-item>
        <el-form-item label="启用">
          <el-switch v-model="newEngine.enabled" />
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="showAddEngine = false">取消</el-button>
        <el-button type="primary" @click="createEngine">创建</el-button>
      </template>
    </el-dialog>

    <!-- 添加规则对话框 -->
    <el-dialog v-model="showAddRule" title="添加检测规则" width="600px">
      <el-form :model="newRule" label-width="120px">
        <el-form-item label="规则名称" required>
          <el-input v-model="newRule.rule_name" placeholder="例如: custom_injection_1" />
        </el-form-item>
        <el-form-item label="规则类型">
          <el-select v-model="newRule.rule_type">
            <el-option label="基础规则" value="basic" />
            <el-option label="高级规则" value="advanced" />
          </el-select>
        </el-form-item>
        <el-form-item label="风险类别">
          <el-select v-model="newRule.category_new">
            <el-option v-for="cat in allCategories" :key="cat.value" :label="cat.label" :value="cat.value" />
          </el-select>
        </el-form-item>
        <el-form-item label="正则表达式" required>
          <el-input v-model="newRule.pattern" type="textarea" :rows="3" placeholder="正则表达式" />
        </el-form-item>
        <el-form-item label="描述">
          <el-input v-model="newRule.description" placeholder="规则描述" />
        </el-form-item>
        <el-form-item label="严重等级">
          <el-slider v-model="newRule.severity" :min="1" :max="10" show-stops />
        </el-form-item>
        <el-form-item label="大小写敏感">
          <el-switch v-model="newRule.case_sensitive" />
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="showAddRule = false">取消</el-button>
        <el-button type="primary" @click="createRule">创建</el-button>
      </template>
    </el-dialog>

    <!-- 添加 Canary Token 对话框 -->
    <el-dialog v-model="showAddCanary" title="创建 Canary Token" width="500px">
      <el-form :model="newCanary" label-width="120px">
        <el-form-item label="Token 名称">
          <el-input v-model="newCanary.token_name" placeholder="例如: main-prompt-canary" />
        </el-form-item>
        <el-form-item label="Token 类型">
          <el-select v-model="newCanary.token_type">
            <el-option label="UUID" value="uuid" />
            <el-option label="自定义" value="custom" />
          </el-select>
        </el-form-item>
        <el-form-item label="Token 值" v-if="newCanary.token_type === 'custom'">
          <el-input v-model="newCanary.token_value" placeholder="自定义 token 值" />
        </el-form-item>
        <el-form-item label="描述">
          <el-input v-model="newCanary.description" placeholder="Token 用途描述" />
        </el-form-item>
        <el-form-item label="泄漏动作">
          <el-select v-model="newCanary.leak_action">
            <el-option label="阻断" value="block" />
            <el-option label="拒绝" value="reject" />
            <el-option label="警告" value="warn" />
            <el-option label="记录" value="log" />
          </el-select>
        </el-form-item>
        <el-form-item label="通知">
          <el-switch v-model="newCanary.notify_on_leak" />
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="showAddCanary = false">取消</el-button>
        <el-button type="primary" @click="createCanaryToken">创建</el-button>
      </template>
    </el-dialog>

  </div>
</template>

<script setup lang="ts">
import { ref, reactive, onMounted, computed } from 'vue'
import { useRouter } from 'vue-router'
import { ElMessage, ElMessageBox } from 'element-plus'
import {
  QuestionFilled, CircleCloseFilled, CircleCheckFilled,
  Setting, Cpu, Warning, List, Key, Checked, DataAnalysis,
  Plus, Search, Link
} from '@element-plus/icons-vue'
import { req } from '@/api/_core'

// 当前标签页
const activeTab = ref('basic')

// ==================== 策略配置 ====================
const policy = reactive({
  tenant_id: '',
  enabled: true,
  detection_mode: 'observe',
  enable_basic_rules: true,
  enable_advanced_rules: true,
  enable_heuristics: true,
  enable_ml_model: false,
  enable_llm_detection: true,
  enable_canary_detection: true,
  enable_vector_similarity: false,
  llm_engine_id: null as number | null,
  content_replacement_strategy: 'llm_rewrite',
  max_input_length: 50000,
  auto_learn_enabled: false,
  detection_timeout_ms: 5000,
  score_threshold_log: 3,
  score_threshold_warn: 6,
  score_threshold_sanitize: 8,
  score_threshold_block: 10,
  action_on_low_risk: 'log',
  action_on_medium_risk: 'warn',
  action_on_high_risk: 'block',
  whitelist_patterns: [] as string[],
  whitelist_users: [] as string[],
  notify_on_detection: false,
  notification_webhook: '',
  notification_email: '',
  total_detections: 0,
  total_blocks: 0,
  last_detection_at: null as string | null,
})

const loadPolicy = async () => {
  try {
    const res = await req<any>('GET', '/api/admin/prompt-injection/policy')
    Object.assign(policy, res)
  } catch (error: any) {
    ElMessage.error('加载策略失败: ' + error.message)
  }
}

const handlePolicyChange = async () => {
  try {
    await req('PUT', '/api/admin/prompt-injection/policy', policy)
    ElMessage.success('策略已更新')
  } catch (error: any) {
    ElMessage.error('更新策略失败: ' + error.message)
  }
}

// 白名单
const whitelistPatternInput = ref('')
const whitelistUserInput = ref('')

const addWhitelistPattern = () => {
  if (whitelistPatternInput.value.trim()) {
    policy.whitelist_patterns.push(whitelistPatternInput.value.trim())
    whitelistPatternInput.value = ''
    handlePolicyChange()
  }
}

const removeWhitelistPattern = (index: number) => {
  policy.whitelist_patterns.splice(index, 1)
  handlePolicyChange()
}

const addWhitelistUser = () => {
  if (whitelistUserInput.value.trim()) {
    policy.whitelist_users.push(whitelistUserInput.value.trim())
    whitelistUserInput.value = ''
    handlePolicyChange()
  }
}

const removeWhitelistUser = (index: number) => {
  policy.whitelist_users.splice(index, 1)
  handlePolicyChange()
}

// ==================== LLM 引擎 ====================
const engines = ref<any[]>([])
const showAddEngine = ref(false)
const selectedEngine = reactive({
  system_prompt: '',
  detection_prompt: '',
})

const newEngine = reactive({
  engine_name: '',
  description: '',
  model_canonical_id: null as number | null,
  temperature: 0.1,
  max_tokens: 512,
  timeout_ms: 3000,
  priority: 0,
  enabled: true,
})

const availableModels = ref<any[]>([])

const loadEngines = async () => {
  try {
    const res = await req<any>('GET', '/api/admin/prompt-injection/engines')
    engines.value = res.engines || []
  } catch (error: any) {
    ElMessage.error('加载引擎失败: ' + error.message)
  }
}

const createEngine = async () => {
  try {
    await req('POST', '/api/admin/prompt-injection/engines', newEngine)
    ElMessage.success('引擎已创建')
    showAddEngine.value = false
    loadEngines()
  } catch (error: any) {
    ElMessage.error('创建引擎失败: ' + error.message)
  }
}

const updateEngine = async (engine: any) => {
  try {
    await req('PUT', `/api/admin/prompt-injection/engines/${engine.id}`, engine)
    ElMessage.success('引擎已更新')
  } catch (error: any) {
    ElMessage.error('更新引擎失败: ' + error.message)
    engine.enabled = !engine.enabled
  }
}

const editEngine = (engine: any) => {
  Object.assign(selectedEngine, engine)
}

const deleteEngine = async (engine: any) => {
  try {
    await ElMessageBox.confirm('确定删除此引擎？', '确认')
    await req('DELETE', `/api/admin/prompt-injection/engines/${engine.id}`)
    ElMessage.success('引擎已删除')
    loadEngines()
  } catch (error: any) {
    if (error !== 'cancel') {
      ElMessage.error('删除引擎失败: ' + error.message)
    }
  }
}

// ==================== 严重等级矩阵 ====================
const severityMatrix = ref<any[]>([])

const loadSeverityMatrix = async () => {
  try {
    const res = await req<any>('GET', '/api/admin/prompt-injection/severity-matrix')
    severityMatrix.value = res.matrix || []
  } catch (error: any) {
    ElMessage.error('加载矩阵失败: ' + error.message)
  }
}

const saveSeverityMatrix = async () => {
  try {
    await req('PUT', '/api/admin/prompt-injection/severity-matrix', severityMatrix.value)
    ElMessage.success('矩阵已保存')
  } catch (error: any) {
    ElMessage.error('保存矩阵失败: ' + error.message)
  }
}

// ==================== 检测规则 ====================
const rules = ref<any[]>([])
const ruleSearch = ref('')
const ruleCategoryFilter = ref('')
const showAddRule = ref(false)

const newRule = reactive({
  rule_name: '',
  rule_type: 'basic',
  category_new: '',
  pattern: '',
  description: '',
  severity: 5,
  case_sensitive: false,
  enabled: true,
})

const allCategories = [
  { value: 'role_hijack', label: '角色劫持' },
  { value: 'instruction_override', label: '指令覆盖' },
  { value: 'instruction_leak', label: '指令泄漏' },
  { value: 'jailbreak', label: '越狱攻击' },
  { value: 'encoding_bypass', label: '编码绕过' },
  { value: 'injection_marker', label: '注入标记' },
  { value: 'multi_turn_attack', label: '多轮攻击' },
  { value: 'resource_exhaustion', label: '资源耗尽' },
  { value: 'data_exfiltration', label: '数据窃取' },
  { value: 'social_engineering', label: '社会工程' },
  { value: 'prompt_leaking', label: '提示词泄漏' },
  { value: 'payload_smuggling', label: 'Payload走私' },
  { value: 'unicode_obfuscation', label: 'Unicode混淆' },
  { value: 'context_manipulation', label: '上下文操纵' },
  { value: 'tool_abuse', label: '工具滥用' },
]

const ruleCategories = computed(() => {
  const counts: Record<string, number> = {}
  rules.value.forEach(r => {
    const cat = r.category_new || r.category
    counts[cat] = (counts[cat] || 0) + 1
  })
  return [
    { value: '', label: '全部', count: rules.value.length },
    ...allCategories.map(c => ({
      ...c,
      count: counts[c.value] || 0,
    })).filter(c => c.count > 0)
  ]
})

const loadRules = async () => {
  try {
    const params: any = {}
    if (ruleCategoryFilter.value) params.category = ruleCategoryFilter.value
    if (ruleSearch.value) params.search = ruleSearch.value
    const queryString = new URLSearchParams(params).toString()
    const url = '/api/admin/prompt-injection/rules' + (queryString ? '?' + queryString : '')
    const res = await req<any>('GET', url)
    rules.value = res.rules || []
  } catch (error: any) {
    ElMessage.error('加载规则失败: ' + error.message)
  }
}

const toggleRule = async (rule: any) => {
  try {
    await req('POST', `/api/admin/prompt-injection/rules/${rule.id}/toggle`, { enabled: rule.enabled })
    ElMessage.success(`规则已${rule.enabled ? '启用' : '禁用'}`)
  } catch (error: any) {
    ElMessage.error('切换规则失败: ' + error.message)
    rule.enabled = !rule.enabled
  }
}

const createRule = async () => {
  try {
    await req('POST', '/api/admin/prompt-injection/rules', newRule)
    ElMessage.success('规则已创建')
    showAddRule.value = false
    loadRules()
  } catch (error: any) {
    ElMessage.error('创建规则失败: ' + error.message)
  }
}

const deleteRule = async (rule: any) => {
  try {
    await ElMessageBox.confirm('确定删除此规则？', '确认')
    await req('DELETE', `/api/admin/prompt-injection/rules/${rule.id}`)
    ElMessage.success('规则已删除')
    loadRules()
  } catch (error: any) {
    if (error !== 'cancel') {
      ElMessage.error('删除规则失败: ' + error.message)
    }
  }
}

// ==================== Canary Token ====================
const canaryTokens = ref<any[]>([])
const showAddCanary = ref(false)

const newCanary = reactive({
  token_name: '',
  token_type: 'uuid',
  token_value: '',
  description: '',
  leak_action: 'block',
  notify_on_leak: true,
  active: true,
})

const loadCanaryTokens = async () => {
  try {
    const res = await req<any>('GET', '/api/admin/prompt-injection/canary-tokens')
    canaryTokens.value = res.tokens || []
  } catch (error: any) {
    ElMessage.error('加载 Token 失败: ' + error.message)
  }
}

const createCanaryToken = async () => {
  try {
    await req('POST', '/api/admin/prompt-injection/canary-tokens', newCanary)
    ElMessage.success('Token 已创建')
    showAddCanary.value = false
    loadCanaryTokens()
  } catch (error: any) {
    ElMessage.error('创建 Token 失败: ' + error.message)
  }
}

const updateCanaryToken = async (token: any) => {
  try {
    await req('PUT', `/api/admin/prompt-injection/canary-tokens/${token.id}`, token)
    ElMessage.success('Token 已更新')
  } catch (error: any) {
    ElMessage.error('更新 Token 失败: ' + error.message)
    token.active = !token.active
  }
}

const deleteCanaryToken = async (token: any) => {
  try {
    await ElMessageBox.confirm('确定删除此 Token？', '确认')
    await req('DELETE', `/api/admin/prompt-injection/canary-tokens/${token.id}`)
    ElMessage.success('Token 已删除')
    loadCanaryTokens()
  } catch (error: any) {
    if (error !== 'cancel') {
      ElMessage.error('删除 Token 失败: ' + error.message)
    }
  }
}

// 审批相关 - 使用现有审批系统
const router = useRouter()

const goToApprovalPage = () => {
  router.push('/admin/approvals')
}

const goToApprovalConfig = () => {
  router.push('/admin/approval-config')
}

const goToSeverityMatrix = () => {
  activeTab.value = 'severity'
}

// ==================== 统计监控 ====================
const stats = reactive({
  total_detections: 0,
  blocked_count: 0,
  critical_count: 0,
  high_count: 0,
  medium_count: 0,
  low_count: 0,
  approval_count: 0,
  replaced_count: 0,
  terminated_count: 0,
  canary_leak_count: 0,
  avg_score: 0,
  max_score: 0,
  avg_llm_confidence: 0,
  affected_sessions: 0,
})

const detections = ref<any[]>([])
const detectionFilter = reactive({
  risk_level: '',
  blocked: '',
  action: '',
  session_key: '',
})
const detectionPagination = reactive({
  page: 1,
  page_size: 20,
  total: 0,
})

const refreshStats = async () => {
  try {
    const res = await req<any>('GET', '/api/admin/prompt-injection/stats')
    Object.assign(stats, res)
  } catch (error: any) {
    ElMessage.error('加载统计失败: ' + error.message)
  }
}

const loadDetections = async () => {
  try {
    const params: any = {
      page: detectionPagination.page,
      page_size: detectionPagination.page_size,
      ...detectionFilter,
    }
    const queryString = new URLSearchParams(params).toString()
    const url = '/api/admin/prompt-injection/detections' + (queryString ? '?' + queryString : '')
    const res = await req<any>('GET', url)
    detections.value = res.detections || []
    detectionPagination.total = res.total || 0
  } catch (error: any) {
    ElMessage.error('加载检测日志失败: ' + error.message)
  }
}

// ==================== 辅助函数 ====================
const getCategoryType = (category: string) => {
  const types: Record<string, string> = {
    role_hijack: 'danger',
    instruction_override: 'danger',
    instruction_leak: 'warning',
    jailbreak: 'danger',
    encoding_bypass: 'info',
    injection_marker: 'danger',
    multi_turn_attack: 'warning',
    resource_exhaustion: 'info',
    data_exfiltration: 'danger',
    social_engineering: 'warning',
    prompt_leaking: 'warning',
    payload_smuggling: 'info',
    unicode_obfuscation: 'info',
    context_manipulation: 'warning',
    tool_abuse: 'danger',
    // 兼容旧分类
    dan: 'danger',
    bypass: 'info',
  }
  return types[category] || ''
}

const getCategoryLabel = (category: string) => {
  const labels: Record<string, string> = {
    role_hijack: '角色劫持',
    instruction_override: '指令覆盖',
    instruction_leak: '指令泄漏',
    jailbreak: '越狱攻击',
    encoding_bypass: '编码绕过',
    injection_marker: '注入标记',
    multi_turn_attack: '多轮攻击',
    resource_exhaustion: '资源耗尽',
    data_exfiltration: '数据窃取',
    social_engineering: '社会工程',
    prompt_leaking: '提示词泄漏',
    payload_smuggling: 'Payload走私',
    unicode_obfuscation: 'Unicode混淆',
    context_manipulation: '上下文操纵',
    tool_abuse: '工具滥用',
    // 兼容旧分类
    dan: 'DAN越狱',
    bypass: '绕过技术',
  }
  return labels[category] || category
}

const getSeverityType = (severity: number) => {
  if (severity >= 9) return 'danger'
  if (severity >= 7) return 'warning'
  if (severity >= 5) return 'info'
  return 'success'
}

const getRiskLevelType = (level: string) => {
  const types: Record<string, string> = {
    critical: 'danger',
    high: 'danger',
    medium: 'warning',
    low: 'info',
  }
  return types[level] || ''
}

const getRiskLevelLabel = (level: string) => {
  const labels: Record<string, string> = {
    critical: '严重',
    high: '高',
    medium: '中',
    low: '低',
  }
  return labels[level] || level
}

const getActionType = (action: string) => {
  const types: Record<string, string> = {
    block: 'danger',
    reject: 'danger',
    terminate: 'danger',
    approve: 'warning',
    replace: 'warning',
    redact: 'warning',
    remove: 'warning',
    sanitize: 'warning',
    warn: 'info',
    log: 'success',
    pass: 'success',
  }
  return types[action] || ''
}

const getActionLabel = (action: string) => {
  const labels: Record<string, string> = {
    block: '阻断',
    reject: '拒绝',
    terminate: '终止',
    approve: '审批',
    replace: '替换',
    redact: '脱敏',
    remove: '移除',
    sanitize: '清洗',
    warn: '警告',
    log: '记录',
    pass: '放行',
  }
  return labels[action] || action
}

const getSeverityTagType = (level: string) => {
  const types: Record<string, string> = {
    critical: 'danger',
    high: 'warning',
    medium: '',
    low: 'info',
  }
  return types[level] || ''
}

const getSeverityLabel = (level: string) => {
  const labels: Record<string, string> = {
    critical: '严重 (Critical)',
    high: '高 (High)',
    medium: '中 (Medium)',
    low: '低 (Low)',
  }
  return labels[level] || level
}

const getApprovalStatusType = (status: string) => {
  const types: Record<string, string> = {
    pending: 'warning',
    approved: 'success',
    rejected: 'danger',
    expired: 'info',
  }
  return types[status] || ''
}

const getApprovalStatusLabel = (status: string) => {
  const labels: Record<string, string> = {
    pending: '待处理',
    approved: '已批准',
    rejected: '已拒绝',
    expired: '已过期',
  }
  return labels[status] || status
}

// ==================== 初始化 ====================
onMounted(() => {
  loadPolicy()
  refreshStats()
  loadRules()
  loadDetections()
  loadEngines()
  loadSeverityMatrix()
  loadCanaryTokens()
})
</script>

<style scoped lang="scss">
.prompt-injection-settings {
  padding: 20px;
}

.settings-header {
  margin-bottom: 24px;

  h2 {
    margin: 0 0 8px 0;
    font-size: 24px;
  }

  .description {
    color: var(--muted, #8b949e);
    margin: 0;
  }
}

.settings-tabs {
  :deep(.el-tabs__header) {
    margin-bottom: 20px;
  }
}

.section-card {
  margin-bottom: 20px;

  .card-header {
    display: flex;
    justify-content: space-between;
    align-items: center;
  }
}

.help-text {
  margin-left: 12px;
  color: var(--muted, #8b949e);
  font-size: 12px;
}

.help-text-small {
  color: var(--muted, #8b949e);
  font-size: 11px;
  margin-top: 4px;
}

.info-icon {
  margin-left: 4px;
  color: var(--muted, #8b949e);
  cursor: help;
}

.whitelist-tags {
  margin-top: 8px;
}

.category-filter {
  margin-bottom: 16px;
  padding-bottom: 12px;
  border-bottom: 1px solid var(--border, #30363d);
}

.risk-distribution {
  h4 {
    margin: 0 0 16px 0;
  }

  .risk-item {
    padding: 16px;
    border-radius: 4px;
    text-align: center;

    .risk-label {
      font-size: 14px;
      margin-bottom: 8px;
    }

    .risk-count {
      font-size: 24px;
      font-weight: bold;
    }

    &.risk-critical {
      background: rgba(248,81,73,.12);
      color: #f85149;
    }

    &.risk-high {
      background: rgba(248,81,73,.12);
      color: #f85149;
    }

    &.risk-medium {
      background: rgba(210,153,34,.12);
      color: #d29922;
    }

    &.risk-low {
      background: rgba(99,102,241,.12);
      color: #818cf8;
    }
  }
}

.filter-form {
  margin-bottom: 16px;
}

.approval-badge {
  margin-left: 8px;
}

.flow-description {
  :deep(.el-descriptions__label) {
    width: 120px;
    font-weight: bold;
  }
}

.quick-link-card {
  cursor: pointer;
  text-align: center;
  padding: 20px;
  transition: all 0.3s;

  &:hover {
    transform: translateY(-4px);
    box-shadow: 0 4px 12px rgba(0, 0, 0, 0.15);
  }

  h3 {
    margin: 12px 0 8px;
    font-size: 16px;
  }

  p {
    color: var(--muted, #8b949e);
    font-size: 14px;
    margin: 0;
  }
}
</style>
