import { apiGet, apiPost, apiDelete } from "@/lib/api";

export type McpKey = {
  id: string;
  name: string;
  /** Começo da chave, para reconhecer (a chave inteira só aparece na criação). */
  prefix: string;
  readOnly: boolean;
  createdAt: string;
  lastUsedAt?: string;
};

export type McpKeyCreated = {
  key: McpKey;
  /** A chave completa — devolvida uma única vez. */
  secret: string;
  /** Endereço MCP para usar com Authorization: Bearer <secret>. */
  endpoint: string;
  /** URL completa com a chave (clientes que só aceitam URL). */
  url: string;
};

export const listMcpKeys = (sid: string) =>
  apiGet<{ keys: McpKey[]; max: number; endpoint: string }>(`/api/sessions/${sid}/mcp-keys`);

export const createMcpKey = (sid: string, body: { name: string; readOnly: boolean }) =>
  apiPost<McpKeyCreated>(`/api/sessions/${sid}/mcp-keys`, body);

export const deleteMcpKey = (sid: string, id: string) => apiDelete(`/api/sessions/${sid}/mcp-keys/${id}`);
