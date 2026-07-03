import { useState } from "react";
import { Loader2, Power, QrCode, Mic } from "lucide-react";
import { toast } from "sonner";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Switch } from "@/components/ui/switch";
import { ChatwootDialog } from "./ChatwootDialog";
import { logoutSession, pairSession, setRecording } from "@/services/sessions";
import type { SessionInfo, SessionState } from "@/types/session";

const statusLabel: Record<SessionState, string> = {
  open: "Conectado",
  qr: "Ler QR",
  connecting: "Conectando…",
  logged_out: "Desconectado",
};

const statusVariant: Record<SessionState, "success" | "secondary" | "muted" | "destructive"> = {
  open: "success",
  qr: "secondary",
  connecting: "muted",
  logged_out: "destructive",
};

export const SessionHeader = ({ session }: { session: SessionInfo }) => {
  const [busy, setBusy] = useState(false);
  const [recBusy, setRecBusy] = useState(false);

  const toggleRecording = async (value: boolean) => {
    setRecBusy(true);
    try {
      await setRecording(session.id, value);
    } catch (e) {
      toast.error((e as Error).message);
    } finally {
      setRecBusy(false);
    }
  };

  const run = async (fn: () => Promise<unknown>) => {
    setBusy(true);
    try {
      await fn();
    } catch (e) {
      toast.error((e as Error).message);
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="flex flex-wrap items-center justify-between gap-3">
      <div className="flex min-w-0 items-center gap-2">
        <h1 className="truncate text-xl font-semibold tracking-tight">{session.name}</h1>
        <Badge variant={statusVariant[session.state]}>{statusLabel[session.state]}</Badge>
      </div>
      <div className="flex items-center gap-2">
        {session.paired && (
          <label className="mr-1 flex cursor-pointer items-center gap-2 text-sm text-muted-foreground">
            <Mic className="h-4 w-4" />
            <span className="hidden sm:inline">Gravar</span>
            <Switch
              checked={session.recording}
              onCheckedChange={toggleRecording}
              disabled={recBusy}
              aria-label="Gravar chamadas desta conta"
            />
          </label>
        )}
        {session.paired && <ChatwootDialog sid={session.id} />}
        {session.paired ? (
          <Button variant="outline" size="sm" disabled={busy} onClick={() => run(() => logoutSession(session.id))}>
            {busy ? <Loader2 className="h-4 w-4 animate-spin" /> : <Power className="h-4 w-4" />}
            Desconectar
          </Button>
        ) : (
          <Button size="sm" disabled={busy} onClick={() => run(() => pairSession(session.id))}>
            {busy ? <Loader2 className="h-4 w-4 animate-spin" /> : <QrCode className="h-4 w-4" />}
            Reativar
          </Button>
        )}
      </div>
    </div>
  );
};
