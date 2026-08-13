/**
 * transport.ts — Seleção do transporte de mídia da chamada.
 *
 * Dois transportes existem:
 *   - "webrtc"    (padrão): áudio+vídeo via RTCPeerConnection/UDP. Exige que o
 *                 browser alcance o servidor por UDP (IP público direto).
 *   - "websocket": áudio via WSS/443 (PCM16). Atravessa proxy reverso HTTP
 *                 (Cloudflare, Nginx, firewall que bloqueia UDP). Sem vídeo.
 *
 * A escolha é EXPLÍCITA (não há auto-detecção mágica): o padrão é WebRTC e o
 * WebSocket é opt-in. Ativa-se de três formas, nesta ordem de prioridade:
 *   1. Query param na URL:  ?transport=ws   (ou ?transport=websocket)
 *   2. localStorage:        wacalls.transport = "websocket"
 *   3. padrão:              "webrtc"
 *
 * O motivo de ser explícito: a única forma confiável de saber se o UDP está
 * bloqueado é a chamada WebRTC real falhar em conectar — um "probe" de ICE
 * local sempre acha candidato host e daria falso-positivo de WebRTC. Enquanto
 * um fallback automático baseado em falha real não existe, o operador liga o
 * WebSocket quando sabe que está atrás de proxy.
 */

export type Transport = "webrtc" | "websocket";

const STORAGE_KEY = "wacalls.transport";

function normalize(v: string | null): Transport | null {
  if (!v) return null;
  const s = v.toLowerCase();
  if (s === "ws" || s === "websocket") return "websocket";
  if (s === "webrtc" || s === "rtc") return "webrtc";
  return null;
}

/** Retorna o transporte selecionado (query param > localStorage > "webrtc"). */
export function getTransport(): Transport {
  try {
    const fromQuery = normalize(new URLSearchParams(window.location.search).get("transport"));
    if (fromQuery) return fromQuery;
  } catch {
    /* ambiente sem window.location — ignora */
  }
  try {
    const fromStore = normalize(localStorage.getItem(STORAGE_KEY));
    if (fromStore) return fromStore;
  } catch {
    /* localStorage indisponível — ignora */
  }
  return "webrtc";
}

/** Fixa o transporte para as próximas chamadas (persistido em localStorage). */
export function setTransport(t: Transport): void {
  try {
    localStorage.setItem(STORAGE_KEY, t);
  } catch {
    /* ignore */
  }
}

/** Remove a preferência persistida (volta ao padrão WebRTC). */
export function clearTransport(): void {
  try {
    localStorage.removeItem(STORAGE_KEY);
  } catch {
    /* ignore */
  }
}
