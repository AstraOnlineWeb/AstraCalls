// bridge.js — roda no painel do AstraCalls (call.trecofantastico.com.br).
// Faz a ponte entre a página (window.postMessage) e a extensão (chrome.runtime).
// Não lê nada do painel; só encaminha o pedido de asserção passkey e devolve o
// resultado. A asserção WebAuthn em si NÃO acontece aqui — acontece na aba do
// web.whatsapp.com (ver background.js), única origem onde o passkey é válido.

(() => {
  const PANEL = "astracalls-panel"; // mensagens vindas do painel
  const EXT = "astracalls-ext"; // mensagens que enviamos ao painel

  const version = chrome.runtime.getManifest().version;

  function toPanel(msg) {
    window.postMessage({ source: EXT, ...msg }, window.location.origin);
  }

  // avisa o painel que a extensão está presente (para habilitar o fluxo passkey)
  toPanel({ type: "READY", version });

  window.addEventListener("message", (event) => {
    // só aceita mensagens desta própria janela e do painel
    if (event.source !== window) return;
    const data = event.data;
    if (!data || data.source !== PANEL) return;

    if (data.type === "PING") {
      toPanel({ type: "PONG", requestId: data.requestId, version });
      return;
    }

    if (data.type === "RUN_PASSKEY_ASSERTION") {
      const requestId = data.requestId;
      chrome.runtime.sendMessage(
        { type: "RUN_PASSKEY_ASSERTION", publicKey: data.publicKey },
        (resp) => {
          if (chrome.runtime.lastError) {
            toPanel({
              type: "PASSKEY_ASSERTION_RESULT",
              requestId,
              ok: false,
              error: chrome.runtime.lastError.message || "extensão indisponível",
            });
            return;
          }
          toPanel({
            type: "PASSKEY_ASSERTION_RESULT",
            requestId,
            ok: !!resp?.ok,
            assertion: resp?.assertion,
            error: resp?.error,
          });
        }
      );
    }
  });
})();
