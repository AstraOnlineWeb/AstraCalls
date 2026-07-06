// background.js — service worker da extensão AstraCalls Passkey Connector.
//
// Recebe do painel (via bridge.js) o desafio WebAuthn de uma conta com passkey e
// executa a asserção na aba do web.whatsapp.com — ÚNICA origem onde o passkey da
// conta é válido (o navegador não deixa outra origem usar credenciais de
// whatsapp.com). O dono confirma com biometria/PIN e devolvemos a assinatura.

const WA_URL = "https://web.whatsapp.com/";

chrome.runtime.onMessage.addListener((msg, _sender, sendResponse) => {
  if (!msg || typeof msg !== "object") return;

  if (msg.type === "PING") {
    sendResponse({ ok: true, version: chrome.runtime.getManifest().version });
    return false;
  }

  if (msg.type === "RUN_PASSKEY_ASSERTION") {
    runAssertion(msg.publicKey)
      .then((assertion) => sendResponse({ ok: true, assertion }))
      .catch((err) => sendResponse({ ok: false, error: String(err?.message || err) }));
    return true; // resposta assíncrona
  }
});

// runAssertion garante uma aba do web.whatsapp.com pronta, foca nela e injeta a
// rotina de asserção no MAIN world (contexto real da página).
async function runAssertion(publicKey) {
  if (!publicKey || !publicKey.challenge) {
    throw new Error("desafio passkey ausente");
  }
  const tab = await ensureWhatsAppTab();
  await chrome.tabs.update(tab.id, { active: true });
  try {
    await chrome.windows.update(tab.windowId, { focused: true });
  } catch (_) {}

  const [inj] = await chrome.scripting.executeScript({
    target: { tabId: tab.id },
    world: "MAIN",
    func: webAuthnAssertionInPage,
    args: [publicKey],
  });
  const result = inj?.result;
  if (!result) throw new Error("sem resposta da página do WhatsApp");
  if (!result.ok) throw new Error(result.error || "asserção falhou");
  return result.assertion;
}

// ensureWhatsAppTab reutiliza uma aba aberta do web.whatsapp.com ou cria uma nova
// e espera terminar de carregar.
async function ensureWhatsAppTab() {
  const existing = await chrome.tabs.query({ url: "https://web.whatsapp.com/*" });
  if (existing && existing.length > 0) return existing[0];

  const tab = await chrome.tabs.create({ url: WA_URL, active: true });
  await waitForTabComplete(tab.id);
  return tab;
}

function waitForTabComplete(tabId) {
  return new Promise((resolve) => {
    function listener(id, info) {
      if (id === tabId && info.status === "complete") {
        chrome.tabs.onUpdated.removeListener(listener);
        // pequena folga p/ o app hidratar
        setTimeout(resolve, 800);
      }
    }
    chrome.tabs.onUpdated.addListener(listener);
  });
}

// webAuthnAssertionInPage roda no MAIN world de web.whatsapp.com. Mostra um card
// AstraCalls, espera o clique do dono (gesto necessário para o WebAuthn), executa
// navigator.credentials.get e devolve a assinatura em base64url (sem padding),
// exatamente no formato que o backend espera (types.WebAuthnResponse).
function webAuthnAssertionInPage(publicKey) {
  return new Promise((resolve) => {
    const b64urlToBuf = (s) => {
      s = String(s).replace(/-/g, "+").replace(/_/g, "/");
      const pad = "=".repeat((4 - (s.length % 4)) % 4);
      const bin = atob(s + pad);
      const u = new Uint8Array(bin.length);
      for (let i = 0; i < bin.length; i++) u[i] = bin.charCodeAt(i);
      return u.buffer;
    };
    const bufToB64url = (buf) => {
      const u = new Uint8Array(buf);
      let s = "";
      for (let i = 0; i < u.length; i++) s += String.fromCharCode(u[i]);
      return btoa(s).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");
    };

    // ---- UI (card flutuante AstraCalls) ----
    const wrap = document.createElement("div");
    wrap.style.cssText =
      "position:fixed;inset:0;z-index:2147483647;display:flex;align-items:center;" +
      "justify-content:center;background:rgba(15,23,32,.55);font-family:system-ui,Segoe UI,Roboto,sans-serif";
    const card = document.createElement("div");
    card.style.cssText =
      "background:#fff;color:#233E4F;max-width:380px;width:calc(100% - 40px);border-radius:16px;" +
      "padding:26px 24px;box-shadow:0 20px 60px rgba(0,0,0,.35);text-align:center";
    card.innerHTML =
      '<div style="width:52px;height:52px;border-radius:14px;background:#233E4F;margin:0 auto 14px;' +
      'display:flex;align-items:center;justify-content:center;color:#89CFF3;font-size:26px">🔐</div>' +
      '<div style="font-size:18px;font-weight:700;margin-bottom:6px">AstraCalls — conectar conta</div>' +
      '<div style="font-size:14px;line-height:1.5;color:#4b5a67;margin-bottom:20px">' +
      "Sua conta usa <b>passkey</b>. Confirme com sua biometria ou PIN para autorizar a conexão com o AstraCalls.</div>";
    const btn = document.createElement("button");
    btn.textContent = "Confirmar com passkey";
    btn.style.cssText =
      "width:100%;border:0;border-radius:10px;padding:13px;font-size:15px;font-weight:600;cursor:pointer;" +
      "background:#233E4F;color:#fff";
    const cancel = document.createElement("button");
    cancel.textContent = "Cancelar";
    cancel.style.cssText =
      "width:100%;border:0;background:transparent;color:#8595a1;padding:12px 0 0;font-size:13px;cursor:pointer";
    const status = document.createElement("div");
    status.style.cssText = "font-size:13px;color:#c0392b;margin-top:12px;min-height:16px";
    card.appendChild(btn);
    card.appendChild(cancel);
    card.appendChild(status);
    wrap.appendChild(card);
    document.documentElement.appendChild(wrap);

    const close = () => wrap.remove();

    cancel.onclick = () => {
      close();
      resolve({ ok: false, error: "cancelado pelo usuário" });
    };

    btn.onclick = async () => {
      btn.disabled = true;
      btn.textContent = "Aguardando confirmação…";
      status.textContent = "";
      try {
        const pk = {
          challenge: b64urlToBuf(publicKey.challenge),
          rpId: publicKey.rpId,
          timeout: publicKey.timeout || 60000,
          userVerification: publicKey.userVerification || "preferred",
          allowCredentials: (publicKey.allowCredentials || []).map((c) => ({
            type: c.type || "public-key",
            id: b64urlToBuf(c.id),
            transports: c.transports,
          })),
        };
        const cred = await navigator.credentials.get({ publicKey: pk });
        const assertion = {
          id: cred.id,
          rawId: bufToB64url(cred.rawId),
          type: cred.type,
          response: {
            clientDataJSON: bufToB64url(cred.response.clientDataJSON),
            authenticatorData: bufToB64url(cred.response.authenticatorData),
            signature: bufToB64url(cred.response.signature),
            userHandle: cred.response.userHandle ? bufToB64url(cred.response.userHandle) : null,
          },
        };
        close();
        resolve({ ok: true, assertion });
      } catch (err) {
        btn.disabled = false;
        btn.textContent = "Tentar novamente";
        status.textContent = "Falha: " + String(err && err.message ? err.message : err);
      }
    };
  });
}
