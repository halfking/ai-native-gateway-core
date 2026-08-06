# 会话管理前端实现指南

## 概述

本文档提供会话管理功能的前端实现参考代码（React + TypeScript）。

---

## 一、类型定义

```typescript
// types/session.ts

export interface SessionListItem {
  session_key: string;
  tenant_id: string;
  title: string;
  summary?: string;
  project_id?: string;
  task_id?: string;
  user_tags: string[];
  user_intent?: string;
  status: 'active' | 'completed' | 'abandoned';
  first_request_at: string;
  last_request_at: string;
  duration_seconds: number;
  request_count: number;
  success_count: number;
  error_count: number;
  total_cost_usd: number;
  total_tokens: number;
  models_used: string[];
  primary_model?: string;
  last_summarized_at?: string;
}

export interface SessionListResponse {
  sessions: SessionListItem[];
  total: number;
  page: number;
  page_size: number;
}

export interface SessionFilters {
  tenant_id?: string;
  project_id?: string;
  task_id?: string;
  tags?: string[];
  status?: string;
  search?: string;
  from_date?: string;
  to_date?: string;
  sort_by?: string;
  sort_order?: 'asc' | 'desc';
  page?: number;
  page_size?: number;
}

export interface TaskFlowResponse {
  task_id: string;
  project_id?: string;
  summary: {
    session_count: number;
    total_cost_usd: number;
    total_tokens: number;
    total_requests: number;
    started_at: string;
    last_activity_at: string;
    status: string;
  };
  sessions: Array<{
    session_key: string;
    title: string;
    summary?: string;
    order: number;
    started_at: string;
    cost_usd: number;
    tokens: number;
  }>;
  daily_costs: Array<{
    date: string;
    session_count: number;
    total_cost_usd: number;
    total_tokens: number;
  }>;
}
```

---

## 二、API 客户端

```typescript
// api/sessionManagement.ts

import axios from 'axios';
import { SessionListResponse, SessionFilters, TaskFlowResponse } from '../types/session';

const API_BASE = process.env.REACT_APP_API_BASE || 'http://localhost:8781';

export const sessionManagementAPI = {
  // 获取会话列表
  async getSessionList(filters: SessionFilters): Promise<SessionListResponse> {
    const params = new URLSearchParams();
    
    if (filters.tenant_id) params.append('tenant_id', filters.tenant_id);
    if (filters.project_id) params.append('project_id', filters.project_id);
    if (filters.task_id) params.append('task_id', filters.task_id);
    if (filters.tags?.length) params.append('tags', filters.tags.join(','));
    if (filters.status) params.append('status', filters.status);
    if (filters.search) params.append('search', filters.search);
    if (filters.from_date) params.append('from_date', filters.from_date);
    if (filters.to_date) params.append('to_date', filters.to_date);
    if (filters.sort_by) params.append('sort_by', filters.sort_by);
    if (filters.sort_order) params.append('sort_order', filters.sort_order);
    if (filters.page) params.append('page', filters.page.toString());
    if (filters.page_size) params.append('page_size', filters.page_size.toString());
    
    const response = await axios.get(`${API_BASE}/api/sessions/list?${params}`);
    return response.data;
  },
  
  // 获取会话详情
  async getSessionDetail(sessionKey: string) {
    const response = await axios.get(`${API_BASE}/api/sessions/detail/${sessionKey}`);
    return response.data;
  },
  
  // 更新会话
  async updateSession(sessionKey: string, data: {
    project_id?: string;
    task_id?: string;
    user_tags?: string[];
    status?: string;
  }) {
    const response = await axios.patch(`${API_BASE}/api/sessions/update/${sessionKey}`, data);
    return response.data;
  },
  
  // 获取任务脉络
  async getTaskFlow(taskId: string): Promise<TaskFlowResponse> {
    const response = await axios.get(`${API_BASE}/api/sessions/task-flow/${taskId}`);
    return response.data;
  },
  
  // 获取项目成本
  async getProjectCosts(projectId: string) {
    const response = await axios.get(`${API_BASE}/api/sessions/project-costs/${projectId}`);
    return response.data;
  }
};
```

---

## 三、会话列表页面

