import { useEffect, useState } from "react";
import { Globe, Loader2 } from "lucide-react";
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
import { getProxy, setProxy } from "@/services/sessions";

export const ProxyDialog = ({ sid }: { sid: string }) => {
  const [open, setOpen] = useState(false);
  const [busy, setBusy] = useState(false);
  const [enabled, setEnabled] = useState(false);
  const [url, setUrl] = useState("");

  useEffect(() => {
    if (!open) return;
    getProxy(sid)
      .then((r) => {
        setEnabled(r.enabled);
        setUrl(r.proxy || "");
      })
      .catch(() => {});
  }, [open, sid]);

  const save = async (value: string) => {
    setBusy(true);
    try {
      await setProxy(sid, value);
      setEnabled(value !== "");
      toast.success(value ? "Proxy salvo — reconectando a sessão" : "Proxy removido — reconectando");
      setOpen(false);
    } catch (e) {
      toast.error((e as Error).message);
    } finally {
      setBusy(false);
    }
  };

  return (
    <Dialog open={open} onOpenChange={setOpen}>
      <DialogTrigger asChild>
        <Button variant={enabled ? "default" : "outline"} size="sm" title="Proxy da conexão">
          <Globe className="h-4 w-4" />
          <span className="hidden sm:inline">Proxy</span>
        </Button>
      </DialogTrigger>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>Proxy da conexão</DialogTitle>
          <DialogDescription>
            A conexão do WhatsApp desta conta (websocket + mídia) sai por este proxy. Trocar reconecta a
            sessão automaticamente. Deixe vazio para conexão direta.
          </DialogDescription>
        </DialogHeader>

        <div className="space-y-1">
          <Label>URL do proxy</Label>
          <Input
            value={url}
            onChange={(e) => setUrl(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === "Enter") void save(url.trim());
            }}
            placeholder="socks5://usuario:senha@host:1080"
            autoComplete="off"
            spellCheck={false}
          />
          <p className="text-xs text-muted-foreground">Formatos: http://, https:// ou socks5://</p>
        </div>

        <DialogFooter className="gap-2 sm:justify-between">
          {enabled ? (
            <Button variant="destructive" size="sm" disabled={busy} onClick={() => void save("")}>
              Remover
            </Button>
          ) : (
            <span />
          )}
          <Button disabled={busy} onClick={() => void save(url.trim())}>
            {busy ? <Loader2 className="h-4 w-4 animate-spin" /> : null}
            Salvar
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
};
