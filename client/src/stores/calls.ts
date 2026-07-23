import { create } from "zustand";
import { eventStream, type BrokerEvent } from "@/lib/event-stream";
import { getClientId } from "@/lib/client-id";
import { queryClient, queryKeys } from "@/lib/query";
import type { OpenCall } from "@/lib/webrtc";
import type { CallSummary, IncomingPayload } from "@/types/call";

// Estado de negociação de vídeo por chamada. peerVideo e os flags de upgrade vêm
// do backend (SSE video-state); localVideo é dirigido pela câmera do próprio
// navegador (o backend não sabe se a câmera do operador está ligada de fato).
export type CallVideoState = {
  peerVideo: boolean;
  localVideo: boolean;
  upgradeIncoming: boolean;
  upgradeOutgoing: boolean;
};

const emptyVideo = (): CallVideoState => ({
  peerVideo: false,
  localVideo: false,
  upgradeIncoming: false,
  upgradeOutgoing: false,
});

type State = {
  calls: CallSummary[];
  ownConnections: Map<string, OpenCall>;
  videoStates: Map<string, CallVideoState>;
  incoming: IncomingPayload | null;
};

export const useCalls = create<State>(() => ({
  calls: [],
  ownConnections: new Map(),
  videoStates: new Map(),
  incoming: null,
}));

const patchVideo = (id: string, patch: Partial<CallVideoState>): void => {
  useCalls.setState((s) => {
    const next = new Map(s.videoStates);
    next.set(id, { ...(next.get(id) ?? emptyVideo()), ...patch });
    return { videoStates: next };
  });
};

let wired = false;
export const ensureCallsWired = (): void => {
  if (wired) return;
  wired = true;
  eventStream.on((ev: BrokerEvent) => {
    if (ev.type === "call-list") {
      useCalls.setState({ calls: ev.calls });
    } else if (ev.type === "call-status") {
      useCalls.setState((s) => ({
        calls: s.calls.map((c) =>
          c.callId === ev.id
            ? { ...c, sessionId: ev.sessionId, status: ev.status, peer: ev.peer, startedAt: ev.startedAt, held: ev.held ?? false }
            : c,
        ),
      }));
    } else if (ev.type === "call-ended") {
      useCalls.setState((s) => {
        const conn = s.ownConnections.get(ev.id);
        if (conn) conn.close();
        const next = new Map(s.ownConnections);
        next.delete(ev.id);
        const nextVideo = new Map(s.videoStates);
        nextVideo.delete(ev.id);
        return {
          calls: s.calls.filter((c) => c.callId !== ev.id),
          ownConnections: next,
          videoStates: nextVideo,
          incoming: s.incoming?.callId === ev.id ? null : s.incoming,
        };
      });
      void queryClient.invalidateQueries({ queryKey: queryKeys.history });
    } else if (ev.type === "incoming") {
      useCalls.setState({ incoming: { sessionId: ev.sessionId, callId: ev.id, peer: ev.peer, video: ev.video, offeredAt: ev.offeredAt } });
    } else if (ev.type === "incoming-claimed") {
      useCalls.setState((s) => (s.incoming?.callId === ev.id ? { incoming: null } : s));
    } else if (ev.type === "video-state") {
      // localVideo é do cliente: não sobrescreve com o valor do backend.
      patchVideo(ev.id, {
        peerVideo: ev.peerVideo,
        upgradeIncoming: ev.upgradeIncoming,
        upgradeOutgoing: ev.upgradeOutgoing,
      });
    }
  });
};

export const isMine = (call: CallSummary): boolean => call.owner === getClientId();

export const registerOwnConnection = (id: string, conn: OpenCall, initialVideo = false): void => {
  useCalls.setState((s) => {
    const next = new Map(s.ownConnections);
    next.set(id, conn);
    return { ownConnections: next };
  });
  if (initialVideo) patchVideo(id, { localVideo: true });
};

// setLocalVideoState reflete na UI se a câmera do operador está ligada.
export const setLocalVideoState = (id: string, on: boolean): void => patchVideo(id, { localVideo: on });

export const clearIncoming = (): void => useCalls.setState({ incoming: null });