```typescript
// pages/SessionList.tsx

import React, { useState, useEffect } from 'react';
import { sessionManagementAPI } from '../api/sessionManagement';
import { SessionListItem, SessionFilters } from '../types/session';
import { 
  Box, 
  TextField, 
  Select, 
  MenuItem, 
  Button, 
  Table,
  TableHead,
  TableBody,
  TableRow,
  TableCell,
  Chip,
  Pagination,
  CircularProgress
} from '@mui/material';

export const SessionListPage: React.FC = () => {
  const [sessions, setSessions] = useState<SessionListItem[]>([]);
  const [total, setTotal] = useState(0);
  const [loading, setLoading] = useState(false);
  const [filters, setFilters] = useState<SessionFilters>({
    page: 1,
    page_size: 20,
    sort_by: 'last_request_at',
    sort_order: 'desc'
  });
  
  // 加载会话列表
  useEffect(() => {
    loadSessions();
  }, [filters]);
  
  const loadSessions = async () => {
    setLoading(true);
    try {
      const response = await sessionManagementAPI.getSessionList(filters);
      setSessions(response.sessions);
      setTotal(response.total);
    } catch (error) {
      console.error('Failed to load sessions:', error);
    } finally {
      setLoading(false);
    }
  };
  
  const handleFilterChange = (key: keyof SessionFilters, value: any) => {
    setFilters(prev => ({ ...prev, [key]: value, page: 1 }));
  };
  
  const handlePageChange = (event: React.ChangeEvent<unknown>, value: number) => {
    setFilters(prev => ({ ...prev, page: value }));
  };
  
  const formatCost = (cost: number) => `$${cost.toFixed(2)}`;
  const formatDuration = (seconds: number) => {
    const hours = Math.floor(seconds / 3600);
    const minutes = Math.floor((seconds % 3600) / 60);
    return `${hours}h ${minutes}m`;
  };
  
  return (
    <Box p={3}>
      <h1>会话管理</h1>
      
      {/* 过滤器 */}
      <Box display="flex" gap={2} mb={3}>
        <TextField
          label="搜索"
          placeholder="标题或总结"
          value={filters.search || ''}
          onChange={(e) => handleFilterChange('search', e.target.value)}
          style={{ width: 300 }}
        />
        
        <TextField
          label="任务ID"
          value={filters.task_id || ''}
          onChange={(e) => handleFilterChange('task_id', e.target.value)}
          style={{ width: 200 }}
        />
        
        <Select
          label="状态"
          value={filters.status || 'all'}
          onChange={(e) => handleFilterChange('status', e.target.value === 'all' ? undefined : e.target.value)}
          style={{ width: 150 }}
        >
          <MenuItem value="all">全部</MenuItem>
          <MenuItem value="active">进行中</MenuItem>
          <MenuItem value="completed">已完成</MenuItem>
          <MenuItem value="abandoned">已放弃</MenuItem>
        </Select>
        
        <Button variant="contained" onClick={loadSessions}>
          搜索
        </Button>
      </Box>
      
      {/* 会话列表 */}
      {loading ? (
        <Box display="flex" justifyContent="center" p={5}>
          <CircularProgress />
        </Box>
      ) : (
        <>
          <Table>
            <TableHead>
              <TableRow>
                <TableCell>标题</TableCell>
                <TableCell>项目/任务</TableCell>
                <TableCell>标签</TableCell>
                <TableCell>状态</TableCell>
                <TableCell>轮次</TableCell>
                <TableCell>成本</TableCell>
                <TableCell>时长</TableCell>
                <TableCell>最后活动</TableCell>
                <TableCell>操作</TableCell>
              </TableRow>
            </TableHead>
            <TableBody>
              {sessions.map((session) => (
                <TableRow key={session.session_key} hover>
                  <TableCell>
                    <strong>{session.title}</strong>
                    {session.summary && (
                      <div style={{ fontSize: '0.85em', color: '#666', marginTop: 4 }}>
                        {session.summary.substring(0, 100)}...
                      </div>
                    )}
                  </TableCell>
                  <TableCell>
                    {session.project_id && <div>项目: {session.project_id}</div>}
                    {session.task_id && <div>任务: {session.task_id}</div>}
                  </TableCell>
                  <TableCell>
                    {session.user_tags.map((tag) => (
                      <Chip key={tag} label={tag} size="small" style={{ marginRight: 4 }} />
                    ))}
                  </TableCell>
                  <TableCell>
                    <Chip 
                      label={session.status} 
                      color={
                        session.status === 'completed' ? 'success' :
                        session.status === 'active' ? 'primary' : 'default'
                      }
                      size="small"
                    />
                  </TableCell>
                  <TableCell>{session.request_count}</TableCell>
                  <TableCell>{formatCost(session.total_cost_usd)}</TableCell>
                  <TableCell>{formatDuration(session.duration_seconds)}</TableCell>
                  <TableCell>
                    {new Date(session.last_request_at).toLocaleString()}
                  </TableCell>
                  <TableCell>
                    <Button 
                      size="small" 
                      onClick={() => window.location.href = `/sessions/${session.session_key}`}
                    >
                      详情
                    </Button>
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
          
          {/* 分页 */}
          <Box display="flex" justifyContent="center" mt={3}>
            <Pagination
              count={Math.ceil(total / (filters.page_size || 20))}
              page={filters.page || 1}
              onChange={handlePageChange}
              color="primary"
            />
          </Box>
          
          <Box mt={2} textAlign="center" color="#666">
            共 {total} 个会话
          </Box>
        </>
      )}
    </Box>
  );
};
```

