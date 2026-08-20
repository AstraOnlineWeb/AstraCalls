# AstraCalls — Denúncia de site falso / malware (impersonação)

**Data da constatação:** 2026-07-26

## Resumo do incidente

O usuário GitHub `melodic-neurolinguist451` está se passando pelo projeto AstraCalls
para distribuir malware:

- **Repo copiado (fachada):** https://github.com/melodic-neurolinguist451/AstraCalls
  — cópia do código-fonte original (inclui `LICENSE.WaCalls`) só para dar aparência de legitimidade.
- **Página de isca:** https://melodic-neurolinguist451.github.io/ — falsa "AstraCalls Documentation"
  que manda o usuário Windows baixar e dar duplo-clique num executável.
- **Payload:** https://github.com/melodic-neurolinguist451/melodic-neurolinguist451.github.io/raw/refs/heads/main/gluten/github_neurolinguist_melodic_io_2.7.zip

## Evidência técnica

- **SHA256 do .zip:** `d2a957674c8b1510c27e71ac8053a6f9e8a985dcd504d290355902f04838c2f3`
- **Conteúdo do .zip:**
  - `Application.bat` — única linha: `start luau.exe mime.txt` (gatilho)
  - `luau.exe` + `lua51.dll` — interpretador Lua legítimo usado como runtime (PE32+ x86-64 Windows)
  - `mime.txt` — payload Lua fortemente ofuscado (decodificadores de string com arrays de bytes
    `\099\082...` que remontam o código malicioso só em runtime, para escapar de antivírus)
- A página também instrui a vítima a escanear QR em WhatsApp → Aparelhos conectados
  → objetivo provável: **sequestro de sessão do WhatsApp**.
- O AstraCalls real roda em Docker no servidor e NUNCA distribui executável para Windows.

---

## 1) GitHub — Report Abuse

Formulário: https://github.com/contact/report-abuse
Categorias: "Malware or security" + "Impersonation"

URLs a reportar:
- https://github.com/melodic-neurolinguist451/AstraCalls
- https://github.com/melodic-neurolinguist451/melodic-neurolinguist451.github.io
- https://melodic-neurolinguist451.github.io/
- https://github.com/melodic-neurolinguist451/melodic-neurolinguist451.github.io/raw/refs/heads/main/gluten/github_neurolinguist_melodic_io_2.7.zip

### Texto (EN):

I am the author of the original open-source project "AstraCalls"
(github.com/AstraOnlineWeb/AstraCalls). The user `melodic-neurolinguist451` is
impersonating my project to distribute malware.

**Impersonation:** The repo `melodic-neurolinguist451/AstraCalls` is a copy of my
source code (including my `LICENSE.WaCalls` file), published to give a malware
distribution page an appearance of legitimacy.

**Malware distribution:** The GitHub Pages site `melodic-neurolinguist451.github.io`
presents itself as "AstraCalls Documentation" and instructs Windows users to download
and double-click an executable from:
github.com/melodic-neurolinguist451/melodic-neurolinguist451.github.io/raw/refs/heads/main/gluten/github_neurolinguist_melodic_io_2.7.zip

The ZIP (SHA256 d2a957674c8b1510c27e71ac8053a6f9e8a985dcd504d290355902f04838c2f3)
contains:
- `Application.bat` — a launcher whose only content is `start luau.exe mime.txt`
- `luau.exe` + `lua51.dll` — a legitimate Lua interpreter used as the runtime
- `mime.txt` — a heavily obfuscated Lua payload (custom byte-array string decoders that
  reconstruct the malicious code at runtime to evade antivirus)

The page also instructs the victim to scan a QR code under WhatsApp → Linked Devices,
indicating the goal is WhatsApp session hijacking. My real project is a server-side
Docker application and never distributes any Windows executable. Please remove these
repositories/pages and the hosted payload. Thank you.

---

## 2) Google Safe Browsing

Formulário: https://safebrowsing.google.com/safebrowsing/report_phish/

URL: https://melodic-neurolinguist451.github.io/

### Details (EN):

This page impersonates the legitimate open-source "AstraCalls" project and tricks users
into downloading and running a malicious Windows executable (an obfuscated Lua loader
executed via `luau.exe`/`mime.txt`, SHA256
d2a957674c8b1510c27e71ac8053a6f9e8a985dcd504d290355902f04838c2f3). It then instructs
victims to link the app to their WhatsApp account (QR under Linked Devices), consistent
with WhatsApp session hijacking. Payload URL:
github.com/melodic-neurolinguist451/melodic-neurolinguist451.github.io/raw/refs/heads/main/gluten/github_neurolinguist_melodic_io_2.7.zip

---

## 3) Aviso para clientes / comunidade (pt-BR)

⚠️ *Alerta de golpe / site falso se passando pelo AstraCalls*

Descobrimos um site falso se passando pelo *AstraCalls* para distribuir vírus. Ele imita
nossa "documentação" e manda *baixar e instalar um programa .exe no Windows*, pedindo
depois pra escanear o QR Code em *WhatsApp → Aparelhos conectados*. *É golpe* — o objetivo
é roubar o acesso ao seu WhatsApp.

✅ *Como se proteger:*
- O AstraCalls *nunca* distribui programa pra baixar/instalar no Windows. Nosso sistema
  roda *no servidor* e é acessado *pelo navegador*.
- *Nunca* baixe "AstraCalls" de sites como melodic-neurolinguist451.github.io nem de
  qualquer link fora dos nossos canais oficiais.
- Se você baixou/executou esse arquivo: *desconecte esse aparelho* em
  *WhatsApp → Aparelhos conectados* imediatamente e rode um antivírus no computador.

🙏 *Ajude a derrubar o golpe (leva 1 minuto):*
Quanto mais gente denunciar, mais rápido o GitHub e o Google removem. Denuncie nos 3 links:

1. *Denunciar ao GitHub* (malware + impersonação):
   https://github.com/contact/report-abuse
   → Cole a URL: https://github.com/melodic-neurolinguist451/AstraCalls

2. *Denunciar a página falsa ao GitHub:*
   https://github.com/contact/report-abuse
   → Cole a URL: https://melodic-neurolinguist451.github.io/

3. *Denunciar ao Google (marca como site perigoso no Chrome):*
   https://safebrowsing.google.com/safebrowsing/report_phish/
   → Cole a URL: https://melodic-neurolinguist451.github.io/

Ao denunciar, marque as opções de *malware / software malicioso* e *impersonation
(se passar por outra pessoa/marca)*.

Na dúvida sobre um link, fale com a gente antes.
