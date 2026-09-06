-- Migration 671: 本地托管模型供应商修复与补全（kind='local'）
--
-- 背景：provider_catalog 早已存在 kind='local' 条目（ollama/mlx/llamacpp/
-- lmstudio/vllm），且 providers 表里已经 seed 了同 code 的本地供应商，但：
--   1. base_url 是未渲染的模板占位符 http://{host}:{port}/v1 —— 完全不可用；
--   2. 5 个本地供应商全部没有凭据（本地服务不校验 API key，但网关链路
--      需要 credentials 行才能路由/discovery/探活）；
--   3. catalog capabilities 为空 —— 缺少 hosting_type / 默认端口 /
--      default_context_window 元数据。
--
-- 本迁移：
--   A. 给 catalog 条目补 capabilities 元数据（hosting_type、default_port、
--      default_context_window、context_known、start_hint）。
--   B. 渲染 providers 表中坏掉的 base_url 占位符为 127.0.0.1 + 默认端口。
--      网关跑在 Docker 里时运营把它改成 host.docker.internal（API 支持覆盖）。
--   C. 不新建 catalog 条目、不动 code（现有前端/文档已引用这些 code）。
--
-- 凭据创建不放在 SQL 里：由 admin.ensureLocalCredential（Go）加密生成占位
-- 密钥，走标准 Fernet/AES 链路。运营在管理台对 local 供应商执行"添加凭据"
-- 时空 key 也会自动落占位值（见 admin/local_provider.go）。
--
-- Rollback:
--   UPDATE provider_catalog SET capabilities='{}'::jsonb WHERE kind='local';
--   UPDATE providers SET base_url='http://{host}:{port}/v1' WHERE kind='local';

-- ── A. catalog 元数据 ────────────────────────────────────────────────
UPDATE provider_catalog SET capabilities = (
  '{"hosting_type":"ollama","default_port":11434,"default_context_window":8192,"context_known":true,"start_hint":"ollama serve"}'::jsonb
) WHERE code = 'ollama' AND kind = 'local';

UPDATE provider_catalog SET capabilities = (
  '{"hosting_type":"mlx-lm","default_port":8080,"default_context_window":8192,"context_known":false,"start_hint":"mlx_lm.server --model <path> --port 8080"}'::jsonb
) WHERE code = 'mlx' AND kind = 'local';

UPDATE provider_catalog SET capabilities = (
  '{"hosting_type":"llamacpp","default_port":8082,"default_context_window":4096,"context_known":true,"start_hint":"llama-server -m <gguf> --port 8082"}'::jsonb
) WHERE code = 'llamacpp' AND kind = 'local';

UPDATE provider_catalog SET capabilities = (
  '{"hosting_type":"lmstudio","default_port":1234,"default_context_window":4096,"context_known":false,"start_hint":"LM Studio GUI → Developer → Start Server"}'::jsonb
) WHERE code = 'lmstudio' AND kind = 'local';

UPDATE provider_catalog SET capabilities = (
  '{"hosting_type":"vllm","default_port":8000,"default_context_window":8192,"context_known":true,"start_hint":"vllm serve <model> --port 8000"}'::jsonb
) WHERE code = 'vllm' AND kind = 'local';

-- ── B. 渲染 providers.base_url 坏占位符 ─────────────────────────────
UPDATE providers p
SET base_url = 'http://127.0.0.1:11434/v1', updated_at = NOW()
WHERE p.kind = 'local' AND p.code = 'ollama'
  AND (p.base_url LIKE '%{host}%' OR p.base_url = '' OR p.base_url IS NULL);

UPDATE providers p
SET base_url = 'http://127.0.0.1:8080/v1', updated_at = NOW()
WHERE p.kind = 'local' AND p.code = 'mlx'
  AND (p.base_url LIKE '%{host}%' OR p.base_url = '' OR p.base_url IS NULL);

UPDATE providers p
SET base_url = 'http://127.0.0.1:8082/v1', updated_at = NOW()
WHERE p.kind = 'local' AND p.code = 'llamacpp'
  AND (p.base_url LIKE '%{host}%' OR p.base_url = '' OR p.base_url IS NULL);

UPDATE providers p
SET base_url = 'http://127.0.0.1:1234/v1', updated_at = NOW()
WHERE p.kind = 'local' AND p.code = 'lmstudio'
  AND (p.base_url LIKE '%{host}%' OR p.base_url = '' OR p.base_url IS NULL);

UPDATE providers p
SET base_url = 'http://127.0.0.1:8000/v1', updated_at = NOW()
WHERE p.kind = 'local' AND p.code = 'vllm'
  AND (p.base_url LIKE '%{host}%' OR p.base_url = '' OR p.base_url IS NULL);
