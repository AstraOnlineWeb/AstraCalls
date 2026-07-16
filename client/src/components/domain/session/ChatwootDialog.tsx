import { useEffect, useState, type ChangeEvent } from "react";
import { MessageSquare, Loader2, Copy, Check } from "lucide-react";
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
import { getChatwoot, setChatwoot, deleteChatwoot } from "@/services/chatwoot";

const empty = { url: "", account_id: "", account_token: "", inbox_id: "", inbox_identifier: "" };

export const ChatwootDialog = ({ sid }: { sid: string }) => {
  const [open, setOpen] = useState(false);
  const [busy, setBusy] = useState(false);
  const [enabled, setEnabled] = useState(false);
  const [copied, setCopied] = useState(false);
  const [form, setForm] = useState({ ...empty });
  const [mirrorApi, setMirrorApi] = useState(false);
  const [importHistory, setImportHistory] = useState(false);
  const [importDays, setImportDays] = useState("30");

  const webhookUrl = `${window.location.origin}/api/sessions/${sid}/chatwoot/webhook`;

  useEffect(() => {
    if (!open) return;
    getChatwoot(sid)
      .then((r) => {
        setEnabled(r.enabled);
        const c = r.chatwoot || ({} as typeof r.chatwoot);
        setMirrorApi(!!c.mirror_api);
        setImportHistory(!!c.import_history);
        setImportDays(c.import_history_days ? String(c.import_history_days) : "30");
        setForm({
          url: c.url || "",
          account_id: c.account_id ? String(c.account_id) : "",
          account_token: "",
          inbox_id: c.inbox_id ? String(c.inbox_id) : "",
          inbox_identifier: c.inbox_identifier || "",
        });
      })
      .catch(() => {});
  }, [open, sid]);

  const set = (k: keyof typeof empty) => (e: ChangeEvent<HTMLInputElement>) =>
    setForm((f) => ({ ...f, [k]: e.target.value }));

  const save = async () => {
    setBusy(true);
    try {
      await setChatwoot(sid, {
        url: form.url.trim(),
        account_id: Number(form.account_id),
        account_token: form.account_token.trim(),
        inbox_id: Number(form.inbox_id),
        inbox_identifier: form.inbox_identifier.trim(),
        mirror_api: mirrorApi,
        import_history: importHistory,
        import_history_days: Number(importDays) || 30,
      });
      toast.success("Chatwoot conectado a esta sessão");
      setEnabled(true);
      setOpen(false);
    } catch (e) {
      toast.error((e as Error).message);
    } finally {
      setBusy(false);
    }
  };

  const disconnect = async () => {
    setBusy(true);
    try {
      await deleteChatwoot(sid);
      toast.success("Chatwoot desconectado");
      setEnabled(false);
      setForm({ ...empty });
      setOpen(false);
    } catch (e) {
      toast.error((e as Error).message);
    } finally {
      setBusy(false);
    }
  };

  const copy = () => {
    navigator.clipboard?.writeText(webhookUrl);
    setCopied(true);
    setTimeout(() => setCopied(false), 1500);
  };

  return (
    <Dialog open={open} onOpenChange={setOpen}>
      <DialogTrigger asChild>
        <Button variant={enabled ? "default" : "outline"} size="sm" title="Chatwoot">
          <MessageSquare className="h-4 w-4" />
          <span className="hidden sm:inline">Chatwoot</span>
        </Button>
      </DialogTrigger>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>Integração com Chatwoot</DialogTitle>
          <DialogDescription>
            Conecte esta sessão a uma caixa de entrada (inbox do tipo <b>API</b>) do Chatwoot.
          </DialogDescription>
        </DialogHeader>

        <div className="space-y-3">
          <div className="space-y-1">
            <Label>URL do Chatwoot</Label>
            <Input value={form.url} onChange={set("url")} placeholder="https://chatwoot.seudominio.com" />
          </div>
          <div className="grid grid-cols-2 gap-3">
            <div className="space-y-1">
              <Label>Account ID</Label>
              <Input value={form.account_id} onChange={set("account_id")} inputMode="numeric" placeholder="1" />
            </div>
            <div className="space-y-1">
              <Label>Inbox ID</Label>
              <Input value={form.inbox_id} onChange={set("inbox_id")} inputMode="numeric" placeholder="5" />
            </div>
          </div>
          <div className="space-y-1">
            <Label>Access Token</Label>
            <Input
              value={form.account_token}
              onChange={set("account_token")}
              type="password"
              placeholder={enabled ? "deixe vazio p/ manter o atual" : "token do agente/admin"}
            />
          </div>
          <div className="space-y-1">
            <Label>Inbox Identifier</Label>
            <Input value={form.inbox_identifier} onChange={set("inbox_identifier")} placeholder="abcd1234" />
          </div>
          <div className="space-y-1">
            <Label>Webhook (cole na inbox do Chatwoot)</Label>
            <div className="flex gap-2">
              <Input readOnly value={webhookUrl} className="text-xs" onFocus={(e) => e.target.select()} />
              <Button type="button" variant="outline" size="icon" onClick={copy} aria-label="Copiar webhook">
                {copied ? <Check className="h-4 w-4" /> : <Copy className="h-4 w-4" />}
              </Button>
            </div>
          </div>

          <label className="flex cursor-pointer items-start justify-between gap-4 rounded-lg border p-3">
            <span className="space-y-0.5">
              <span className="block text-sm font-medium">Refletir mensagens enviadas pela API</span>
              <span className="block text-xs text-muted-foreground">
                O que for disparado pela API (ex.: n8n) aparece na conversa como nota privada, igual ao
                que é enviado pelo aparelho. Não reenvia nada ao contato.
              </span>
            </span>
            <Switch
              checked={mirrorApi}
              onCheckedChange={setMirrorApi}
              aria-label="Refletir mensagens enviadas pela API"
            />
          </label>

          <div className="rounded-lg border p-3">
            <label className="flex cursor-pointer items-start justify-between gap-4">
              <span className="space-y-0.5">
                <span className="block text-sm font-medium">Importar histórico ao conectar</span>
                <span className="block text-xs text-muted-foreground">
                  Traz as conversas antigas que o WhatsApp envia ao parear, reconstruindo a timeline no
                  Chatwoot. Ative <b>antes de conectar a conta</b> — o histórico só chega no pareamento.
                </span>
              </span>
              <Switch
                checked={importHistory}
                onCheckedChange={setImportHistory}
                aria-label="Importar histórico ao conectar"
              />
            </label>
            {importHistory && (
              <div className="mt-3 flex items-center gap-2">
                <Label className="text-xs text-muted-foreground">Importar últimos</Label>
                <Input
                  value={importDays}
                  onChange={(e) => setImportDays(e.target.value.replace(/\D/g, ""))}
                  inputMode="numeric"
                  className="h-8 w-20"
                  aria-label="Dias de histórico a importar"
                />
                <span className="text-xs text-muted-foreground">dias</span>
              </div>
            )}
          </div>
        </div>

        <DialogFooter className="gap-2 sm:justify-between">
          {enabled ? (
            <Button variant="destructive" size="sm" disabled={busy} onClick={disconnect}>
              Desconectar
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
