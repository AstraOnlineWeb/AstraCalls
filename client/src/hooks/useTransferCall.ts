import { useMutation } from "@tanstack/react-query";
import { toast } from "sonner";
import { transferCall } from "@/services/calls";

// Transferência cega: põe a chamada em espera com música e re-oferece a outro
// atendente. to opcional = clientId do atendente alvo; sem ele vai para a fila da conta.
export const useTransferCall = () =>
  useMutation({
    mutationFn: async (vars: { sid: string; callId: string; to?: string }) => {
      await transferCall(vars.sid, vars.callId, vars.to);
    },
    onSuccess: () => toast.success("Chamada transferida"),
    onError: (e: Error) => toast.error(e.message),
  });
