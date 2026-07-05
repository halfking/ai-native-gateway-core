-- 015_admin_llm_work_types.sql — Admin 内部 LLM 任务（会话标题 / 会话总结）
-- system_prompt 可经 work_type_config 在管理面调整；model=auto + cost_first 走国内便宜模型。

BEGIN;

ALTER TABLE work_type_config ADD COLUMN IF NOT EXISTS system_prompt TEXT;

INSERT INTO work_type_config (
    key, label, category, l1_task_type, default_profile, tags, prompt_keywords, sort_order, system_prompt
) VALUES
  (
    'session_title',
    '会话标题生成',
    '企业',
    'creative',
    'cost_first',
    ARRAY['session','title','admin','gateway'],
    ARRAY['标题','会话','总结','主题'],
    23,
    '你是会话标题生成助手。根据下方完整多轮会话日志，用中文生成一个简短准确的标题（不超过18字），概括用户目标与会话结果。只输出标题纯文本：不要引号、编号、解释、XML/HTML 标签、thinking/redacted 标记或英文占位符。'
  ),
  (
    'session_summary',
    '会话日志总结',
    '企业',
    'creative',
    'cost_first',
    ARRAY['session','summary','admin','gateway'],
    ARRAY['总结','摘要','会话','日志'],
    24,
    '你是会话日志分析助手。请严格输出 JSON，格式如下：
{"summary":"一段连贯的中文摘要（80-200字），说明会话目标、关键步骤、最终结果","key_points":["要点1","要点2","要点3"]}
要求：
- summary 必须是完整句子，涵盖：做了什么、怎么做的、结果如何
- key_points 提取 3-5 个关键事实或决策点，每条 15-40 字
- 不要输出 JSON 以外的任何文本
- 如果语料中包含错误信息，务必在总结中提及'
  )
ON CONFLICT (key) DO NOTHING;

INSERT INTO work_type_model_route (work_type_key, canonical_name, weight, min_score, enabled)
VALUES
  ('session_title',   'minimax-m2.7', 1.00, 0, TRUE),
  ('session_title',   'glm-5.1',      0.95, 0, TRUE),
  ('session_title',   'minimax-m3',   0.90, 0, TRUE),
  ('session_title',   'deepseek-chat',0.85, 0, TRUE),
  ('session_summary', 'minimax-m2.7', 1.00, 0, TRUE),
  ('session_summary', 'glm-5.1',      0.95, 0, TRUE),
  ('session_summary', 'minimax-m3',   0.90, 0, TRUE),
  ('session_summary', 'deepseek-chat',0.85, 0, TRUE)
ON CONFLICT (work_type_key, canonical_name) DO NOTHING;

COMMIT;