---

## 四、任务脉络页面

```typescript
// pages/TaskFlow.tsx

import React, { useState, useEffect } from 'react';
import { useParams } from 'react-router-dom';
import { sessionManagementAPI } from '../api/sessionManagement';
import { TaskFlowResponse } from '../types/session';
import { 
  Box, 
  Card, 
  CardContent, 
  Typography, 
  Timeline,
  TimelineItem,
  TimelineSeparator,
  TimelineConnector,
  TimelineContent,
  TimelineDot,
  Chip,
  CircularProgress
} from '@mui/material';

export const TaskFlowPage: React.FC = () => {
  const { taskId } = useParams<{ taskId: string }>();
  const [data, setData] = useState<TaskFlowResponse | null>(null);
  const [loading, setLoading] = useState(true);
  
  useEffect(() => {
    loadTaskFlow();
  }, [taskId]);
  
  const loadTaskFlow = async () => {
    if (!taskId) return;
    
    setLoading(true);
    try {
      const response = await sessionManagementAPI.getTaskFlow(taskId);
      setData(response);
    } catch (error) {
      console.error('Failed to load task flow:', error);
    } finally {
      setLoading(false);
    }
  };
  
  if (loading) {
    return (
      <Box display="flex" justifyContent="center" p={5}>
        <CircularProgress />
      </Box>
    );
  }
  
  if (!data) {
    return <Box p={3}>任务未找到</Box>;
  }
  
  return (
    <Box p={3}>
      <h1>任务脉络: {data.task_id}</h1>
      
      {/* 任务汇总 */}
      <Card style={{ marginBottom: 24 }}>
        <CardContent>
          <Typography variant="h6" gutterBottom>
            任务统计
          </Typography>
          <Box display="flex" gap={4}>
            <Box>
              <Typography variant="body2" color="textSecondary">
                会话数量
              </Typography>
              <Typography variant="h5">
                {data.summary.session_count}
              </Typography>
            </Box>
            <Box>
              <Typography variant="body2" color="textSecondary">
                总成本
              </Typography>
              <Typography variant="h5" color="primary">
                ${data.summary.total_cost_usd.toFixed(2)}
              </Typography>
            </Box>
            <Box>
              <Typography variant="body2" color="textSecondary">
                总 Token
              </Typography>
              <Typography variant="h5">
                {data.summary.total_tokens.toLocaleString()}
              </Typography>
            </Box>
            <Box>
              <Typography variant="body2" color="textSecondary">
                状态
              </Typography>
              <Chip 
                label={data.summary.status}
                color={data.summary.status === 'completed' ? 'success' : 'primary'}
              />
            </Box>
          </Box>
        </CardContent>
      </Card>
      
      {/* 会话时间线 */}
      <Card>
        <CardContent>
          <Typography variant="h6" gutterBottom>
            会话流程
          </Typography>
          
          <Timeline>
            {data.sessions.map((session, index) => (
              <TimelineItem key={session.session_key}>
                <TimelineSeparator>
                  <TimelineDot color={index === data.sessions.length - 1 ? 'primary' : 'grey'} />
                  {index < data.sessions.length - 1 && <TimelineConnector />}
                </TimelineSeparator>
                <TimelineContent>
                  <Card variant="outlined" style={{ marginBottom: 16 }}>
                    <CardContent>
                      <Box display="flex" justifyContent="space-between" alignItems="center">
                        <Box>
                          <Typography variant="h6">
                            {session.order}. {session.title}
                          </Typography>
                          <Typography variant="body2" color="textSecondary">
                            {new Date(session.started_at).toLocaleString()}
                          </Typography>
                        </Box>
                        <Box textAlign="right">
                          <Typography variant="body1" color="primary">
                            ${session.cost_usd.toFixed(2)}
                          </Typography>
                          <Typography variant="body2" color="textSecondary">
                            {session.tokens.toLocaleString()} tokens
                          </Typography>
                        </Box>
                      </Box>
                      
                      {session.summary && (
                        <Typography 
                          variant="body2" 
                          style={{ marginTop: 8, color: '#666' }}
                        >
                          {session.summary}
                        </Typography>
                      )}
                      
                      <Button 
                        size="small" 
                        style={{ marginTop: 8 }}
                        onClick={() => window.location.href = `/sessions/${session.session_key}`}
                      >
                        查看详情
                      </Button>
                    </CardContent>
                  </Card>
                </TimelineContent>
              </TimelineItem>
            ))}
          </Timeline>
        </CardContent>
      </Card>
      
      {/* 每日成本图表（可以使用 recharts 或其他图表库） */}
      {data.daily_costs.length > 0 && (
        <Card style={{ marginTop: 24 }}>
          <CardContent>
            <Typography variant="h6" gutterBottom>
              每日成本趋势
            </Typography>
            {/* 这里可以添加图表组件 */}
            <Typography variant="body2" color="textSecondary">
              （图表实现需要引入图表库如 recharts 或 chart.js）
            </Typography>
          </CardContent>
        </Card>
      )}
    </Box>
  );
};
```

