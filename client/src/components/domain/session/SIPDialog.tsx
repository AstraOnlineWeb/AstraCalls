import { useEffect, useState, type ReactNode } from "react";
import { Phone, Copy, Check, Eye, EyeOff, Loader2, Server, ArrowUpRight } from "lucide-react";
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
import { Badge } from "@/components/ui/badge";
import { cn } from "@/lib/utils";
import { getSIPExt, setSIPExt, type SIPExtConfig } from "@/services/sessions";
import type { SessionInfo } from "@/types/session";

const SIP_PORT = 5060;

type Field = "user" | "pass" | "server" | "domain";
type Tab = "server" | "external";

const emptyExt: SIPExtConfig = {
  enabled: false,
  host: "",
  port: SIP_PORT,
  user: "",
  pass: "",
  dest: "",
};

const StatusBadge = ({ status, error }: { status?: string; error?: string }) => {
  if (!status) return null;
  const map: Record<string, { label: string; cls: string }> = {
    registered: { label: "Registrado", cls: "bg-emerald-500/15 text-emerald-600 border-emerald-500/30" },
    registering: { label: "Registrando…", cls: "bg-amber-500/15 text-amber-600 border-amber-500/30" },
    failed: { label: "Falhou", cls: "bg-red-500/15 text-red-600 border-red-500/30" },
  };
  const s = map[status] ?? { label: status, cls: "" };
  return (
    <Badge variant="outline" className={cn("font-normal", s.cls)} title={error || undefined}>
      {s.label}
    </Badge>
  );
};

