import { useMutation } from "@tanstack/react-query";
import { toast } from "sonner";
import { openCall } from "@/lib/webrtc";
import { pickupCall, resumeCall } from "@/services/calls";
import { registerOwnConnection, clearTransferOffer } from "@/stores/calls";

// Assume uma chamada transferida: fixa o novo dono no backend (sem re-aceitar no
// WhatsApp), abre a ponte WebRTC (que troca automaticamente a ponte do dono anterior)
// e tira da espera. Diferente do accept: NÃO chama acceptCall.
export const usePickupCall = (micId: string | null) =>
  useMutation({
    mutationFn: async (vars: { sid: string; callId: string }) => {
      const res = await pickupCall(vars.sid, vars.callId);
      const id = res.call.callId;
      const conn = await openCall(vars.sid, id, micId, { video: false });
      registerOwnConnection(id, conn);
      await resumeCall(vars.sid, id); // sai da espera só depois que a ponte assumiu
      clearTransferOffer();
      return id;
    },
    onError: (e: Error) => {
      clearTransferOffer();
      toast.error(e.message);
    },
  });
