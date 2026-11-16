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
  value: {
    sanitize: '{n} 处',
    route: '{n} 候选',
    meter: '{n} token',
    injection: '得分 {n}（{d}）',
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
    readout: '{total} 条 · 成功率 {rate}% · 在途 {inFlight}',
    title: '实时统计',
    hint: '统计口径与实时拓扑页一致：近 5 分钟与近 1 小时为数据库直查，当天为日桶（滞后不超过 1 小时）。',
    close: '关闭',
  },
  idle: {
    wait: '已等待 {ms} 毫秒没有新请求',
  },
}
