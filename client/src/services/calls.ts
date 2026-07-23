import { apiPost, apiDelete } from "@/lib/api";
import { getClientId } from "@/lib/client-id";
import { apiUrl, getApiKey } from "@/lib/auth";

export const startCall = (sid: string, phone: string, record: boolean, video: boolean) =>
  apiPost<{ call: { callId: string } }>(`/api/sessions/${sid}/calls`, {
    phone,
    duration_ms: 300_000,
    record,
    video,
  });

export const acceptCall = (sid: string, callId: string) =>
  apiPost<{ call: { callId: string } }>(`/api/sessions/${sid}/calls/${callId}/accept`, {});

export const rejectCall = async (sid: string, callId: string): Promise<void> => {
  const r = await fetch(apiUrl(`/api/sessions/${sid}/calls/${callId}/reject`), {
    method: "POST",
    headers: { "X-Client-Id": getClientId(), "X-API-Key": getApiKey(), "Content-Type": "application/json" },
    body: "{}",
  });
  if (!r.ok) throw new Error(`reject ${r.status}`);
};

export const endCall = (sid: string, callId: string) =>
  apiDelete(`/api/sessions/${sid}/calls/${callId}`);

// Espera (hold): coloca a chamada em espera mantendo o leg vivo. moh=true toca música
// de espera para o interlocutor (padrão no backend). resumeCall tira da espera.
export const holdCall = (sid: string, callId: string, moh = true) =>
  apiPost(`/api/sessions/${sid}/calls/${callId}/hold`, { moh });

export const resumeCall = (sid: string, callId: string) =>
  apiPost(`/api/sessions/${sid}/calls/${callId}/resume`, {});

export type VideoAction = "request" | "accept" | "reject" | "stop";

// Negociação de vídeo mid-call: pedir upgrade, aceitar/recusar um pedido recebido
// ou desligar o próprio vídeo (downgrade).
export const callVideo = (sid: string, callId: string, action: VideoAction) =>
  apiPost(`/api/sessions/${sid}/calls/${callId}/video/${action}`, {});
