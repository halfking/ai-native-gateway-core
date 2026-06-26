-- ============================================================================
-- llm-gateway-go 工作类型配置初始化数据
-- ============================================================================

-- ----------------------------------------------------------------------------
-- 工作类型配置
-- ----------------------------------------------------------------------------
INSERT INTO public.work_type_config (key, label, category, l1_task_type, default_profile, tags, prompt_keywords, sort_order, system_prompt)
VALUES
  ('general_chat', '通用对话', '通用', 'chat', 'smart', ARRAY['chat','general'], ARRAY['对话','聊天','问答'], 1, NULL),
  ('reasoning', '逻辑推理', '通用', 'reasoning', 'smart', ARRAY['reasoning','logic'], ARRAY['推理','逻辑','数学','证明'], 2, NULL),
  ('long_doc', '长文档处理', '通用', 'long_context', 'smart', ARRAY['long_context','document'], ARRAY['长文档','全文','摘要','PDF'], 3, NULL),
  ('code_gen', '代码生成', '研发', 'code', 'speed_first', ARRAY['code','programming'], ARRAY['代码','编程','实现','函数'], 4, NULL),
  ('code_review', '代码审查', '研发', 'code', 'smart', ARRAY['code','review'], ARRAY['审查','review','重构','bug'], 5, NULL),
  ('agent_workflow', '多步Agent', '研发', 'agent', 'smart', ARRAY['agent','workflow'], ARRAY['agent','多步','工作流','工具'], 6, NULL),
  ('fn_call', '函数调用', '研发', 'function_call', 'speed_first', ARRAY['function_call','tools'], ARRAY['function','tool','调用','API'], 7, NULL),
  ('copywriting', '文案创作', '营销', 'creative', 'smart', ARRAY['creative','copy'], ARRAY['文案','标题','广告语','营销'], 8, NULL),
  ('social_post', '社媒发帖', '营销', 'creative', 'speed_first', ARRAY['social','post'], ARRAY['发帖','微博','小红书','朋友圈'], 9, NULL),
  ('video_script', '短视频脚本', '营销', 'creative', 'smart', ARRAY['video','script'], ARRAY['脚本','短视频','分镜','口播'], 10, NULL),
  ('brand_strategy', '品牌策略', '营销', 'reasoning', 'smart', ARRAY['brand','strategy'], ARRAY['品牌','策略','定位','竞品'], 11, NULL),
  ('web_scrape', '网页采集', '采集', 'agent', 'cost_first', ARRAY['scrape','crawl'], ARRAY['采集','爬虫','抓取','网页'], 12, NULL),
  ('social_monitor', '自媒体监测', '采集', 'agent', 'cost_first', ARRAY['monitor','social'], ARRAY['监测','舆情','评论','热搜'], 13, NULL),
  ('short_video_collect', '短视频采集', '采集', 'agent', 'cost_first', ARRAY['video','collect'], ARRAY['短视频','下载','采集','抖音'], 14, NULL),
  ('news_digest', '资讯摘要', '采集', 'creative', 'speed_first', ARRAY['news','digest'], ARRAY['资讯','新闻','摘要','日报'], 15, NULL),
  ('competitor_intel', '竞品情报', '采集', 'reasoning', 'smart', ARRAY['competitor','intel'], ARRAY['竞品','情报','对比','市场'], 16, NULL),
  ('image_understand', '图像理解', '多媒体', 'vision', 'smart', ARRAY['vision','image'], ARRAY['图像','识图','OCR','视觉'], 17, NULL),
  ('image_gen_prompt', '生图Prompt', '多媒体', 'creative', 'smart', ARRAY['image','prompt'], ARRAY['生图','prompt','Stable','Midjourney'], 18, NULL),
  ('crm_followup', 'CRM跟进', '企业', 'chat', 'smart', ARRAY['crm','followup'], ARRAY['CRM','跟进','客户','销售'], 19, NULL),
  ('doc_translate', '文档翻译', '企业', 'creative', 'cost_first', ARRAY['translate','document'], ARRAY['翻译','文档','双语','本地化'], 20, NULL),
  ('meeting_summary', '会议纪要', '企业', 'creative', 'speed_first', ARRAY['meeting','summary'], ARRAY['会议','纪要','总结','行动项'], 21, NULL),
  ('compliance_audit', '合规审计', '企业', 'reasoning', 'smart', ARRAY['compliance','audit'], ARRAY['合规','审计','风控','政策'], 22, NULL)
ON CONFLICT (key) DO UPDATE SET
    label = EXCLUDED.label,
    category = EXCLUDED.category,
    l1_task_type = EXCLUDED.l1_task_type,
    default_profile = EXCLUDED.default_profile,
    tags = EXCLUDED.tags,
    prompt_keywords = EXCLUDED.prompt_keywords,
    sort_order = EXCLUDED.sort_order,
    updated_at = NOW();

-- ----------------------------------------------------------------------------
-- 会话标题和总结（特殊工作类型，带system prompt）
-- ----------------------------------------------------------------------------
INSERT INTO public.work_type_config (key, label, category, l1_task_type, default_profile, tags, prompt_keywords, sort_order, system_prompt)
VALUES
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
    '你是会话日志分析助手。请严格输出 JSON，格式如下：{"summary":"一段连贯的中文摘要（80-200字），说明会话目标、关键步骤、最终结果","key_points":["要点1","要点2","要点3"]}要求：- summary 必须是完整句子，涵盖：做了什么、怎么做的、结果如何 - key_points 提取 3-5 个关键事实或决策点，每条 15-40 字 - 不要输出 JSON 以外的任何文本 - 如果语料中包含错误信息，务必在总结中提及'
  )
ON CONFLICT (key) DO UPDATE SET
    label = EXCLUDED.label,
    system_prompt = EXCLUDED.system_prompt,
    updated_at = NOW();

-- ----------------------------------------------------------------------------
-- 工作类型模型路由（会话标题和总结的默认模型配置）
-- ----------------------------------------------------------------------------
INSERT INTO public.work_type_model_route (work_type_key, canonical_name, weight, min_score, enabled)
VALUES
  ('session_title', 'minimax-m2.7', 1.00, 0, true),
  ('session_title', 'glm-5.1', 0.95, 0, true),
  ('session_title', 'minimax-m3', 0.90, 0, true),
  ('session_title', 'deepseek-chat', 0.85, 0, true),
  ('session_summary', 'minimax-m2.7', 1.00, 0, true),
  ('session_summary', 'glm-5.1', 0.95, 0, true),
  ('session_summary', 'minimax-m3', 0.90, 0, true),
  ('session_summary', 'deepseek-chat', 0.85, 0, true)
ON CONFLICT (work_type_key, canonical_name) DO NOTHING;