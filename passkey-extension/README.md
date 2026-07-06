# AstraCalls Passkey Connector

Extensão Chrome (Manifest V3) que autoriza a conexão de contas WhatsApp
protegidas por **passkey (WebAuthn)** no AstraCalls.

## Por que ela é necessária

O WhatsApp passou a exigir uma prova WebAuthn (biometria/PIN/chave de segurança)
do dono ao vincular um dispositivo em algumas contas. Essa prova **só pode ser
feita na origem `web.whatsapp.com`** — nenhuma outra página pode usar a passkey
da conta. Como o painel do AstraCalls roda em outro domínio, ele delega essa
única operação para esta extensão, que a executa na aba do WhatsApp Web.

A extensão **não lê nada** da conta nem da sessão do WhatsApp Web: apenas executa
a asserção WebAuthn quando o AstraCalls pede e devolve a assinatura.

## Como funciona (fluxo)

1. No AstraCalls, o usuário inicia a conexão da conta (QR ou código).
2. Se a conta exige passkey, o backend expõe o desafio WebAuthn ao painel.
3. O painel (via `bridge.js`) pede a asserção à extensão.
4. A extensão abre/foca `web.whatsapp.com`, mostra um card AstraCalls e o dono
   confirma com biometria/PIN.
5. A assinatura volta ao painel → `POST /api/sessions/{id}/pair-passkey` → conecta.

## Arquivos

- `manifest.json` — MV3 (permissões `scripting`, `tabs`; hosts `web.whatsapp.com`
  e o domínio do painel).
- `background.js` — service worker: recebe o pedido, abre a aba do WhatsApp e
  injeta a asserção no MAIN world (com card de confirmação).
- `bridge.js` — content script no painel: ponte `window.postMessage` ↔ extensão.
- `popup.html` / `popup.js` — status/marca.
- `icons/` — ícones (gerados do favicon do AstraCalls).

## Instalar (modo desenvolvedor / distribuição privada)

1. Abra `chrome://extensions` no Chrome/Edge do **dono da conta**.
2. Ative o **Modo do desenvolvedor** (canto superior direito).
3. Clique em **Carregar sem compactação** e selecione esta pasta.
4. Pronto — ao conectar uma conta com passkey no AstraCalls, o card aparece.

Para distribuir empacotado: `chrome://extensions` → **Compactar extensão** gera um
`.crx` + chave. Recomenda-se distribuição **privada** (não publicar em loja).

## Ajustes

- Domínio do painel: edite `host_permissions` e o `matches` de `content_scripts`
  no `manifest.json` se o painel não estiver em `call.trecofantastico.com.br`.
- O protocolo `postMessage` (`astracalls-panel` ↔ `astracalls-ext`) é consumido
  por `client/src/lib/passkey.ts` no painel.