export const SIPDialog = ({ session }: { session: SessionInfo }) => {
  const [open, setOpen] = useState(false);
  const [tab, setTab] = useState<Tab>("server");
  const [showPass, setShowPass] = useState(false);
  const [copied, setCopied] = useState<Field | null>(null);

  // Modelo 1 (servidor): credenciais que o PBX do cliente usa para registrar aqui.
  const sipUser = session.sip_user || "";
  const sipPass = session.sip_pass || "";
  const server =
    session.sip_url && session.sip_url !== "127.0.0.1:5060"
      ? session.sip_url
      : `${window.location.hostname}:${SIP_PORT}`;
  const domain = server.split(":")[0];

  // Modelo 2 (externo): esta sessão se registra num PBX do cliente.
  const [ext, setExt] = useState<SIPExtConfig>({ ...emptyExt });
  const [busy, setBusy] = useState(false);
  const [showExtPass, setShowExtPass] = useState(false);

  useEffect(() => {
    if (!open) return;
    getSIPExt(session.id)
      .then((r) => setExt({ ...emptyExt, ...r, port: r.port || SIP_PORT }))
      .catch(() => {});
  }, [open, session.id]);

  const copy = (field: Field, value: string) => {
    navigator.clipboard?.writeText(value);
    setCopied(field);
    setTimeout(() => setCopied(null), 1500);
  };

  const setE = <K extends keyof SIPExtConfig>(k: K, v: SIPExtConfig[K]) =>
    setExt((prev) => ({ ...prev, [k]: v }));

  const saveExt = async () => {
    if (ext.enabled && (!ext.host.trim() || !ext.user.trim())) {
      toast.error("Preencha host e usuário do PBX");
      return;
    }
    setBusy(true);
    try {
      await setSIPExt(session.id, {
        enabled: ext.enabled,
        host: ext.host.trim(),
        port: Number(ext.port) || SIP_PORT,
        user: ext.user.trim(),
        pass: ext.pass,
        dest: ext.dest.trim(),
      });
      toast.success(ext.enabled ? "Registro no PBX salvo — conectando…" : "Registro no PBX desativado");
      setOpen(false);
    } catch (e) {
      toast.error((e as Error).message);
    } finally {
      setBusy(false);
    }
  };

  const Row = ({ label, field, value }: { label: string; field: Field; value: string }) => (
    <div className="space-y-1">
      <Label>{label}</Label>
      <div className="flex gap-2">
        <Input readOnly value={value} className="font-mono text-xs" onFocus={(e) => e.target.select()} />
        <Button type="button" variant="outline" size="icon" onClick={() => copy(field, value)} aria-label={`Copiar ${label}`}>
          {copied === field ? <Check className="h-4 w-4" /> : <Copy className="h-4 w-4" />}
        </Button>
      </div>
    </div>
  );

  const TabButton = ({ id, icon, children }: { id: Tab; icon: ReactNode; children: ReactNode }) => (
    <button
      type="button"
      onClick={() => setTab(id)}
      className={cn(
        "flex flex-1 items-center justify-center gap-1.5 rounded-full px-3 py-1.5 text-xs font-medium transition-colors",
        tab === id ? "bg-card text-foreground shadow-sm" : "text-muted-foreground hover:text-foreground",
      )}
    >
      {icon}
      {children}
    </button>
  );

  return (
    <Dialog open={open} onOpenChange={setOpen}>
      <DialogTrigger asChild>
        <Button variant={session.sip_ext_status === "registered" ? "default" : "outline"} size="sm">
          <Phone className="h-4 w-4" />
          SIP
        </Button>
      </DialogTrigger>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>Integração SIP / PBX</DialogTitle>
          <DialogDescription>
            Duas formas de integrar esta sessão a um PBX/softphone. Escolha uma das abas abaixo.
          </DialogDescription>
        </DialogHeader>

        <div className="flex gap-1 rounded-full bg-muted p-1">
          <TabButton id="server" icon={<Server className="h-3.5 w-3.5" />}>
            PBX registra aqui
          </TabButton>
          <TabButton id="external" icon={<ArrowUpRight className="h-3.5 w-3.5" />}>
            Registrar em PBX
          </TabButton>
        </div>

        {tab === "server" ? (
          <div className="space-y-3">
            <p className="text-xs text-muted-foreground">
              O PBX/softphone do cliente <b>se registra neste servidor</b> com as credenciais abaixo.
              Chamadas discadas pelo tronco saem pelo WhatsApp desta sessão.
            </p>
            <Row label="Servidor SIP (host:porta)" field="server" value={server} />
            <Row label="Domínio / Realm" field="domain" value={domain} />
            <Row label="Usuário SIP" field="user" value={sipUser} />

            <div className="space-y-1">
              <Label>Senha SIP</Label>
              <div className="flex gap-2">
                <Input
                  readOnly
                  type={showPass ? "text" : "password"}
                  value={sipPass}
                  className="font-mono text-xs"
                  onFocus={(e) => e.target.select()}
                />
                <Button
                  type="button"
                  variant="outline"
                  size="icon"
                  onClick={() => setShowPass((v) => !v)}
                  aria-label={showPass ? "Ocultar senha" : "Mostrar senha"}
                >
                  {showPass ? <EyeOff className="h-4 w-4" /> : <Eye className="h-4 w-4" />}
                </Button>
                <Button
                  type="button"
                  variant="outline"
                  size="icon"
                  onClick={() => copy("pass", sipPass)}
                  aria-label="Copiar senha"
                >
                  {copied === "pass" ? <Check className="h-4 w-4" /> : <Copy className="h-4 w-4" />}
                </Button>
              </div>
            </div>

            <div className="rounded-md border border-border bg-muted/40 p-3 text-xs text-muted-foreground">
              <p className="font-medium text-foreground">Como configurar</p>
              <ul className="mt-1 list-disc space-y-0.5 pl-4">
                <li>Transporte: <b>UDP</b> (porta {SIP_PORT}), codec <b>G.711 u-law (PCMU)</b>.</li>
                <li>No FreePBX: crie um <b>Trunk PJSIP</b> com Username = Usuário SIP e SIP Server = host acima.</li>
                <li>Disque o número no formato internacional (ex.: 5511999999999).</li>
                <li>Mantenha estas credenciais em sigilo — dão acesso a fazer chamadas por esta sessão.</li>
              </ul>
            </div>
          </div>
        ) : (
          <div className="space-y-3">
            <div className="flex items-center justify-between rounded-md border border-border p-3">
              <div>
                <p className="text-sm font-medium">Registrar em PBX externo</p>
                <p className="text-xs text-muted-foreground">
                  O AstraCalls vira um <b>ramal registrado</b> no seu Asterisk/FreePBX.
                </p>
              </div>
              <div className="flex items-center gap-2">
                <StatusBadge status={ext.status} error={ext.error} />
                <Switch checked={ext.enabled} onCheckedChange={(v) => setE("enabled", v)} />
              </div>
            </div>

            {ext.error && ext.status === "failed" ? (
              <p className="text-xs text-red-600">{ext.error}</p>
            ) : null}

            <div className="grid grid-cols-[1fr_6rem] gap-2">
              <div className="space-y-1">
                <Label>Host do PBX</Label>
                <Input
                  value={ext.host}
                  onChange={(e) => setE("host", e.target.value)}
                  placeholder="pbx.suaempresa.com"
                  disabled={!ext.enabled}
                />
              </div>
              <div className="space-y-1">
                <Label>Porta</Label>
                <Input
                  value={String(ext.port)}
                  onChange={(e) => setE("port", Number(e.target.value.replace(/\D/g, "")) || 0)}
                  inputMode="numeric"
                  placeholder="5060"
                  disabled={!ext.enabled}
                />
              </div>
            </div>

            <div className="grid grid-cols-2 gap-3">
              <div className="space-y-1">
                <Label>Usuário / Ramal</Label>
                <Input
                  value={ext.user}
                  onChange={(e) => setE("user", e.target.value)}
                  autoComplete="off"
                  placeholder="1001"
                  disabled={!ext.enabled}
                />
              </div>
              <div className="space-y-1">
                <Label>Senha</Label>
                <div className="flex gap-2">
                  <Input
                    value={ext.pass}
                    onChange={(e) => setE("pass", e.target.value)}
                    type={showExtPass ? "text" : "password"}
                    autoComplete="new-password"
                    placeholder="senha do ramal"
                    disabled={!ext.enabled}
                  />
                  <Button
                    type="button"
                    variant="outline"
                    size="icon"
                    onClick={() => setShowExtPass((v) => !v)}
                    aria-label={showExtPass ? "Ocultar senha" : "Mostrar senha"}
                  >
                    {showExtPass ? <EyeOff className="h-4 w-4" /> : <Eye className="h-4 w-4" />}
                  </Button>
                </div>
              </div>
            </div>

            <div className="space-y-1">
              <Label>Ramal/destino para chamadas recebidas (opcional)</Label>
              <Input
                value={ext.dest}
                onChange={(e) => setE("dest", e.target.value)}
                placeholder="ex.: 600 (fila/ramal que toca quando chega chamada no WhatsApp)"
                disabled={!ext.enabled}
              />
            </div>

            <div className="rounded-md border border-border bg-muted/40 p-3 text-xs text-muted-foreground">
              <p className="font-medium text-foreground">Como funciona</p>
              <ul className="mt-1 list-disc space-y-0.5 pl-4">
                <li>Crie um <b>ramal</b> no seu PBX e ponha usuário/senha dele aqui.</li>
                <li>
                  <b>PBX → WhatsApp:</b> no dialplan, mande a chamada para este ramal com o número de
                  destino no formato internacional (ex.: 5511999999999).
                </li>
                <li>
                  <b>WhatsApp → PBX:</b> chamadas recebidas tocam no ramal/fila do campo acima.
                </li>
                <li>Codec <b>G.711 u-law (PCMU)</b>, transporte UDP.</li>
              </ul>
            </div>
          </div>
        )}

        {tab === "external" ? (
          <DialogFooter>
            <Button disabled={busy} onClick={() => void saveExt()}>
              {busy ? <Loader2 className="mr-1 h-4 w-4 animate-spin" /> : null}
              Salvar
            </Button>
          </DialogFooter>
        ) : null}
      </DialogContent>
    </Dialog>
  );
};
