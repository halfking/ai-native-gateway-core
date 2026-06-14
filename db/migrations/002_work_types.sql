-- 002_work_types.sql — Phase 1 work type config (SSOT cache in Gateway PG)
-- Idempotent: IF NOT EXISTS + ON CONFLICT DO NOTHING seeds.

BEGIN;

CREATE TABLE IF NOT EXISTS work_type_config (
    key                 TEXT PRIMARY KEY,
    label               TEXT NOT NULL,
    category            TEXT NOT NULL,
    l1_task_type        TEXT NOT NULL,
    default_profile     TEXT NOT NULL DEFAULT 'smart'
                            CHECK (default_profile IN ('smart', 'speed_first', 'cost_first')),
    tags                TEXT[] NOT NULL DEFAULT '{}',
    prompt_keywords     TEXT[] NOT NULL DEFAULT '{}',
    acc_task_type       TEXT,
    enabled             BOOLEAN NOT NULL DEFAULT TRUE,
    sort_order          INT NOT NULL DEFAULT 0,
    synced_from_acc_at  TIMESTAMPTZ,
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_work_type_config_category ON work_type_config (category, sort_order);
CREATE INDEX IF NOT EXISTS idx_work_type_config_l1 ON work_type_config (l1_task_type);
COMMENT ON TABLE work_type_config IS 'Work type definitions (P1 seed; Phase 3 sync from ACC)';

CREATE TABLE IF NOT EXISTS work_type_model_route (
    id              SERIAL PRIMARY KEY,
    work_type_key   TEXT NOT NULL REFERENCES work_type_config(key) ON DELETE CASCADE,
    canonical_name  TEXT NOT NULL,
    weight          NUMERIC(5,2) NOT NULL DEFAULT 1.0,
    min_score       NUMERIC(8,4) NOT NULL DEFAULT 0,
    enabled         BOOLEAN NOT NULL DEFAULT TRUE,
    UNIQUE (work_type_key, canonical_name)
);
CREATE INDEX IF NOT EXISTS idx_wtmr_work_type ON work_type_model_route (work_type_key);
COMMENT ON TABLE work_type_model_route IS 'Preferred model routes per work type (L1 selection hints)';

-- Optional future column on request_logs (placeholder for work_type tracking)
ALTER TABLE request_logs ADD COLUMN IF NOT EXISTS work_type TEXT;
CREATE INDEX IF NOT EXISTS idx_request_logs_work_type
    ON request_logs (work_type, ts DESC)
    WHERE work_type IS NOT NULL AND work_type <> '';

-- 22 seed work types (Phase 1)
INSERT INTO work_type_config (key, label, category, l1_task_type, default_profile, tags, prompt_keywords, sort_order)
VALUES
  ('general_chat',        '通用对话',   '通用',   'chat',          'smart',       ARRAY['chat','general'],           ARRAY['对话','聊天','问答'],                    1),
  ('reasoning',           '逻辑推理',   '通用',   'reasoning',     'smart',       ARRAY['reasoning','logic'],        ARRAY['推理','逻辑','数学','证明'],              2),
  ('long_doc',            '长文档处理', '通用',   'long_context',  'smart',       ARRAY['long_context','document'],  ARRAY['长文档','全文','摘要','PDF'],             3),
  ('code_gen',            '代码生成',   '研发',   'code',          'speed_first', ARRAY['code','programming'],       ARRAY['代码','编程','实现','函数'],              4),
  ('code_review',         '代码审查',   '研发',   'code',          'smart',       ARRAY['code','review'],            ARRAY['审查','review','重构','bug'],            5),
  ('agent_workflow',      '多步Agent',  '研发',   'agent',         'smart',       ARRAY['agent','workflow'],         ARRAY['agent','多步','工作流','工具'],           6),
  ('fn_call',             '函数调用',   '研发',   'function_call', 'speed_first', ARRAY['function_call','tools'],    ARRAY['function','tool','调用','API'],          7),
  ('copywriting',         '文案创作',   '营销',   'creative',      'smart',       ARRAY['creative','copy'],          ARRAY['文案','标题','广告语','营销'],            8),
  ('social_post',         '社媒发帖',   '营销',   'creative',      'speed_first', ARRAY['social','post'],            ARRAY['发帖','微博','小红书','朋友圈'],          9),
  ('video_script',        '短视频脚本', '营销',   'creative',      'smart',       ARRAY['video','script'],           ARRAY['脚本','短视频','分镜','口播'],           10),
  ('brand_strategy',      '品牌策略',   '营销',   'reasoning',     'smart',       ARRAY['brand','strategy'],         ARRAY['品牌','策略','定位','竞品'],             11),
  ('web_scrape',          '网页采集',   '采集',   'agent',         'cost_first',  ARRAY['scrape','crawl'],           ARRAY['采集','爬虫','抓取','网页'],             12),
  ('social_monitor',      '自媒体监测', '采集',   'agent',         'cost_first',  ARRAY['monitor','social'],         ARRAY['监测','舆情','评论','热搜'],             13),
  ('short_video_collect', '短视频采集', '采集',   'agent',         'cost_first',  ARRAY['video','collect'],          ARRAY['短视频','下载','采集','抖音'],           14),
  ('news_digest',         '资讯摘要',   '采集',   'creative',      'speed_first', ARRAY['news','digest'],            ARRAY['资讯','新闻','摘要','日报'],             15),
  ('competitor_intel',    '竞品情报',   '采集',   'reasoning',     'smart',       ARRAY['competitor','intel'],       ARRAY['竞品','情报','对比','市场'],             16),
  ('image_understand',    '图像理解',   '多媒体', 'vision',        'smart',       ARRAY['vision','image'],           ARRAY['图像','识图','OCR','视觉'],              17),
  ('image_gen_prompt',    '生图Prompt', '多媒体', 'creative',      'smart',       ARRAY['image','prompt'],           ARRAY['生图','prompt','Stable','Midjourney'],   18),
  ('crm_followup',        'CRM跟进',    '企业',   'chat',          'smart',       ARRAY['crm','followup'],           ARRAY['CRM','跟进','客户','销售'],              19),
  ('doc_translate',       '文档翻译',   '企业',   'creative',      'cost_first',  ARRAY['translate','document'],     ARRAY['翻译','文档','双语','本地化'],           20),
  ('meeting_summary',     '会议纪要',   '企业',   'creative',      'speed_first', ARRAY['meeting','summary'],        ARRAY['会议','纪要','总结','行动项'],           21),
  ('compliance_audit',    '合规审计',   '企业',   'reasoning',     'smart',       ARRAY['compliance','audit'],       ARRAY['合规','审计','风控','政策'],             22)
ON CONFLICT (key) DO NOTHING;

COMMIT;
