# W2-Q2 Team Hierarchy - Frontend Integration Guide

## Overview

This document provides complete specifications for implementing the frontend team management UI. The backend API is fully implemented and tested. This guide covers:

1. API Client Usage
2. Required UI Components
3. User Flows
4. State Management
5. Error Handling

## 1. API Client Usage

The TypeScript API client is available at `web/src/api-teams.ts`.

### Available Functions

```typescript
// Team CRUD
createTeam(req: CreateTeamRequest): Promise<Team>
listTeams(): Promise<ListTeamsResponse>
getTeam(teamId: number): Promise<Team>
updateTeam(teamId: number, updates: UpdateTeamRequest): Promise<Team>
deleteTeam(teamId: number): Promise<void>

// Member Management
addTeamMember(request: AddMemberRequest): Promise<TeamMember>
removeTeamMember(request: RemoveMemberRequest): Promise<void>

// Budget Management
getTeamBudget(teamId: number): Promise<TeamBudget | null>
upsertTeamBudget(budget: UpsertBudgetRequest): Promise<TeamBudget>
getTeamBudgetStats(teamId: number): Promise<TeamBudgetStats>

// Utility Functions
isBudgetOverLimit(stats: TeamBudgetStats): boolean
getRemainingCredits(stats: TeamBudgetStats): number
getBudgetUsagePercentage(stats: TeamBudgetStats): number
```

### Example Usage

```typescript
import { createTeam, listTeams, addTeamMember } from '@/api-teams'

// Create a team
const team = await createTeam({
  name: 'engineering',
  display_name: 'Engineering Team',
  description: 'Development team'
})

// List teams
const { teams } = await listTeams()

// Add a member
const member = await addTeamMember({
  team_id: team.id,
  user_id: 123,
  role: 'admin'
})
```

## 2. Required UI Components

### 2.1 TeamListView.vue

**Purpose**: Display all teams owned by the current user

**Features**:
- Table/Card view of teams
- Show team name, display name, member count
- Actions: View, Edit, Delete
- "Create New Team" button

**API Calls**:
- `listTeams()` on mount
- `deleteTeam(id)` on delete action

**UI Elements**:
```
+------------------------------------------+
| Teams                    [+ Create Team] |
+------------------------------------------+
| Name         | Members | Budget | Actions|
|--------------|---------|--------|--------|
| Engineering  |    5    | $1000  | View   |
| Marketing    |    3    |   -    | Edit   |
+------------------------------------------+
```

### 2.2 TeamFormModal.vue

**Purpose**: Create or edit a team

**Features**:
- Form fields: name, display_name, description
- Validation: name required, URL-safe characters
- Submit button with loading state
- Cancel button

**API Calls**:
- `createTeam(data)` for new teams
- `updateTeam(id, data)` for edits

**Form Fields**:
```
Team Name: [engineering        ] (required, URL-safe)
Display Name: [Engineering Team  ] (optional)
Description: [Development team    ] (optional)

[Cancel] [Create Team]
```

### 2.3 TeamDetailView.vue

**Purpose**: View and manage a single team

**Features**:
- Team information display
- Member list with roles
- Budget configuration
- Budget usage statistics
- "Add Member" and "Edit Team" actions

**API Calls**:
- `getTeam(id)` on mount
- `getTeamBudget(id)` on mount
- `getTeamBudgetStats(id)` on mount

**Layout**:
```
+------------------------------------------+
| Engineering Team              [Edit]     |
| ID: 1 | Owner: Alice                     |
+------------------------------------------+
| Members (5)                 [+ Add]      |
|  Alice     | Owner  | [Remove]           |
|  Bob       | Admin  | [Remove]           |
|  Charlie   | Member | [Remove]           |
+------------------------------------------+
| Budget                                   |
|  Monthly Limit: $1000                    |
|  Spent: $450 (45%)                       |
|  Status: ██████████░░░░░░░░░░           |
|  Enforcement: Warn                       |
|                           [Edit Budget]  |
+------------------------------------------+
```

### 2.4 AddMemberModal.vue

**Purpose**: Add a user to a team

**Features**:
- User search/select dropdown
- Role selection (owner, admin, member)
- Validation: user not already in team
- Submit button

**API Calls**:
- `addTeamMember({ team_id, user_id, role })`

**Form Fields**:
```
Select User: [Search users...   v]
Role: ( ) Owner (•) Admin ( ) Member

[Cancel] [Add Member]
```

### 2.5 BudgetConfigModal.vue

**Purpose**: Configure team budget

**Features**:
- Monthly limit input (credits or USD)
- Enforcement mode: warn / hard
- Preview of current usage
- Save button

**API Calls**:
- `upsertTeamBudget({ team_id, monthly_limit_credits, enforcement_mode })`

**Form Fields**:
```
Monthly Limit: [10000] credits
Enforcement: (•) Warn ( ) Hard block

Warn: Alert when limit reached, allow overage
Hard: Block requests when limit reached

Current Usage: 4500 / 10000 credits (45%)

[Cancel] [Save Budget]
```

## 3. User Flows

### 3.1 Create Team Flow

1. User clicks "Create Team" button
2. TeamFormModal opens
3. User fills: name="engineering", display_name="Engineering Team"
4. User clicks "Create Team"
5. Call `createTeam(data)`
6. On success: Close modal, refresh team list, show success message
7. On error: Show error message in modal

### 3.2 Add Member Flow

1. User opens team detail page
2. User clicks "Add Member"
3. AddMemberModal opens
4. User selects user from dropdown
5. User selects role (admin/member)
6. User clicks "Add Member"
7. Call `addTeamMember({ team_id, user_id, role })`
8. On success: Close modal, refresh member list
9. On error: Show error (e.g., "user already in team")

