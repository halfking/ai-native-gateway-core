-- Phase 3 测试工具数据
-- 插入到 tool_registry 表

-- 1. Filesystem 工具集
INSERT INTO tool_registry (tool_id, tenant_id, category, tool_name, tool_definition, enabled, priority, version)
VALUES 
  ('filesystem.read_file', 'default', 'filesystem', 'read_file', 
   '{"type":"function","function":{"name":"read_file","description":"Read content from a file","parameters":{"type":"object","properties":{"path":{"type":"string","description":"File path to read"}},"required":["path"]}}}'::jsonb,
   true, 50, 1),
   
  ('filesystem.write_file', 'default', 'filesystem', 'write_file',
   '{"type":"function","function":{"name":"write_file","description":"Write content to a file","parameters":{"type":"object","properties":{"path":{"type":"string","description":"File path to write"},"content":{"type":"string","description":"Content to write"}},"required":["path","content"]}}}'::jsonb,
   true, 50, 1),
   
  ('filesystem.list_directory', 'default', 'filesystem', 'list_directory',
   '{"type":"function","function":{"name":"list_directory","description":"List files in a directory","parameters":{"type":"object","properties":{"path":{"type":"string","description":"Directory path"}},"required":["path"]}}}'::jsonb,
   true, 50, 1),
   
  ('filesystem.delete_file', 'default', 'filesystem', 'delete_file',
   '{"type":"function","function":{"name":"delete_file","description":"Delete a file","parameters":{"type":"object","properties":{"path":{"type":"string","description":"File path to delete"}},"required":["path"]}}}'::jsonb,
   true, 50, 1),
   
  ('filesystem.create_directory', 'default', 'filesystem', 'create_directory',
   '{"type":"function","function":{"name":"create_directory","description":"Create a new directory","parameters":{"type":"object","properties":{"path":{"type":"string","description":"Directory path to create"}},"required":["path"]}}}'::jsonb,
   true, 50, 1);

-- 2. Network 工具集
INSERT INTO tool_registry (tool_id, tenant_id, category, tool_name, tool_definition, enabled, priority, version)
VALUES 
  ('network.http_get', 'default', 'network', 'http_get',
   '{"type":"function","function":{"name":"http_get","description":"Send HTTP GET request","parameters":{"type":"object","properties":{"url":{"type":"string","description":"URL to request"},"headers":{"type":"object","description":"HTTP headers"}},"required":["url"]}}}'::jsonb,
   true, 50, 1),
   
  ('network.http_post', 'default', 'network', 'http_post',
   '{"type":"function","function":{"name":"http_post","description":"Send HTTP POST request","parameters":{"type":"object","properties":{"url":{"type":"string","description":"URL to request"},"body":{"type":"string","description":"Request body"},"headers":{"type":"object","description":"HTTP headers"}},"required":["url","body"]}}}'::jsonb,
   true, 50, 1);

-- 3. Database 工具集
INSERT INTO tool_registry (tool_id, tenant_id, category, tool_name, tool_definition, enabled, priority, version)
VALUES 
  ('database.query', 'default', 'database', 'query',
   '{"type":"function","function":{"name":"database_query","description":"Execute a database query","parameters":{"type":"object","properties":{"sql":{"type":"string","description":"SQL query to execute"}},"required":["sql"]}}}'::jsonb,
   true, 50, 1),
   
  ('database.insert', 'default', 'database', 'insert',
   '{"type":"function","function":{"name":"database_insert","description":"Insert data into database","parameters":{"type":"object","properties":{"table":{"type":"string","description":"Table name"},"data":{"type":"object","description":"Data to insert"}},"required":["table","data"]}}}'::jsonb,
   true, 50, 1);

-- 4. System 工具集
INSERT INTO tool_registry (tool_id, tenant_id, category, tool_name, tool_definition, enabled, priority, version)
VALUES 
  ('system.execute_command', 'default', 'system', 'execute_command',
   '{"type":"function","function":{"name":"execute_command","description":"Execute a system command","parameters":{"type":"object","properties":{"command":{"type":"string","description":"Command to execute"}},"required":["command"]}}}'::jsonb,
   true, 50, 1),
   
  ('system.get_environment', 'default', 'system', 'get_environment',
   '{"type":"function","function":{"name":"get_environment","description":"Get environment variables","parameters":{"type":"object","properties":{"key":{"type":"string","description":"Environment variable key"}},"required":["key"]}}}'::jsonb,
   true, 50, 1);

-- 验证插入的数据
SELECT 
  category,
  COUNT(*) as tool_count,
  ARRAY_AGG(tool_id ORDER BY tool_id) as tools
FROM tool_registry
WHERE tenant_id = 'default'
GROUP BY category
ORDER BY category;
