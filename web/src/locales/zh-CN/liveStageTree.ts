// liveStageTree.ts — 请求阶段树（live2）文案（2026-11-16）
//
// ★ 措辞纪律：
//   · 「未测量」与「未到达」是**两个不同的结论**，文案必须能让人一眼分开；
//   · 口径不可判定显示「—」，不冒充 0%；
//   · 不出现内部字段名（snake_case 标识符在界面上是噪音，且仓内有门在管）。
export default {
  title: '请求阶段树',
  empty: '当前过滤条件下窗口内没有请求',
  stage: {
    auth: '鉴权/限流',
    sanitize: '脱敏',
    approval: '审批',
    injection: '注入检测',
    route: '路由',
    compress: '压缩',
    firstByte: '首字节',
    restore: '脱敏回填',
    provider: '供应商/令牌池',
    meter: '计量',
    reply: '回复',
  },
  state: {
    pending: '未到达',
    active: '进行中',
    done: '已完成',
    skipped: '不适用',
    failed: '失败',
    warned: '有告警',
    unmeasured: '未测量',
  },
  /** 显示模式（设计第 6 轮：完整 / 紧凑，同一批数据、同一套过滤）。 */
  mode: {
    label: '显示模式',
    full: '完整',
    compact: '紧凑',
  },
  /** 图例（设计第 4 轮）：七态字形 + 两条轨的方向。 */
  legend: {
    sendRail: '发送轨（左→右）',
    recvRail: '接收轨（右→左）',
  },
  /** 列头（设计第 8 轮）：时间 / 客户端 / 阶段。 */
  header: {
    time: '时间',
    client: '客户端',
    flow: '阶段流',
  },
  /** 客户端类型的人话标签（设计里叫「客户端类型徽标」）。 */
  clientType: {
    coding_agent: '编码助手',
    sdk: 'SDK 客户端',
    cli: '命令行',
    app: '桌面应用',
    automation: '自动化',
    bot: '机器人',
    api: 'API 调用',
    web: '网页应用',
  },
  /** 会话轮次（同一会话里的第几轮）。 */
  session: {
    first: '首轮',
    round: '第 {n} 轮',
  },
  /** 时间窗（过滤条没有「全部」档时用得到）。 */
  window: {
    all: '全部时间',
  },
  /** 「有新请求」贴底提示。 */
  newPill: '有新请求',
  value: {
    sanitize: '{n} 处',
    route: '{n} 候选',
    injection: '得分 {n}',
    compress: {
      yes: '已压缩',
      no: '未压缩',
    },
  },
  decision: {
    pass: '放行',
    need_approval: '待人工审批',
    block: '拒绝',
    safe: '安全',
    warn: '命中（仅告警）',
    warnShort: '命中',
    unknown: '未判定',
  },
  return: {
    lane: '回复沿回程线回到客户端',
    label: '回客户端',
  },
  band: {
    status: {
      success: '成功',
      in_progress: '进行中',
      failure: '失败',
    },
    toolsRestore: 'tools 回填 {n}',
    toolsRestored: '已回填',
    queue: '排队 {ms}ms',
    attempts: '尝试 {n} 次',
    finalModel: '最终模型',
    upstreamModel: '上游模型',
    finalModelTip: {
      action: '来源：路由选定的节点动作（解析后的标准模型名）。',
      canonical: '来源：请求帧记录的标准模型名。',
      upstreamRaw: '来源：实际下发上游的模型串。',
      frame: '来源：请求帧的模型字段。',
      none: '未测量：没有可判定的模型来源。',
    },
    modelEntry: {
      auto: 'AUTO 匹配 {model}',
      direct: '请求 {model}',
    },
    chain: {
      auto: 'auto 任务类型 {task} → 匹配 {model}',
      alias: '{from} 解析为 {model}',
      switch: '模型切换 {from} → {to}',
    },
    cache: {
      hit: '缓存命中 {pct}%',
      na: '缓存命中率 —',
      anomaly: '缓存口径异常',
      tip: {
        na: '上游没有上报缓存命中数，不是 0%。',
        unknown: '上游协议未判定，缓存命中率的分母无法确定。',
        anomaly: '命中率超过 100%，说明两个口径混用了。',
        parallel: '口径：缓存读取与输入并列，分母 = 输入 + 缓存读取。',
        subset: '口径：缓存读取已含在输入内，分母 = 输入。',
      },
    },
  },
  credibility: {
    verified: '可信',
    suspected: '疑似掺水',
    diluted: '已掺水',
    unknown: '判定未知',
    unmeasured: '可信度未测量',
    fingerprintMissing: '未提供',
    measuredAtMissing: '未提供',
    tip: {
      unmeasured: '该凭据与上游模型的组合没有可信度测量记录。',
      detail:
        '判定 {verdict}｜身份一致性 {identity}｜能力 {capability}｜稳定性 {consistency}｜基准 {reference}｜系统指纹 {fingerprint}｜测量时间 {measuredAt}',
    },
  },
  stats: {
    open: '展开统计（全页）',
    readout: '{window} · {total} 请求 · {rate} 成功率 · 在途 {inFlight}',
    title: '实时统计',
    hint: '主口径＝顶部过滤条当前可见的请求（内存实时窗口）；折叠区是数据库直查的另一把尺，两者不混算。',
    close: '关闭',
    scope: '{window} · 内存内 {n} 条',
    kpi: {
      title: '总览',
      requests: '请求数',
      successRate: '成功率',
      inFlight: '在途',
      failed: '失败',
      avgLatency: '平均耗时',
      p95Latency: 'P95 耗时',
      totalTokens: 'token 合计',
      inputTokens: '输入 token',
      outputTokens: '输出 token',
      cost: '成本合计',
      cacheRate: '缓存命中率',
      providers: '供应商数',
      models: '模型数',
      clients: '客户端数',
      credentials: '凭据数',
      attempts: '出站尝试',
    },
    trend: {
      title: '周期统计',
      hint: '按分钟分桶；空桶也保留，断流一眼看得见',
      empty: '窗口内没有请求',
    },
    dim: {
      title: '分类统计',
      hint: '与树上的请求同源（当前过滤后的窗口）',
      model: '模型',
      provider: '供应商',
      client: '客户端',
      credential: '凭据',
      status: '状态',
      name: '名称',
      requests: '请求数',
      successRate: '成功率',
      inFlight: '在途',
      tokens: 'token 合计',
      cost: '成本',
      cache: '缓存率',
      avgLatency: '平均耗时',
      empty: '窗口内没有请求',
    },
    notes: {
      title: '口径说明',
      scope: '统计基线＝顶部过滤条当前可见的请求（内存实时窗口），不是数据库全量。',
      coverage: '未上报的耗时 / token / 成本不计入合计（不按 0 计），覆盖率见各指标旁的说明。',
      cost: '成本取请求帧的 cost_usd；费用（对用户计费）与成本（上游采购）单位不同，不可相减。',
      cache: '缓存命中率的分母随上游协议变（OpenAI 包含口径 / Anthropic 并列口径），两种口径同时出现时不给数。',
      db: '下方折叠区是数据库直查口径（近 5 分钟 / 1 小时直查、当天日桶），与本页内存窗口不是同一把尺。',
    },
    db: {
      title: '数据库口径（历史全量直查）',
    },
  },
  idle: {
    wait: '已等待 {ms} 毫秒没有新请求',
  },
  /** 错误来源与错误码（设计第 9 轮：最右边显示错误源头及类型）。 */
  error: {
    column: '错误',
    tag: {
      gateway: 'GW 网关',
      provider: 'UP 供应商',
      other: '其它',
    },
    source: {
      gateway: '网关',
      provider: '供应商',
      other: '其它',
    },
    kindLabel: '错误类型：{kind}',
    codeLabel: '错误码 {code}',
    stageLabel: '失败阶段：{stage}',
    kind: {
      upstream_timeout: '上游超时',
      upstream_rate_limited: '上游限流',
      upstream_quota_permanent: '上游配额永久不足',
      upstream_error: '上游错误',
      upstream_connection_error: '上游连接失败',
      upstream_status_5xx: '上游 5xx 错误',
      eof_without_done: '上游提前结束',
      probe_timeout: '探测超时',
      rate_limit: '限流',
      rate_limit_exceeded: '超过限流阈值',
      key_throttled: '密钥被限流',
      no_route: '没有可用路由',
      model_not_found: '模型不存在',
      client_cancel: '客户端取消',
      prompt_injection_blocked: '提示词注入被拦截',
    },
  },
}
