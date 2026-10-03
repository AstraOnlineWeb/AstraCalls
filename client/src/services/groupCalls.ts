import { apiGet, apiPost } from "@/lib/api";

export type GroupInfo = {
  jid: string;
  name: string;
  participants?: unknown[];
};

// listGroups lista os grupos da sessão (para escolher onde ligar em grupo).
export const listGroups = (sid: string) =>
  apiGet<{ groups?: GroupInfo[] } | GroupInfo[]>(`/api/sessions/${sid}/groups`).then((r) =>
    Array.isArray(r) ? r : (r.groups ?? []),
  );

// startGroupCall inicia uma chamada em grupo (toca para os membros do grupo).
export const startGroupCall = (sid: string, groupJid: string, video = false) =>
  apiPost<{ callId: string; targets: number }>(`/api/sessions/${sid}/calls/group`, { groupJid, video });

// endGroupCall encerra a chamada em grupo ativa.
export const endGroupCall = (sid: string) =>
  apiPost<unknown>(`/api/sessions/${sid}/calls/group/end`, {});

export type RosterEntry = { phone: string; name: string };

// getGroupRoster devolve o roster (número do LID -> telefone + nome) da chamada ativa,
// p/ rotular os vídeos com número+nome em vez do @lid.
export const getGroupRoster = (sid: string) =>
  apiGet<{ roster: Record<string, RosterEntry> }>(`/api/sessions/${sid}/calls/group/roster`).then(
    (r) => r.roster ?? {},
  );