### 3.3 Configure Budget Flow

1. User opens team detail page
2. User clicks "Edit Budget"
3. BudgetConfigModal opens with current values
4. User sets limit=10000, mode="hard"
5. User clicks "Save Budget"
6. Call `upsertTeamBudget(data)`
7. On success: Close modal, refresh budget display
8. On error: Show error message

## 4. State Management

### Recommended Pinia Store Structure

```typescript
// stores/teams.ts
import { defineStore } from 'pinia'
import * as api from '@/api-teams'

export const useTeamStore = defineStore('teams', {
  state: () => ({
    teams: [] as api.Team[],
    currentTeam: null as api.Team | null,
    members: [] as api.TeamMember[],
    budget: null as api.TeamBudget | null,
    budgetStats: null as api.TeamBudgetStats | null,
    loading: false,
    error: null as string | null,
  }),

  actions: {
    async fetchTeams() {
      this.loading = true
      try {
        const data = await api.listTeams()
        this.teams = data.teams
      } catch (err) {
        this.error = err.message
      } finally {
        this.loading = false
      }
    },

    async fetchTeamDetails(teamId: number) {
      this.loading = true
      try {
        this.currentTeam = await api.getTeam(teamId)
        this.budget = await api.getTeamBudget(teamId)
        this.budgetStats = await api.getTeamBudgetStats(teamId)
      } catch (err) {
        this.error = err.message
      } finally {
        this.loading = false
      }
    },

    async createTeam(data: api.CreateTeamRequest) {
      const team = await api.createTeam(data)
      this.teams.push(team)
      return team
    },

    async addMember(data: api.AddMemberRequest) {
      const member = await api.addTeamMember(data)
      this.members.push(member)
      return member
    },

    // ... other actions
  },
})
```

## 5. Error Handling

### Common Error Scenarios

| HTTP Status | Scenario | User Message |
|-------------|----------|--------------|
| 400 | Invalid input | "Invalid input: {detail}" |
| 401 | Not authenticated | "Please log in to continue" |
| 403 | Not team owner | "Only team owners can perform this action" |
| 404 | Team not found | "Team not found" |
| 409 | User already in team | "User is already a team member" |
| 500 | Server error | "Server error, please try again" |

### Error Handling Example

```typescript
async function handleCreateTeam(data: CreateTeamRequest) {
  try {
    const team = await createTeam(data)
    showSuccess('Team created successfully')
    router.push(`/teams/${team.id}`)
  } catch (err) {
    if (err.message.includes('400')) {
      showError('Invalid team name or data')
    } else if (err.message.includes('403')) {
      showError('You do not have permission to create teams')
    } else {
      showError('Failed to create team. Please try again.')
    }
  }
}
```

## 6. Routing Configuration

### Recommended Routes

```typescript
// router/index.ts
{
  path: '/teams',
  name: 'teams',
  component: () => import('@/views/TeamListView.vue'),
  meta: { requiresAuth: true }
},
{
  path: '/teams/:id',
  name: 'team-detail',
  component: () => import('@/views/TeamDetailView.vue'),
  meta: { requiresAuth: true }
}
```

## 7. Integration with API Key Creation

When creating an API key, add an optional team selector:

```typescript
// In KeyFormModal.vue
<select v-model="formData.team_id">
  <option :value="null">Personal Key</option>
  <option v-for="team in teams" :key="team.id" :value="team.id">
    {{ team.display_name || team.name }}
  </option>
</select>
```

API call:
```typescript
await createAPIKey({
  ...keyData,
  team_id: formData.team_id // Optional field
})
```

## 8. Testing Checklist

### Manual Testing
- [ ] Create a team
- [ ] List teams
- [ ] View team details
- [ ] Update team information
- [ ] Delete a team
- [ ] Add a member to a team
- [ ] Remove a member from a team
- [ ] Set team budget
- [ ] View budget usage statistics
- [ ] Create API key with team_id

### Edge Cases
- [ ] Try to add duplicate member (should fail)
- [ ] Try to delete team with API keys (check cascade behavior)
- [ ] Try to access team you don't own (should 403)
- [ ] Set budget to 0 (valid, disables budget)
- [ ] Exceed budget in "hard" mode (API requests should be blocked)

## 9. Backend API Reference

All endpoints are under `/api/admin/teams` and require admin authentication.

### Endpoints Summary

| Method | Endpoint | Description |
|--------|----------|-------------|
| POST | `/api/admin/teams` | Create team |
| GET | `/api/admin/teams` | List teams |
| GET | `/api/admin/teams/{id}` | Get team |
| PUT | `/api/admin/teams/{id}` | Update team |
| DELETE | `/api/admin/teams/{id}` | Delete team |
| POST | `/api/admin/teams/members/add` | Add member |
| POST | `/api/admin/teams/members/remove` | Remove member |
| GET | `/api/admin/teams/budget?team_id={id}` | Get budget |
| PUT | `/api/admin/teams/budget` | Set budget |
| GET | `/api/admin/teams/budget/stats?team_id={id}` | Get usage stats |

## 10. Next Steps

1. **Immediate**: Review this document and API client
2. **Phase 1**: Implement TeamListView and TeamFormModal (basic CRUD)
3. **Phase 2**: Implement TeamDetailView with member management
4. **Phase 3**: Implement budget configuration and visualization
5. **Phase 4**: Integrate team selector into API key creation form
6. **Phase 5**: End-to-end testing and polish

## 11. Backend Status

✅ **Complete and Production-Ready**:
- Database migrations (validated on PostgreSQL)
- 10 REST API endpoints
- Role-based permission middleware
- API Key team_id integration
- 26 unit tests (all passing)
- Transaction management
- Error handling

The backend is ready for immediate use. Frontend implementation can proceed without any backend blockers.
