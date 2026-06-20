-- Phase 2: Meta-tools and tool registry
-- Migration: 021_tool_registry_and_metatools.sql

-- Tool categories table
CREATE TABLE IF NOT EXISTS tool_categories (
    id VARCHAR(64) PRIMARY KEY,
    name VARCHAR(128) NOT NULL,
    description TEXT,
    enabled BOOLEAN DEFAULT true,
    display_order INT DEFAULT 0,
    created_at TIMESTAMPTZ DEFAULT NOW(),
    updated_at TIMESTAMPTZ DEFAULT NOW()
);

-- Tool registry table
CREATE TABLE IF NOT EXISTS tool_registry (
    id SERIAL PRIMARY KEY,
    category VARCHAR(64) NOT NULL REFERENCES tool_categories(id) ON DELETE CASCADE,
    tool_name VARCHAR(128) NOT NULL UNIQUE,
    tool_definition JSONB NOT NULL,
    enabled BOOLEAN DEFAULT true,
    priority INT DEFAULT 0,
    created_at TIMESTAMPTZ DEFAULT NOW(),
    updated_at TIMESTAMPTZ DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_tool_registry_category ON tool_registry(category) WHERE enabled = true;
CREATE INDEX IF NOT EXISTS idx_tool_registry_name ON tool_registry(tool_name) WHERE enabled = true;
CREATE INDEX IF NOT EXISTS idx_tool_categories_order ON tool_categories(display_order) WHERE enabled = true;

-- Insert initial categories
INSERT INTO tool_categories (id, name, description, display_order) VALUES
('filesystem', 'File System Operations', 'Read, write, search files and directories', 1),
('web_search', 'Web Search & Scraping', 'Search the web, fetch URLs, extract content', 2),
('database', 'Database Operations', 'Query and manipulate databases (PostgreSQL, MySQL, Redis)', 3),
('code_execution', 'Code Execution', 'Execute code in various languages', 4),
('network', 'Network Operations', 'HTTP requests, websockets, SSH', 5),
('data_processing', 'Data Processing', 'Transform, analyze, and visualize data', 6),
('ai_ml', 'AI & Machine Learning', 'Run ML models, embeddings, classification', 7)
ON CONFLICT (id) DO NOTHING;

-- Sample tools for filesystem category (for demo)
INSERT INTO tool_registry (category, tool_name, tool_definition, priority) VALUES
('filesystem', 'read_file', '{
  "type": "function",
  "function": {
    "name": "read_file",
    "description": "Read contents of a file",
    "parameters": {
      "type": "object",
      "properties": {
        "path": {
          "type": "string",
          "description": "File path to read"
        }
      },
      "required": ["path"]
    }
  }
}', 100),
('filesystem', 'write_file', '{
  "type": "function",
  "function": {
    "name": "write_file",
    "description": "Write content to a file",
    "parameters": {
      "type": "object",
      "properties": {
        "path": {
          "type": "string",
          "description": "File path to write"
        },
        "content": {
          "type": "string",
          "description": "Content to write"
        }
      },
      "required": ["path", "content"]
    }
  }
}', 90),
('filesystem', 'list_directory', '{
  "type": "function",
  "function": {
    "name": "list_directory",
    "description": "List files and directories",
    "parameters": {
      "type": "object",
      "properties": {
        "path": {
          "type": "string",
          "description": "Directory path"
        }
      },
      "required": ["path"]
    }
  }
}', 80)
ON CONFLICT (tool_name) DO NOTHING;

COMMENT ON TABLE tool_categories IS 'Phase 2: Tool category definitions for layered loading';
COMMENT ON TABLE tool_registry IS 'Phase 2: Centralized tool definition registry';
