import { useEffect, useState } from "react";
import { Settings2, Loader2 } from "lucide-react";
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
import { Switch } from "@/components/ui/switch";
import { getOptions, setOptions, type SessionOptions } from "@/services/sessions";

const empty: SessionOptions = { read_messages: false, always_online: false };

export const OptionsDialog = ({ sid }: { sid: string }) => {
  const [open, setOpen] = useState(false);
  const [busy, setBusy] = useState(false);
  const [opts, setOpts] = useState<SessionOptions>({ ...empty });

  useEffect(() => {
    if (!open) return;
    getOptions(sid)
      .then((o) => setOpts({ ...empty, ...o }))
      .catch(() => {});
  }, [open, sid]);

  const set = (k: keyof SessionOptions) => (v: boolean) => setOpts((o) => ({ ...o, [k]: v }));

  const save = async () => {
    setBusy(true);
    try {
      await setOptions(sid, opts);
      toast.success("Opções salvas");
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
        <Button variant="outline" size="sm" title="Opções da conta">
          <Settings2 className="h-4 w-4" />
          <span className="hidden sm:inline">Opções</span>
        </Button>
      </DialogTrigger>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>Opções da conta</DialogTitle>
          <DialogDescription>Como esta conta aparece para os contatos e para o celular.</DialogDescription>
        </DialogHeader>

        <div className="space-y-3">
          <label className="flex cursor-pointer items-start justify-between gap-4 rounded-lg border p-3">
            <span className="space-y-0.5">
              <span className="block text-sm font-medium">Ler mensagens automaticamente</span>
              <span className="block text-xs text-muted-foreground">
                Marca como lida (tique azul) toda mensagem recebida. Com isso o celular não mostra a
                mensagem como não lida.
              </span>
            </span>
            <Switch
              checked={opts.read_messages}
              onCheckedChange={set("read_messages")}
              aria-label="Ler mensagens automaticamente"
            />
          </label>

          <label className="flex cursor-pointer items-start justify-between gap-4 rounded-lg border p-3">
            <span className="space-y-0.5">
              <span className="block text-sm font-medium">Sempre online</span>
              <span className="block text-xs text-muted-foreground">
                Mantém a conta aparecendo como online. Atenção: enquanto estiver online o WhatsApp{" "}
                <b>não notifica o celular</b>. Desligado, a conta fica online só por um instante ao
                conectar, o suficiente para o WhatsApp não desconectar o dispositivo.
              </span>
            </span>
            <Switch
              checked={opts.always_online}
              onCheckedChange={set("always_online")}
              aria-label="Sempre online"
            />
          </label>
        </div>

        <DialogFooter>
          <Button disabled={busy} onClick={() => void save()}>
            {busy ? <Loader2 className="h-4 w-4 animate-spin" /> : null}
            Salvar
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
};
