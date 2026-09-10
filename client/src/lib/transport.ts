/**
 * transport.ts — Seleção do transporte de mídia da chamada.
 *
 * Dois transportes existem:
 *   - "webrtc"    (padrão): áudio+vídeo via RTCPeerConnection/UDP. Exige que o
 *                 browser alcance o servidor por UDP (IP público direto).
 *   - "websocket": áudio via WSS/443 (PCM16). Atravessa proxy reverso HTTP
 *                 (Cloudflare, Nginx, firewall que bloqueia UDP). Sem vídeo.
 *
 * Seleção (ordem de prioridade):
 *   1. Query param na URL:  ?transport=ws   (ou ?transport=webrtc)
 *   2. localStorage:        wacalls.transport = "websocket" | "webrtc"
 *   3. padrão:              "auto" (ver getTransportMode)
 *
 * No modo "auto" (padrão) o openAdaptiveCall tenta WebRTC (mantém vídeo quando a
 * rede permite) e, se o ICE NÃO conectar em alguns segundos (proxy/firewall
 * bloqueando UDP), cai automaticamente para o WebSocket (áudio-only) — sem o
 * operador abrir porta nem escolher nada. Forçar ?transport= desliga o fallback.
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

/**
 * Modo de transporte, distinguindo escolha EXPLÍCITA de "auto" (nada escolhido).
 *   - "webrtc" / "websocket": operador forçou via ?transport= ou localStorage.
 *   - "auto" (padrão): sem escolha — o openAdaptiveCall tenta WebRTC e, se o ICE
 *     não conectar, cai automaticamente para WebSocket (áudio-only). Isso faz a
 *     chamada funcionar atrás de proxy/firewall que bloqueia UDP sem o operador
 *     precisar abrir porta nem setar nada.
 */
export type TransportMode = "webrtc" | "websocket" | "auto";

export function getTransportMode(): TransportMode {
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
  // Padrão do servidor (injetado no index.html via WACALLS_DEFAULT_TRANSPORT).
  // Permite fixar o transporte por instância (ex.: "websocket" onde o WebRTC/UDP
  // não fecha) sem o agente precisar de ?transport=. Query e localStorage têm
  // prioridade sobre isso.
  try {
    const fromServer = normalize((window as unknown as { __WACALLS_DEFAULT_TRANSPORT?: string }).__WACALLS_DEFAULT_TRANSPORT ?? null);
    if (fromServer) return fromServer;
  } catch {
    /* ignora */
  }
  return "auto";
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
