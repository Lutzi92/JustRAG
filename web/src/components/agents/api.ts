import { API_BASE_URL, authFetch } from '../../api';

export interface AgentRecord {
  id: string;
  userId: string;
  name: string;
  description: string;
  icon: string;
  systemPrompt: string;
  chatModel: string;
  toolNames: string[];
  config: Record<string, string>;
  isEnabled: boolean;
  createdAt: string;
  updatedAt: string;
}

export interface TeamRecord {
  id: string;
  userId: string;
  name: string;
  description: string;
  icon: string;
  maxAgentsPerTurn: number;
  isEnabled: boolean;
  memberIds: string[];
  createdAt: string;
  updatedAt: string;
}

export type AgentUpsert = Omit<AgentRecord, 'id' | 'userId' | 'createdAt' | 'updatedAt'>;
export type TeamUpsert = Omit<TeamRecord, 'id' | 'userId' | 'createdAt' | 'updatedAt'>;

// Mirrors siteconfig.KBConfigField (GET /api/agents/registry).
export interface AgentConfigField {
  key: string;
  type: 'bool' | 'int' | 'float' | 'string' | 'enum';
  group: string;
  label: string;
  help: string;
  min?: number;
  max?: number;
  enum?: string[];
}

export interface AgentRegistry {
  fields: AgentConfigField[];
  tools: string[];
}

export interface KbAgentOption {
  id: string;
  name: string;
  description: string;
  icon: string;
  isDefault: boolean;
  memberCount?: number;
}

export interface KbAgents {
  agents: KbAgentOption[];
  teams: KbAgentOption[];
}

async function json<T>(res: Response, what: string): Promise<T> {
  if (!res.ok) {
    const body = await res.json().catch(() => ({} as { error?: string }));
    throw new Error((body as { error?: string }).error || `${what}: ${res.status}`);
  }
  return res.json() as Promise<T>;
}

const base = `${API_BASE_URL}/api`;
const jsonInit = (method: string, body: unknown): RequestInit => ({
  method,
  headers: { 'Content-Type': 'application/json' },
  body: JSON.stringify(body),
});

export const listAgents = () =>
  authFetch(`${base}/agents`).then(r => json<AgentRecord[]>(r, 'list agents'));
export const createAgent = (a: AgentUpsert) =>
  authFetch(`${base}/agents`, jsonInit('POST', a)).then(r => json<AgentRecord>(r, 'create agent'));
export const updateAgent = (id: string, a: AgentUpsert) =>
  authFetch(`${base}/agents/${id}`, jsonInit('PUT', a)).then(r => json<AgentRecord>(r, 'update agent'));
export const deleteAgent = async (id: string) => {
  const r = await authFetch(`${base}/agents/${id}`, { method: 'DELETE' });
  if (!r.ok) throw new Error(`delete agent: ${r.status}`);
};

export const listTeams = () =>
  authFetch(`${base}/agent-teams`).then(r => json<TeamRecord[]>(r, 'list teams'));
export const createTeam = (t: TeamUpsert) =>
  authFetch(`${base}/agent-teams`, jsonInit('POST', t)).then(r => json<TeamRecord>(r, 'create team'));
export const updateTeam = (id: string, t: TeamUpsert) =>
  authFetch(`${base}/agent-teams/${id}`, jsonInit('PUT', t)).then(r => json<TeamRecord>(r, 'update team'));
export const deleteTeam = async (id: string) => {
  const r = await authFetch(`${base}/agent-teams/${id}`, { method: 'DELETE' });
  if (!r.ok) throw new Error(`delete team: ${r.status}`);
};

export const fetchAgentRegistry = () =>
  authFetch(`${base}/agents/registry`).then(r => json<AgentRegistry>(r, 'agent registry'));
export const fetchKbAgents = (kbId: string) =>
  authFetch(`${base}/kb/${kbId}/agents`).then(r => json<KbAgents>(r, 'kb agents'));

export const attachAgentToKb = (kbId: string, agentId: string, isDefault = false) =>
  authFetch(`${base}/kb/${kbId}/agents/${agentId}`, jsonInit('PUT', { isDefault }))
    .then(r => json<{ success: boolean }>(r, 'attach agent'));
export const detachAgentFromKb = async (kbId: string, agentId: string) => {
  const r = await authFetch(`${base}/kb/${kbId}/agents/${agentId}`, { method: 'DELETE' });
  if (!r.ok) throw new Error(`detach agent: ${r.status}`);
};
export const attachTeamToKb = (kbId: string, teamId: string, isDefault = false) =>
  authFetch(`${base}/kb/${kbId}/teams/${teamId}`, jsonInit('PUT', { isDefault }))
    .then(r => json<{ success: boolean }>(r, 'attach team'));
export const detachTeamFromKb = async (kbId: string, teamId: string) => {
  const r = await authFetch(`${base}/kb/${kbId}/teams/${teamId}`, { method: 'DELETE' });
  if (!r.ok) throw new Error(`detach team: ${r.status}`);
};
