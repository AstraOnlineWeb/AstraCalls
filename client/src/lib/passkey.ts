// passkey.ts — ponte entre o painel e a extensão "AstraCalls Passkey Connector".
//
// Contas com passkey exigem uma prova WebAuthn feita na origem web.whatsapp.com,
// o que só a extensão consegue fazer. Aqui detectamos a extensão e pedimos a
// asserção; a assinatura resultante é enviada ao backend (POST /pair-passkey).

const PANEL = "astracalls-panel";
const EXT = "astracalls-ext";

export type WebAuthnPublicKey = {
  challenge: string;
  timeout?: number;
  rpId: string;
  allowCredentials?: Array<{ id: string; type?: string; transports?: string[] }>;
  userVerification?: string;
  extensions?: Record<string, unknown>;
};

export type WebAuthnAssertion = {
  id: string;
  rawId: string;
  type: string;
  response: {
    clientDataJSON: string;
    authenticatorData: string;
    signature: string;
    userHandle: string | null;
  };
};

let extReady = false;

// a extensão anuncia presença com {source:EXT, type:"READY"} ao carregar
if (typeof window !== "undefined") {
  window.addEventListener("message", (e) => {
    if (e.source === window && (e.data?.source === EXT) && e.data?.type === "READY") {
      extReady = true;
    }
  });
}

function rid(): string {
  return "pk_" + Math.random().toString(36).slice(2) + Date.now().toString(36);
}

// isExtensionInstalled faz um PING e espera o PONG (timeout curto).
export function isExtensionInstalled(timeoutMs = 600): Promise<boolean> {
  if (extReady) return Promise.resolve(true);
  return new Promise((resolve) => {
    const requestId = rid();
    const timer = setTimeout(() => {
      window.removeEventListener("message", onMsg);
      resolve(false);
    }, timeoutMs);
    function onMsg(e: MessageEvent) {
      if (e.source !== window) return;
      const d = e.data;
      if (d?.source === EXT && d?.type === "PONG" && d?.requestId === requestId) {
        clearTimeout(timer);
        window.removeEventListener("message", onMsg);
        extReady = true;
        resolve(true);
      }
    }
    window.addEventListener("message", onMsg);
    window.postMessage({ source: PANEL, type: "PING", requestId }, window.location.origin);
  });
}

// runPasskeyAssertion pede à extensão a asserção WebAuthn para o desafio dado.
// Resolve com a assinatura (WebAuthnAssertion) ou rejeita com o erro.
export function runPasskeyAssertion(publicKey: WebAuthnPublicKey): Promise<WebAuthnAssertion> {
  return new Promise((resolve, reject) => {
    const requestId = rid();
    function onMsg(e: MessageEvent) {
      if (e.source !== window) return;
      const d = e.data;
      if (d?.source === EXT && d?.type === "PASSKEY_ASSERTION_RESULT" && d?.requestId === requestId) {
        window.removeEventListener("message", onMsg);
        if (d.ok && d.assertion) resolve(d.assertion as WebAuthnAssertion);
        else reject(new Error(d.error || "falha na asserção passkey"));
      }
    }
    window.addEventListener("message", onMsg);
    window.postMessage({ source: PANEL, type: "RUN_PASSKEY_ASSERTION", requestId, publicKey }, window.location.origin);
  });
}

// download da extensão (zip servido pelo próprio backend, na mesma origem)
export const EXTENSION_DOWNLOAD_URL = "/astracalls-passkey.zip";
