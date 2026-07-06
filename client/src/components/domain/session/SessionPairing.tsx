import { useEffect, useRef, useState } from "react";
import { Loader2, ShieldCheck, KeyRound } from "lucide-react";
import { QRCodeSVG } from "qrcode.react";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import { useSessions } from "@/stores/sessions";
import { submitPasskeyAssertion } from "@/services/sessions";
import {
  isExtensionInstalled,
  runPasskeyAssertion,
  EXTENSION_HELP_URL,
} from "@/lib/passkey";
import type { SessionInfo } from "@/types/session";

const PasskeyStep = ({ session }: { session: SessionInfo }) => {
  const publicKey = useSessions((s) => s.passkeys[session.id]);
  const [hasExt, setHasExt] = useState<boolean | null>(null);
  const [status, setStatus] = useState<"idle" | "running" | "error">("idle");
  const [error, setError] = useState("");
  const startedFor = useRef<string | null>(null);

  useEffect(() => {
    void isExtensionInstalled().then(setHasExt);
  }, []);

  const authorize = async () => {
    if (!publicKey) return;
    setStatus("running");
    setError("");
    try {
      const assertion = await runPasskeyAssertion(publicKey);
      await submitPasskeyAssertion(session.id, assertion);
      // sucesso final chega via auth-state (state="open")
    } catch (e) {
      setStatus("error");
      setError(e instanceof Error ? e.message : String(e));
      return;
    }
    setStatus("idle");
  };

  // dispara automaticamente uma vez quando a extensão está presente e há desafio
  useEffect(() => {
    if (hasExt && publicKey && startedFor.current !== session.id && status === "idle") {
      startedFor.current = session.id;
      void authorize();
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [hasExt, publicKey, session.id]);

  return (
    <div className="flex flex-col items-center gap-4 text-center">
      <div className="flex h-14 w-14 items-center justify-center rounded-2xl bg-primary text-primary-foreground">
        <KeyRound className="h-7 w-7" />
      </div>
      <div>
        <p className="font-semibold">Esta conta usa passkey</p>
        <p className="text-sm text-muted-foreground">
          O WhatsApp exige uma confirmação de segurança do dono da conta para conectar.
        </p>
      </div>

      {hasExt === false ? (
        <div className="space-y-3">
          <Badge variant="destructive">Extensão AstraCalls Passkey não encontrada</Badge>
          <p className="text-sm text-muted-foreground">
            Instale a extensão no navegador do dono da conta para autorizar a conexão.
          </p>
          <Button asChild variant="outline">
            <a href={EXTENSION_HELP_URL} target="_blank" rel="noreferrer">
              Como instalar a extensão
            </a>
          </Button>
        </div>
      ) : status === "running" ? (
        <Badge variant="muted" className="gap-1.5">
          <Loader2 className="h-3 w-3 animate-spin" /> Aguardando confirmação na aba do WhatsApp…
        </Badge>
      ) : status === "error" ? (
        <div className="space-y-3">
          <Badge variant="destructive">Falha: {error}</Badge>
          <Button onClick={() => void authorize()} className="gap-1.5">
            <ShieldCheck className="h-4 w-4" /> Tentar novamente
          </Button>
        </div>
      ) : (
        <Button onClick={() => void authorize()} className="gap-1.5">
          <ShieldCheck className="h-4 w-4" /> Confirmar com passkey
        </Button>
      )}
    </div>
  );
};

export const SessionPairing = ({ session }: { session: SessionInfo }) => {
  const qr = useSessions((s) => s.qrs[session.id]);
  const isPasskey = session.state === "passkey_request";

  return (
    <div className="flex min-h-[55vh] items-center justify-center">
      <Card className="w-full max-w-md">
        <CardHeader className="items-center text-center">
          <CardTitle>Conectar {session.name}</CardTitle>
          {!isPasskey && (
            <CardDescription>
              Abra o WhatsApp → Aparelhos conectados → Conectar aparelho e escaneie.
            </CardDescription>
          )}
        </CardHeader>
        <CardContent className="flex flex-col items-center gap-4">
          {isPasskey ? (
            <PasskeyStep session={session} />
          ) : qr ? (
            <div className="rounded-lg border bg-white p-3">
              <QRCodeSVG value={qr} size={232} marginSize={1} />
            </div>
          ) : session.state === "logged_out" ? (
            <Badge variant="destructive">Desconectado — use Reativar acima para gerar um QR</Badge>
          ) : (
            <>
              <Skeleton className="h-[258px] w-[258px] rounded-lg" />
              <Badge variant="muted" className="gap-1.5">
                <Loader2 className="h-3 w-3 animate-spin" /> Aguardando QR…
              </Badge>
            </>
          )}
        </CardContent>
      </Card>
    </div>
  );
};
