import { useEffect } from "react";
import { PlusCircle } from "lucide-react";
import { TooltipProvider } from "@/components/ui/tooltip";
import { Toaster } from "@/components/ui/sonner";
import { AppShell } from "@/components/layout/AppShell";
import { CallsPage } from "@/pages/CallsPage";
import { SessionPairing } from "@/components/domain/session/SessionPairing";
import { SessionHeader } from "@/components/domain/session/SessionHeader";
import { IncomingCallModal } from "@/components/domain/call/IncomingCallModal";
import { TransferOfferModal } from "@/components/domain/call/TransferOfferModal";
import { EmptyState } from "@/components/shared/EmptyState";
import { ensureSessionsWired, useSessions } from "@/stores/sessions";
import { ensureCallsWired } from "@/stores/calls";
import { useTheme } from "@/stores/theme";

export const App = () => {
  const sessions = useSessions((s) => s.sessions);
  const activeId = useSessions((s) => s.activeId);
  const theme = useTheme((s) => s.theme);

  useEffect(() => {
    ensureSessionsWired();
    ensureCallsWired();
  }, []);

  const active = sessions.find((s) => s.id === activeId) ?? null;

  return (
    <TooltipProvider delayDuration={200}>
      <AppShell>
        {sessions.length === 0 ? (
          <EmptyState
            icon={<PlusCircle className="h-6 w-6" />}
            title="Nenhuma conta ainda"
            description="Crie sua primeira conta de WhatsApp na barra lateral para começar a ligar."
          />
        ) : active ? (
          <div className="space-y-6">
            <SessionHeader session={active} />
            {active.paired ? <CallsPage sid={active.id} /> : <SessionPairing session={active} />}
          </div>
        ) : (
          <EmptyState title="Selecione uma conta" description="Escolha uma conta na barra lateral." />
        )}
      </AppShell>
      <IncomingCallModal />
      <TransferOfferModal />
      <Toaster theme={theme} position="top-right" richColors closeButton />
    </TooltipProvider>
  );
};
