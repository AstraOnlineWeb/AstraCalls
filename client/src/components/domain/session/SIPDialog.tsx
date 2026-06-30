import { useState } from "react";
import { Phone, Copy, Check, Eye, EyeOff } from "lucide-react";
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
  DialogDescription,
  DialogTrigger,
} from "@/components/ui/dialog";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import type { SessionInfo } from "@/types/session";

const SIP_PORT = 5060;

type Field = "user" | "pass" | "server" | "domain";

export const SIPDialog = ({ session }: { session: SessionInfo }) => {
  const [open, setOpen] = useState(false);
  const [showPass, setShowPass] = useState(false);
  const [copied, setCopied] = useState<Field | null>(null);

  const sipUser = session.sip_user || "";
  const sipPass = session.sip_pass || "";
  const server =
    session.sip_url && session.sip_url !== "127.0.0.1:5060"
      ? session.sip_url
      : `${window.location.hostname}:${SIP_PORT}`;
  const domain = server.split(":")[0];

  const copy = (field: Field, value: string) => {
    navigator.clipboard?.writeText(value);
    setCopied(field);
    setTimeout(() => setCopied(null), 1500);
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

  return (
    <Dialog open={open} onOpenChange={setOpen}>
      <DialogTrigger asChild>
        <Button variant="outline" size="sm">
          <Phone className="h-4 w-4" />
          SIP
        </Button>
      </DialogTrigger>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>Integração SIP / PBX</DialogTitle>
          <DialogDescription>
            Configure um <b>tronco</b> no seu Asterisk/FreePBX (ou um ramal no softphone) que se
            registra neste servidor com as credenciais abaixo. Chamadas discadas pelo tronco saem
            pelo WhatsApp desta sessão.
          </DialogDescription>
        </DialogHeader>

        <div className="space-y-3">
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
      </DialogContent>
    </Dialog>
  );
};
