import { useMutation } from "@tanstack/react-query";
import { holdCall, resumeCall } from "@/services/calls";

// useCallHold expõe colocar em espera / retomar uma chamada. O backend mantém o leg
// vivo e toca música de espera para o interlocutor enquanto held=true.
export const useCallHold = () =>
  useMutation({
    mutationFn: async (vars: { sid: string; callId: string; hold: boolean }) => {
      try {
        if (vars.hold) await holdCall(vars.sid, vars.callId);
        else await resumeCall(vars.sid, vars.callId);
      } catch {}
    },
  });
