import { useEffect, useRef, useState } from "react";
import { Loader2, ShieldCheck, KeyRound, QrCode, Smartphone } from "lucide-react";
import { QRCodeSVG } from "qrcode.react";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Skeleton } from "@/components/ui/skeleton";
import { useSessions, setPairingCode } from "@/stores/sessions";
import { submitPasskeyAssertion, pairSessionCode } from "@/services/sessions";
import {
  isExtensionInstalled,
  runPasskeyAssertion,
  EXTENSION_DOWNLOAD_URL,
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
            Instale a extensão no navegador do dono da conta para autorizar a conexão:
          </p>
          <ol className="mx-auto max-w-xs space-y-1 text-left text-sm text-muted-foreground">
            <li>1. Baixe e descompacte o arquivo abaixo</li>
            <li>
              2. Abra <code className="rounded bg-muted px-1">chrome://extensions</code> e ative o
              <b> Modo do desenvolvedor</b>
            </li>
            <li>3. Clique em <b>Carregar sem compactação</b> e escolha a pasta</li>
            <li>4. Volte aqui e reative a conexão</li>
          </ol>
          <Button asChild>
            <a href={EXTENSION_DOWNLOAD_URL} download>
              Baixar extensão (.zip)
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

const CodeStep = ({ session }: { session: SessionInfo }) => {
  const code = useSessions((s) => s.codes[session.id]);
  const [phone, setPhone] = useState("");
  const [status, setStatus] = useState<"idle" | "loading" | "error">("idle");
  const [error, setError] = useState("");

  const digits = phone.replace(/\D/g, "");
  const canSubmit = digits.length >= 10 && status !== "loading";

  const generate = async () => {
    if (!canSubmit) return;
    setStatus("loading");
    setError("");
    try {
      const { code: c } = await pairSessionCode(session.id, digits);
      setPairingCode(session.id, c);
      setStatus("idle");
    } catch (e) {
      setStatus("error");
      setError(e instanceof Error ? e.message : String(e));
    }
  };

  const pretty = code ? (code.length === 8 ? `${code.slice(0, 4)}-${code.slice(4)}` : code) : "";

  return (
    <div className="flex w-full flex-col items-center gap-4 text-center">
      {code ? (
        <>
          <p className="text-sm text-muted-foreground">
            No WhatsApp do número, abra <b>Aparelhos conectados → Conectar aparelho →
            Conectar com número de telefone</b> e digite:
          </p>
          <div className="rounded-xl border bg-muted/50 px-6 py-4">
            <span className="font-mono text-3xl font-bold tracking-[0.25em]">{pretty}</span>
          </div>
          <Badge variant="muted" className="gap-1.5">
            <Loader2 className="h-3 w-3 animate-spin" /> Aguardando confirmação no aparelho…
          </Badge>
          <Button variant="ghost" size="sm" onClick={() => void generate()} disabled={status === "loading"}>
            Gerar novo código
          </Button>
        </>
      ) : (
        <>
          <p className="text-sm text-muted-foreground">
            Informe o número com DDI e DDD (ex.: <code className="rounded bg-muted px-1">5511999999999</code>)
            para receber um código de conexão.
          </p>
          <Input
            type="tel"
            inputMode="numeric"
            placeholder="5511999999999"
            value={phone}
            onChange={(e) => setPhone(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === "Enter") void generate();
            }}
            className="text-center"
          />
          {status === "error" && <Badge variant="destructive">Falha: {error}</Badge>}
          <Button onClick={() => void generate()} disabled={!canSubmit} className="w-full gap-1.5">
            {status === "loading" ? (
              <>
                <Loader2 className="h-4 w-4 animate-spin" /> Gerando…
              </>
            ) : (
              <>
                <Smartphone className="h-4 w-4" /> Gerar código
              </>
            )}
          </Button>
        </>
      )}
    </div>
  );
};

export const SessionPairing = ({ session }: { session: SessionInfo }) => {
  const qr = useSessions((s) => s.qrs[session.id]);
  const hasCode = useSessions((s) => !!s.codes[session.id]);
  const isPasskey = session.state === "passkey_request";
  const [mode, setMode] = useState<"qr" | "code">(
    session.state === "pairing_code" || hasCode ? "code" : "qr",
  );

  return (
    <div className="flex min-h-[55vh] items-center justify-center">
      <Card className="w-full max-w-md">
        <CardHeader className="items-center text-center">
          <CardTitle>Conectar {session.name}</CardTitle>
          {!isPasskey && mode === "qr" && (
            <CardDescription>
              Abra o WhatsApp → Aparelhos conectados → Conectar aparelho e escaneie.
            </CardDescription>
          )}
        </CardHeader>
        <CardContent className="flex flex-col items-center gap-4">
          {isPasskey ? (
            <PasskeyStep session={session} />
          ) : (
            <>
              {/* Seletor de método: QR ou código por número */}
              <div className="flex w-full rounded-lg border p-1">
                <button
                  type="button"
                  onClick={() => setMode("qr")}
                  className={`flex flex-1 items-center justify-center gap-1.5 rounded-md py-1.5 text-sm font-medium transition ${
                    mode === "qr" ? "bg-primary text-primary-foreground" : "text-muted-foreground hover:text-foreground"
                  }`}
                >
                  <QrCode className="h-4 w-4" /> QR Code
                </button>
                <button
                  type="button"
                  onClick={() => setMode("code")}
                  className={`flex flex-1 items-center justify-center gap-1.5 rounded-md py-1.5 text-sm font-medium transition ${
                    mode === "code" ? "bg-primary text-primary-foreground" : "text-muted-foreground hover:text-foreground"
                  }`}
                >
                  <Smartphone className="h-4 w-4" /> Código
                </button>
              </div>

              {mode === "code" ? (
                <CodeStep session={session} />
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
            </>
          )}
        </CardContent>
      </Card>
    </div>
  );
};
