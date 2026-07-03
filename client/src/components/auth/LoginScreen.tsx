import { useState } from "react";
import { Loader2 } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { setAuth } from "@/lib/auth";

export const LoginScreen = ({ onSuccess }: { onSuccess: () => void }) => {
  const [url, setUrl] = useState(window.location.origin);
  const [key, setKey] = useState("");
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState("");

  const submit = async () => {
    setBusy(true);
    setErr("");
    const base = url.replace(/\/+$/, "");
    try {
      const r = await fetch(`${base}/api/config`, { headers: { "X-API-Key": key } });
      if (!r.ok) {
        setErr(r.status === 401 ? "API key inválida" : `Erro ${r.status}`);
        return;
      }
      setAuth(base, key);
      onSuccess();
    } catch {
      setErr("Não foi possível conectar à URL informada");
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="flex min-h-screen items-center justify-center bg-background px-4">
      <div className="w-full max-w-sm space-y-5 rounded-2xl border bg-card p-7 shadow-soft">
        <div className="flex flex-col items-center gap-2 text-center">
          <span className="inline-flex dark:rounded-xl dark:bg-white dark:px-3 dark:py-2">
            <img src="/logoCalls.png" alt="AstraCalls" className="h-9 w-auto select-none" draggable={false} />
          </span>
          <p className="text-sm text-muted-foreground">Acesse com a URL e a API key</p>
        </div>
        <div className="space-y-3">
          <div className="space-y-1">
            <Label>URL</Label>
            <Input value={url} onChange={(e) => setUrl(e.target.value)} placeholder="https://call.seudominio.com" />
          </div>
          <div className="space-y-1">
            <Label>API key</Label>
            <Input
              type="password"
              value={key}
              onChange={(e) => setKey(e.target.value)}
              onKeyDown={(e) => {
                if (e.key === "Enter") submit();
              }}
              placeholder="sua chave"
            />
          </div>
          {err && <p className="text-sm text-destructive">{err}</p>}
          <Button className="w-full" disabled={busy || !key.trim()} onClick={submit}>
            {busy ? <Loader2 className="h-4 w-4 animate-spin" /> : null}
            Entrar
          </Button>
        </div>
      </div>
    </div>
  );
};
