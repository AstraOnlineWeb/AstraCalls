import { useEffect, useState } from "react";
import { Webhook, Loader2 } from "lucide-react";
import { toast } from "sonner";
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
  DialogDescription,
  DialogTrigger,
  DialogFooter,
} from "@/components/ui/dialog";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Switch } from "@/components/ui/switch";
import { getWebhook, setWebhook, deleteWebhook, type WebhookInput } from "@/services/webhook";

// Tipos de evento que o webhook da sessão entrega (nenhum marcado = todos).
const EVENTS: { id: string; label: string }[] = [
  { id: "message", label: "Mensagens" },
  { id: "receipt", label: "Entrega/leitura" },
  { id: "reaction", label: "Reações" },
  { id: "deleted", label: "Apagadas" },
  { id: "poll_vote", label: "Votos de enquete" },
  { id: "event_response", label: "Respostas de evento" },
  { id: "form_response", label: "Formulários" },
  { id: "group_participants", label: "Participantes de grupo" },
  { id: "view_once", label: "Visualização única" },
  { id: "session_status", label: "Status da sessão" },
  { id: "broadcast", label: "Disparos" },
  { id: "recording", label: "Gravações" },
  { id: "restriction", label: "Restrições" },
  { id: "call_peer_mute", label: "Mudo na chamada" },
  { id: "call_hand_raise", label: "Mão levantada" },
];

export const WebhookDialog = ({ sid }: { sid: string }) => {
  const [open, setOpen] = useState(false);
  const [busy, setBusy] = useState(false);
  const [enabled, setEnabled] = useState(false);
  const [url, setUrl] = useState("");
  const [secret, setSecret] = useState("");
  const [hasSecret, setHasSecret] = useState(false);
  const [events, setEvents] = useState<string[]>([]);
  const [ignoreGroups, setIgnoreGroups] = useState(false);

  // carrega já na montagem p/ o botão refletir se há webhook configurado
  useEffect(() => {
    getWebhook(sid)
      .then((r) => {
        setEnabled(!!r.webhook);
        setUrl(r.webhook || "");
        setHasSecret(r.hasSecret);
        setEvents(r.events ?? []);
        setIgnoreGroups(!!r.ignoreGroups);
        setSecret("");
      })
      .catch(() => {});
  }, [open, sid]);

  const toggle = (id: string) =>
    setEvents((ev) => (ev.includes(id) ? ev.filter((e) => e !== id) : [...ev, id]));

  const save = async () => {
    const u = url.trim();
    if (!/^https?:\/\//i.test(u)) {
      toast.error("Informe uma URL http(s) válida");
      return;
    }
    setBusy(true);
    try {
      const body: WebhookInput = { url: u, events, ignoreGroups };
      if (secret.trim()) body.secret = secret.trim();
      await setWebhook(sid, body);
      toast.success("Webhook salvo");
      setEnabled(true);
      setOpen(false);
    } catch (e) {
      toast.error((e as Error).message);
    } finally {
      setBusy(false);
    }
  };

  const remove = async () => {
    setBusy(true);
    try {
      await deleteWebhook(sid);
      toast.success("Webhook removido");
      setEnabled(false);
      setOpen(false);
    } catch (e) {
      toast.error((e as Error).message);
    } finally {
      setBusy(false);
    }
  };

  const clearSecret = async () => {
    setBusy(true);
    try {
      await setWebhook(sid, { url: url.trim(), secret: "", events, ignoreGroups });
      setHasSecret(false);
      toast.success("Secret removido");
    } catch (e) {
      toast.error((e as Error).message);
    } finally {
      setBusy(false);
    }
  };

  return (
    <Dialog open={open} onOpenChange={setOpen}>
      <DialogTrigger asChild>
        <Button variant={enabled ? "default" : "outline"} size="sm" title="Webhook">
          <Webhook className="h-4 w-4" />
          <span className="hidden sm:inline">Webhook</span>
        </Button>
      </DialogTrigger>
      <DialogContent className="max-h-[90vh] overflow-y-auto">
        <DialogHeader>
          <DialogTitle>Webhook da sessão</DialogTitle>
          <DialogDescription>
            Os eventos desta conta são enviados por POST (JSON) para a URL abaixo, no formato{" "}
            <code>{"{ session, event, timestamp, data }"}</code>.
          </DialogDescription>
        </DialogHeader>

        <div className="space-y-3">
          <div className="space-y-1">
            <Label>URL</Label>
            <Input
              value={url}
              onChange={(e) => setUrl(e.target.value)}
              placeholder="https://meu-sistema/webhook/astracalls"
            />
          </div>
          <div className="space-y-1">
            <Label>Secret (opcional)</Label>
            <div className="flex gap-2">
              <Input
                value={secret}
                onChange={(e) => setSecret(e.target.value)}
                type="password"
                placeholder={hasSecret ? "configurado — deixe vazio p/ manter" : "assina com X-Webhook-Signature"}
              />
              {hasSecret && (
                <Button type="button" variant="outline" size="sm" disabled={busy} onClick={clearSecret}>
                  Remover
                </Button>
              )}
            </div>
          </div>

          <label className="flex cursor-pointer items-start justify-between gap-4 rounded-lg border p-3">
            <span className="space-y-0.5">
              <span className="block text-sm font-medium">Ignorar grupos</span>
              <span className="block text-xs text-muted-foreground">
                Não envia nenhum evento de grupo (mensagens, confirmações, reações, participantes…). Só
                conversas individuais.
              </span>
            </span>
            <Switch checked={ignoreGroups} onCheckedChange={setIgnoreGroups} aria-label="Ignorar grupos" />
          </label>

          <div className="space-y-2">
            <Label>Eventos</Label>
            <p className="text-xs text-muted-foreground">
              {events.length === 0 ? "Nenhum marcado = envia todos." : `${events.length} selecionado(s).`}
            </p>
            <div className="flex flex-wrap gap-2">
              {EVENTS.map((e) => {
                const on = events.includes(e.id);
                return (
                  <Button
                    key={e.id}
                    type="button"
                    size="sm"
                    variant={on ? "default" : "outline"}
                    className="h-7 px-2 text-xs"
                    title={e.id}
                    aria-pressed={on}
                    onClick={() => toggle(e.id)}
                  >
                    {e.label}
                  </Button>
                );
              })}
            </div>
          </div>
        </div>

        <DialogFooter className="gap-2 sm:justify-between">
          {enabled ? (
            <Button variant="destructive" size="sm" disabled={busy} onClick={remove}>
              Remover webhook
            </Button>
          ) : (
            <span />
          )}
          <Button disabled={busy} onClick={save}>
            {busy ? <Loader2 className="h-4 w-4 animate-spin" /> : null}
            Salvar
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
};
