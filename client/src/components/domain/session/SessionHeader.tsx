import { useState } from "react";
import { Loader2, Power, QrCode, Mic, Copy, Check, Phone } from "lucide-react";
import { toast } from "sonner";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Switch } from "@/components/ui/switch";
import { ChatwootDialog } from "./ChatwootDialog";
import { ProxyDialog } from "./ProxyDialog";
import { SIPDialog } from "./SIPDialog";
import { logoutSession, pairSession, setRecording } from "@/services/sessions";
import { phoneFromJid } from "@/utils/format";
import type { SessionInfo, SessionState } from "@/types/session";

const statusLabel: Record<SessionState, string> = {
  open: "Conectado",
  qr: "Ler QR",
  connecting: "Conectando…",
  logged_out: "Desconectado",
  pairing_code: "Código de pareamento",
  passkey_request: "Confirmar passkey",
};

const statusVariant: Record<SessionState, "success" | "secondary" | "muted" | "destructive"> = {
  open: "success",
  qr: "secondary",
  connecting: "muted",
  logged_out: "destructive",
  pairing_code: "secondary",
  passkey_request: "secondary",
};

export const SessionHeader = ({ session }: { session: SessionInfo }) => {
  const [busy, setBusy] = useState(false);
  const [recBusy, setRecBusy] = useState(false);
  const [copied, setCopied] = useState(false);

  const copyId = async () => {
    try {
      await navigator.clipboard?.writeText(session.id);
      setCopied(true);
      setTimeout(() => setCopied(false), 1500);
    } catch {
      toast.error("Não foi possível copiar");
    }
  };

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

  const connectedPhone = phoneFromJid(session.jid);
  const lastPhone = phoneFromJid(session.lastJid);

  return (
    <div className="flex flex-wrap items-center justify-between gap-3">
      <div className="flex min-w-0 flex-col gap-1">
        <div className="flex min-w-0 items-center gap-2">
          <h1 className="truncate text-xl font-semibold tracking-tight">{session.name}</h1>
          <Badge variant={statusVariant[session.state]}>{statusLabel[session.state]}</Badge>
        </div>
        {connectedPhone ? (
          <div className="flex items-center gap-1.5 text-sm">
            <Phone className="h-3.5 w-3.5 text-emerald-600" />
            <span className="font-medium text-foreground">{connectedPhone}</span>
          </div>
        ) : lastPhone ? (
          <div className="flex items-center gap-1.5 text-xs text-muted-foreground" title="Último número que esteve conectado">
            <Phone className="h-3.5 w-3.5" />
            <span>Último número: <span className="font-medium">{lastPhone}</span></span>
          </div>
        ) : null}
        <button
          type="button"
          onClick={copyId}
          title="Copiar ID da sessão (use na API)"
          className="flex w-fit items-center gap-1.5 rounded-md text-xs text-muted-foreground transition-colors hover:text-foreground"
        >
          <span className="font-medium">ID:</span>
          <code className="rounded bg-muted px-1.5 py-0.5 font-mono">{session.id}</code>
          {copied ? <Check className="h-3.5 w-3.5" /> : <Copy className="h-3.5 w-3.5" />}
        </button>
      </div>
      <div className="flex flex-wrap items-center justify-end gap-2">
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
        {session.paired && <ProxyDialog sid={session.id} />}
        {session.paired && <SIPDialog session={session} />}
        {session.paired && <ChatwootDialog sid={session.id} />}
        {session.paired ? (
          <Button
            variant="outline"
            size="sm"
            disabled={busy}
            title="Desconectar"
            onClick={() => run(() => logoutSession(session.id))}
          >
            {busy ? <Loader2 className="h-4 w-4 animate-spin" /> : <Power className="h-4 w-4" />}
            <span className="hidden sm:inline">Desconectar</span>
          </Button>
        ) : (
          <Button size="sm" disabled={busy} title="Reativar" onClick={() => run(() => pairSession(session.id))}>
            {busy ? <Loader2 className="h-4 w-4 animate-spin" /> : <QrCode className="h-4 w-4" />}
            <span className="hidden sm:inline">Reativar</span>
          </Button>
        )}
      </div>
    </div>
  );
};
