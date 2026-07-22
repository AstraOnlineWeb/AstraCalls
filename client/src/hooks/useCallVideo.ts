import { useState } from "react";
import { toast } from "sonner";
import { callVideo } from "@/services/calls";
import { setLocalVideoState, useCalls, type CallVideoState } from "@/stores/calls";
import type { CallSummary } from "@/types/call";

const empty: CallVideoState = {
  peerVideo: false,
  localVideo: false,
  upgradeIncoming: false,
  upgradeOutgoing: false,
};

// useCallVideo concentra as ações de vídeo mid-call de uma chamada: ligar a
// câmera (pede upgrade), desligar (downgrade), e aceitar/recusar um pedido do
// peer. Liga a câmera do navegador via OpenCall.setLocalVideo e sinaliza o
// backend via callVideo.
export const useCallVideo = (call: CallSummary) => {
  const conn = useCalls((s) => s.ownConnections.get(call.callId));
  const state = useCalls((s) => s.videoStates.get(call.callId)) ?? empty;
  const [busy, setBusy] = useState(false);

  const run = async (fn: () => Promise<void>) => {
    if (busy) return;
    setBusy(true);
    try {
      await fn();
    } catch (e) {
      toast.error((e as Error).message || "Falha na negociação de vídeo");
    } finally {
      setBusy(false);
    }
  };

  // Liga a câmera e pede o upgrade para a outra ponta.
  const enable = () =>
    run(async () => {
      if (!conn) return;
      const ok = await conn.setLocalVideo(true);
      if (!ok) {
        toast.error("Câmera/vídeo não suportado neste navegador");
        return;
      }
      setLocalVideoState(call.callId, true);
      await callVideo(call.sessionId, call.callId, "request");
    });

  // Desliga a câmera (downgrade para áudio).
  const disable = () =>
    run(async () => {
      await callVideo(call.sessionId, call.callId, "stop").catch(() => {});
      if (conn) await conn.setLocalVideo(false);
      setLocalVideoState(call.callId, false);
    });

  // Aceita um pedido de upgrade recebido: confirma no backend e liga a câmera.
  const accept = () =>
    run(async () => {
      await callVideo(call.sessionId, call.callId, "accept");
      if (conn) {
        const ok = await conn.setLocalVideo(true);
        setLocalVideoState(call.callId, ok);
      }
    });

  // Recusa o pedido de upgrade recebido.
  const reject = () =>
    run(async () => {
      await callVideo(call.sessionId, call.callId, "reject");
    });

  return { state, busy, enable, disable, accept, reject };
};
