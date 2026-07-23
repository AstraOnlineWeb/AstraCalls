import { getApiBase, getApiKey } from "./auth";
import type { CallStatus } from "@/types/call";
import type { SessionInfo, SessionState } from "@/types/session";

type CallListRow = {
  sessionId: string;
  callId: string;
  owner: string | null;
  direction: "outbound" | "inbound";
  peer: string;
  startedAt: number;
  status: CallStatus;
  held?: boolean;
  endedAt?: number;
  endReason?: string;
};

export type BrokerEvent =
  | { type: "session-list"; sessions: SessionInfo[] }
  | { type: "session-qr"; sessionId: string; qr: string }
  | { type: "auth-state"; sessionId: string; paired: boolean; state: SessionState; qr?: string; passkey?: unknown }
  | { type: "call-list"; calls: CallListRow[] }
  | { type: "call-status"; sessionId: string; id: string; owner: string | null; status: CallStatus; peer: string; startedAt: number; held?: boolean }
  | { type: "call-ended"; sessionId: string; id: string; owner: string | null; reason: string; endedAt: number }
  | { type: "incoming"; sessionId: string; id: string; peer: string; video: boolean; offeredAt: number }
  | { type: "incoming-claimed"; sessionId: string; id: string; owner: string }
  | { type: "call-transfer-offer"; sessionId: string; id: string; peer: string; from: string; offeredAt: number }
  | { type: "call-transfer-claimed"; sessionId: string; id: string; owner: string }
  | {
      type: "video-state";
      sessionId: string;
      id: string;
      kind: "upgrade-request" | "state";
      peerVideo: boolean;
      localVideo: boolean;
      upgradeIncoming: boolean;
      upgradeOutgoing: boolean;
    };

type Listener = (ev: BrokerEvent) => void;

class EventStream {
  #es: EventSource | null = null;
  #listeners = new Set<Listener>();

  connect(clientId: string): void {
    if (this.#es) return;
    const url = `${getApiBase()}/api/events?clientId=${encodeURIComponent(clientId)}&apiKey=${encodeURIComponent(getApiKey())}`;
    this.#es = new EventSource(url);
    this.#es.onmessage = (ev) => {
      try {
        const parsed: BrokerEvent = JSON.parse(ev.data);
        for (const l of this.#listeners) l(parsed);
      } catch {}
    };
    this.#es.onerror = () => {};
  }

  on(l: Listener): () => void {
    this.#listeners.add(l);
    return () => this.#listeners.delete(l);
  }

  close(): void {
    this.#es?.close();
    this.#es = null;
  }
}

export const eventStream = new EventStream();