---

## 五、路由配置

```typescript
// App.tsx

import React from 'react';
import { BrowserRouter as Router, Route, Switch } from 'react-router-dom';
import { SessionListPage } from './pages/SessionList';
import { TaskFlowPage } from './pages/TaskFlow';
import { SessionDetailPage } from './pages/SessionDetail';

function App() {
  return (
    <Router>
      <Switch>
        <Route exact path="/sessions" component={SessionListPage} />
        <Route path="/sessions/:sessionKey" component={SessionDetailPage} />
        <Route path="/tasks/:taskId/flow" component={TaskFlowPage} />
      </Switch>
    </Router>
  );
}

export default App;
```

---

## 六、样式建议

```css
/* sessions.css */

.session-card {
  border: 1px solid #e0e0e0;
  border-radius: 8px;
  padding: 16px;
  margin-bottom: 12px;
  transition: box-shadow 0.2s;
}

.session-card:hover {
  box-shadow: 0 2px 8px rgba(0,0,0,0.1);
  cursor: pointer;
}

.session-title {
  font-size: 16px;
  font-weight: 600;
  margin-bottom: 8px;
}

.session-summary {
  font-size: 14px;
  color: #666;
  margin-bottom: 12px;
  line-height: 1.5;
}

.session-meta {
  display: flex;
  gap: 16px;
  font-size: 13px;
  color: #888;
}

.session-tag {
  display: inline-block;
  background: #e3f2fd;
  color: #1976d2;
  padding: 4px 8px;
  border-radius: 4px;
  font-size: 12px;
  margin-right: 6px;
}

.cost-highlight {
  color: #4caf50;
  font-weight: 600;
}
```

---

## 七、实现清单

### 必需功能
- [x] 会话列表页面
- [x] 任务脉络页面
- [ ] 会话详情页面
- [ ] 会话编辑对话框
- [ ] 项目成本页面

### 高级功能
- [ ] 实时搜索（防抖）
- [ ] 导出功能（CSV/Excel）
- [ ] 批量操作（批量打标签）
- [ ] 成本趋势图表
- [ ] 响应式设计（移动端适配）

### UI/UX 优化
- [ ] 加载状态动画
- [ ] 错误提示
- [ ] 空状态提示
- [ ] 筛选器持久化（localStorage）
- [ ] 快捷键支持

---

## 八、开发建议

### 技术栈
- **前端框架**：React 18+ 或 Vue 3+
- **UI 库**：Material-UI, Ant Design, 或 Chakra UI
- **状态管理**：React Query（推荐）或 Redux
- **图表**：Recharts 或 Chart.js
- **HTTP 客户端**：Axios 或 Fetch API

### 最佳实践
1. **使用 React Query**：自动处理加载、缓存、重试
2. **错误边界**：优雅处理组件错误
3. **懒加载**：大数据列表使用虚拟滚动
4. **权限控制**：根据用户角色显示/隐藏功能
5. **国际化**：使用 i18n 支持多语言

---

## 九、测试建议

```typescript
// SessionList.test.tsx

import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { SessionListPage } from './SessionList';
import { sessionManagementAPI } from '../api/sessionManagement';

jest.mock('../api/sessionManagement');

describe('SessionListPage', () => {
  it('should render session list', async () => {
    const mockSessions = {
      sessions: [
        {
          session_key: 'test_1',
          title: 'Test Session',
          // ... other fields
        }
      ],
      total: 1,
      page: 1,
      page_size: 20
    };
    
    (sessionManagementAPI.getSessionList as jest.Mock).mockResolvedValue(mockSessions);
    
    render(<SessionListPage />);
    
    await waitFor(() => {
      expect(screen.getByText('Test Session')).toBeInTheDocument();
    });
  });
  
  it('should filter by search term', async () => {
    // ... test implementation
  });
});
```

---

**文档版本**：v1.0  
**创建日期**：2026-08-06  
**技术栈**：React + TypeScript + Material-UI
