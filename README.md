<div align="center">

# 📞 AstraCalls

**Chamadas de voz e vídeo do WhatsApp em Go puro, direto do navegador — prontas para produção SaaS.**

VoIP nativo (áudio + vídeo, 1:1 e em grupo), gateway **SIP/PBX**, suíte completa de **mensagens**, **disparos**, **webhooks** confiáveis, integração com **Chatwoot** e deploy em **Docker Swarm + Traefik**.

[![Go](https://img.shields.io/badge/Go-1.26+-00ADD8?logo=go&logoColor=white)](https://go.dev)
[![React](https://img.shields.io/badge/React-19-61DAFB?logo=react&logoColor=black)](https://react.dev)
[![whatsmeow](https://img.shields.io/badge/whatsmeow-VoIP-25D366?logo=whatsapp&logoColor=white)](https://github.com/tulir/whatsmeow)
[![pion](https://img.shields.io/badge/pion-WebRTC-FF6B6B)](https://github.com/pion/webrtc)
[![PostgreSQL](https://img.shields.io/badge/PostgreSQL-16-336791?logo=postgresql&logoColor=white)](https://www.postgresql.org)
[![Docker](https://img.shields.io/badge/Docker-amd64%20%2B%20arm64-2496ED?logo=docker&logoColor=white)](https://hub.docker.com/r/astraonline/astracalls)
[![License](https://img.shields.io/badge/license-AGPL--3.0-blue.svg)](#-licença)

[Visão Geral](#-visão-geral) · [Funcionalidades](#-funcionalidades) · [Arquitetura](#-arquitetura) · [Início Rápido](#-início-rápido) · [Configuração](#-configuração) · [Deploy](#-deploy-em-produção-docker-swarm--traefik) · [API](#-api) · [Suporte](#️-suporte-profissional)

</div>

---

> **AstraCalls** é um fork de produção do [**WaCalls**](https://github.com/JotaDev66/WaCalls)
> (de [@jotadev66](https://github.com/jotadev66)). Mantém o núcleo VoIP nativo em Go e
> adiciona tudo que falta para operar como serviço: **vídeo** (1:1 e em grupo),
> **gateway SIP**, **PostgreSQL por sessão**, **API de mensagens completa**, **disparos em
> massa**, **webhooks assinados com retry/DLQ**, **integração nativa com Chatwoot** (com
> widget de chamada dentro do Chatwoot), **autenticação por API key**, **observabilidade** e
> **imagem Docker multi-arquitetura**. Os créditos do projeto original estão preservados em
> [Colaboradores](#-colaboradores).

---

## 📋 Visão Geral

O AstraCalls pareia uma ou mais contas do WhatsApp (QR code, **código de pareamento** ou
**passkey**) e permite **fazer e receber chamadas de voz e vídeo** de qualquer navegador,
ligar para **tronco/ramal SIP**, e operar a conta como canal de **mensagens** (texto, mídia,
botões, listas, carrossel, enquete, formulário, pix, produto, status…).

O microfone e a câmera do navegador chegam ao servidor Go por **WebRTC** (ou por
**WebSocket**, com fallback automático quando o WebRTC não passa no firewall). O servidor
transcodifica para os codecs do WhatsApp (**MLow** para voz, **H.264** para vídeo) e injeta
a mídia na malha de **relay SRTP** do WhatsApp — e o caminho inverso traz o áudio e o vídeo
do outro lado de volta ao navegador.

Toda a pilha VoIP roda **nativamente em Go**: codec MLow, RTP/SRTP/SRTCP, STUN, transporte
SCTP/DTLS do relay, pipeline H.264 e a sinalização `<call>`, integrados ao
[**whatsmeow**](https://github.com/tulir/whatsmeow) e servidos a um painel **React 19**. A
única dependência em C é o codec `opus_mlow` (via cgo) — e é opcional: sem ela o servidor
roda em modo **somente sinalização**.

Várias contas podem ser pareadas e operadas lado a lado, cada uma com QR, status, histórico
e banco próprios. Uma conta mantém **várias chamadas 1:1 simultâneas** (uma por atendente),
roteadas por call ID.

> **Status:** estável e em produção (última versão estável no [CHANGELOG](./CHANGELOG.md)).
> Chamada em **grupo** é funcional (áudio + vídeo, 3+ participantes) mas segue marcada como
> experimental, atrás da flag `WACALLS_GROUP_CALLS`.

---

## 🚀 Funcionalidades

### ☎️ Chamadas de voz
- **Saída e entrada 1:1** com áudio bidirecional, várias chamadas simultâneas por conta.
- **Espera (hold/resume) com música**, **atender em espera** e **transferência cega** entre
  atendentes (troca de dono + ponte, sem tocar a perna do WhatsApp).
- **Mudo / mão levantada** (`mute_v2`, `raise_hand`) nos dois sentidos.
- **Timeout de toque**, recusa que para o telefone de quem ligou, consent freshness
  (RFC 7675) — a chamada não cai sozinha aos ~20s.
- **Toque fantasma** (`POST /calls/fake`): liga e desliga antes de atender (aviso/alerta).
- **Transporte de áudio no navegador:** WebRTC com **fallback automático para WebSocket**
  (`WACALLS_DEFAULT_TRANSPORT=auto|webrtc|websocket`); WS de áudio **PCM16 full-duplex**
  (`GET /calls/{id}/ws`) pronto para agentes de voz/IA.
- **Gravação opt-in por sessão**: MP3 mono mixado dos dois lados, vira nota privada no
  Chatwoot e evento `recording` no webhook.

### 📹 Chamadas de vídeo
- **Vídeo 1:1** (câmera do atendente e do cliente), **upgrade/downgrade** no meio da
  chamada (áudio → vídeo e volta), religar a câmera, **MediaClock** (áudio e vídeo no mesmo
  relógio).
- **Orientação automática** (lê a rotação do celular no RTP e no stanza `<video>`) com
  **botão de girar** como ajuste manual, no painel e no widget.
- **Vídeo no widget do Chatwoot** (WebCodecs, data channel H.264).
- **Disparo de ligações de VÍDEO** com arquivo gravado (`POST /broadcast {video:true}`).

### 👥 Chamada em grupo *(experimental, `WACALLS_GROUP_CALLS=1`)*
- Liga **para um grupo existente** (resolve os membros) com **áudio e vídeo**; o atendente
  ouve o **mix** de todos e fala para todos; vê a **câmera de cada participante** em tiles
  rotulados com número + nome.
- Protocolo completo do grupo: offer com `group_info`, chave de **epoch compartilhada com
  rotação** a cada entrada, relay de grupo (DTLS direto, SFU com HBH-FEC), **SRTCP**
  (Sender Reports + **PLI**/pedido de keyframe nos dois sentidos), anúncio `<video state>`,
  participante que sai some do painel.
- Endpoints `POST /calls/group`, `/calls/group/end`, WS `/calls/group/ws` (áudio PCM16) e
  `/calls/group/video-ws` (H.264), `GET /calls/group/roster`.

### ☎️ Gateway SIP / PBX
- **Servidor SIP** (porta 5060 UDP/TCP) para o PBX/softphone do cliente **se registrar** no
  AstraCalls (credenciais por sessão, mostradas no painel) — tronco ↔ WhatsApp nos dois
  sentidos, áudio G.711 ↔ MLow.
- **Registro em PBX externo** (o AstraCalls vira um ramal do Asterisk/FreePBX/tronco
  hospedado), com **servidor, domínio e outbound proxy** separados, re-REGISTER automático
  e status no painel.
- RTP em **faixa fixa de portas** (`WACALLS_RTP_PORT_MIN/MAX`), publicável em `mode=host`.

### 💬 Mensagens (API estilo WAHA, ~60 endpoints)
- **Envio:** texto, imagem, áudio/nota de voz (com waveform), vídeo, **nota de vídeo (PTV)**,
  documento, sticker, contato, localização, link com preview, **enquete** (e voto),
  **evento** (e resposta), **pix (BR Code)**, **produto**, **status** (texto/imagem/vídeo/áudio).
- **Interativas que renderizam no número não-oficial:** `interactive` (CTA: abrir URL,
  copiar código, ligar, resposta rápida), `buttons`, `list`, `carousel` e **formulário
  (webview)** com respostas no Chatwoot + webhook `form_response`.
- **Mensagem que some após leitura** (`disappearing`), mensagens temporárias por chat.
- **Editar, reagir, encaminhar, marcar como lida, digitando/gravando, presença.**
- **Agendamento** (`/schedule`, persiste e sobrevive a restart), **envio idempotente**
  (`X-Message-ID`), **busca** no histórico, **download de mídia recebida**.
- **Contatos, grupos, canais, perfil, privacidade, bloqueio, proxy por sessão**.

### 📢 Disparos em massa
- `POST /blast`: campanha de texto/imagem com **pacing anti-ban**, status e cancelamento.
- `POST /broadcast`: **disparo de ligações** (áudio ou vídeo) tocando um arquivo gravado.
- `GET /restriction-status`: detecta **restrição/shadow-ban** da conta (`reachout_timelock`).

### 🔔 Webhooks confiáveis
- Eventos por sessão (`message`, `receipt`, `call_*`, `recording`, `form_response`,
  `group_participants`, `restriction`…), **filtro de eventos**, **assinatura HMAC-SHA256**
  (`X-Webhook-Signature`), **retry com backoff**, **circuit breaker**, **DLQ** com replay.

### 🤝 Chatwoot
- Contato/conversa por telefone, mensagens **WhatsApp ↔ Chatwoot** (texto + mídia) com
  **1 QR só** por número; grupos e canais (opcionais); **assinatura do atendente**,
  **always online**, **marcar lidas**; **importar histórico sob demanda**; mensagem
  editada, origem de anúncio (Click to WhatsApp), nota privada da gravação.
- **Widget de chamada** (`widget.js`): botão de telefone na conversa, toca e abre sozinho
  na chamada recebida, áudio por WebRTC ou WebSocket, **vídeo**, token de widget por conta.
- Correção do **9º dígito BR / LID** em todos os envios; sem contato "lixo" com LID.

### 🔐 Pareamento e segurança
- Pareamento por **QR**, **código** (`/pair-code`) e **passkey** (`/pair-passkey`, com
  extensão de navegador própria em `passkey-extension/`).
- **API key** (`X-API-Key`), **tokens de widget** assinados, guarda **anti-SSRF** no
  download de mídia por URL, **escopo por conta** nos eventos SSE.

### 📊 Observabilidade e infra
- `GET /livez`, `/readyz`, `/metrics` (Prometheus), versão/commit do build em
  `GET /api/config` e no painel.
- **PostgreSQL por sessão** (estilo WAHA), **imagem multi-arch** (amd64 + arm64) publicada
  pelo CI em [Docker Hub](https://hub.docker.com/r/astraonline/astracalls).

---

## 🏗️ Arquitetura

```
┌──────────────────────────────────────────────────────────────────────────────┐
│          NAVEGADOR (painel React)   +   Widget no Chatwoot   +   PBX/SIP      │
│   mic/câmera · WebRTC (Opus + H.264 DC) ou WebSocket (PCM16) · HTTP + SSE     │
└──────────────────────────────────┬───────────────────────────────┬───────────┘
                                   │ /api/sessions/{sid}/calls/…    │ SIP 5060 + RTP
                                   ▼                               ▼
┌──────────────────────────── GO SERVER (cmd/server) ──────────────────────────┐
│  SessionManager  contas (whatsmeow client + CallManager + GroupCallManager)  │
│  Broker          SSE escopado por conta (sessões, chamadas, vídeo, ações)    │
│  Bridge / WS     pion WebRTC ou WebSocket ⇄ PCM 16 kHz / H.264               │
│  SIP Gateway     sipgo (UAS + UAC), ponte RTP G.711 ⇄ PCM                    │
│  Messaging       ~60 endpoints de envio, agendamento, blast, broadcast       │
│  Webhook         retry/backoff, circuit breaker, DLQ, HMAC                   │
│  Chatwoot        integração bidirecional + widget + import de histórico      │
│  Recorder        MP3 por chamada (ffmpeg)                                    │
│                                                                              │
│  internal/wa     adaptador VoipSocket sobre o whatsmeow                      │
│  internal/voip   call (1:1 + grupo) · signaling · media (MLow, SRTP/SRTCP,   │
│                  cripto de grupo) · transport (relay SCTP/DTLS, STUN, H.264) │
└────────────────┬─────────────────────────────────────┬────────────────────────┘
                 │ sinalização <call>                   │ mídia SRTP / H.264
                 ▼                                      ▼
        ┌────────────────┐                  ┌───────────────────────────┐
        │  WhatsApp WS   │                  │  relay do WhatsApp (1:1)  │
        │  (whatsmeow)   │                  │  relay de GRUPO (SFU)     │
        └────────────────┘                  └───────────────────────────┘
                                                        │
                                             ┌─────────────────────────┐
                                             │  PostgreSQL 16          │
                                             │  wacalls_main + 1/sessão│
                                             └─────────────────────────┘
```

### Estrutura

| Caminho | Responsabilidade |
|---|---|
| `cmd/server` | HTTP/SSE, sessões + store (Postgres), ponte WebRTC/WS, auth, mensagens, agendamento, blast/broadcast, webhook, Chatwoot, gravação, **SIP** (`sip_gateway.go`, `sip_uac.go`, `sip_rtp.go`), **grupo** (`groupcall.go`) |
| `internal/wa` | `VoipSocket` — envia/recebe stanzas `<call>` via whatsmeow |
| `internal/voip/core` | Tipos de domínio, constantes, interface `VoipSocket` |
| `internal/voip/wanode` | Helpers de node WhatsApp e JID |
| `internal/voip/media` | Codec MLow, RTP, **SRTP/SRTCP**, SSRC, resampling, derivação de chaves (1:1 e **epoch de grupo**) |
| `internal/voip/transport` | Relay SCTP (1:1), **relay de grupo** (DTLS direto + Allocate de grupo), STUN, empacotamento H.264 |
| `internal/voip/signaling` | Build/parse de `<call>` (offer/accept/vídeo/grupo/rekey), cripto da call-key |
| `internal/voip/call` | `CallManager` (1:1) e `GroupCallManager` (grupo), pipeline de vídeo, mixer de áudio |
| `client/` | Painel React 19 + Vite + Tailwind v4 + shadcn/ui (discador, chamadas, vídeo, grupo, sessões, SIP, Chatwoot, histórico, login) |
| `client/public/widget.js` | Widget de chamada (áudio + vídeo) embutível no Chatwoot |
| `client/public/openapi.yaml` · `api-docs.html` | Documentação OpenAPI/Swagger da API (`/api-docs.html` no servidor) |
| `passkey-extension/` | Extensão Chrome para pareamento por passkey |

---

## ⚙️ Início Rápido

```bash
git clone https://github.com/AstraOnlineWeb/AstraCalls.git
cd AstraCalls
git submodule update --init          # codec opus_mlow (necessário p/ áudio ao vivo)

go mod download                      # dependências Go
cd client && npm install && cd ..    # dependências do painel React
```

É preciso um **PostgreSQL** (usuário com `CREATEDB`):

```bash
export WACALLS_PG_URL='postgres://wacalls:senha@127.0.0.1:5432/postgres?sslmode=disable'
```

### Rodar (somente sinalização — sem compilador C; pareia e chama, sem áudio)

```bash
go run ./cmd/server -addr :8080          # adicione -debug para logs verbosos
```

### Rodar (áudio ao vivo — codec MLow nativo via cgo)

```bash
CGO_ENABLED=1 \
CGO_LDFLAGS="-L$PWD/native -Wl,-rpath,$PWD/native" \
go run -tags mlow ./cmd/server -addr :8080 -debug
```

> O áudio real exige cgo + `native/libopus_mlow.so` (compilada de
> [opus_mlow](https://github.com/edgardmessias/opus_mlow)). A imagem Docker já vem com tudo.

Abra `http://localhost:8080`, clique em **Nova sessão** e escaneie o QR em
**WhatsApp → Aparelhos conectados** (ou use código de pareamento / passkey).

### Painel React em modo dev

```bash
cd client
npm run dev      # Vite na :5173, faz proxy de /api → http://localhost:8080
```

---

## 🔧 Configuração

### Flags

| Flag | Padrão | Significado |
|---|---|---|
| `-addr` | `:8080` | Endereço HTTP |
| `-pg-url` / `-pg-namespace` | env | URL do Postgres e prefixo dos bancos por sessão |
| `-static` | `client/dist` | Diretório do painel estático |
| `-debug` | `false` | Logs verbosos (inclui o log interno do whatsmeow) |
| `-max-calls-per-session` | `8` | Chamadas simultâneas por sessão (`0` = ilimitado) |

### Variáveis de ambiente

| Env | Padrão | Significado |
|---|---|---|
| `WACALLS_PG_URL` | — | URL de manutenção do Postgres (usuário com `CREATEDB`) |
| `WACALLS_PG_NAMESPACE` | `wacalls` | Prefixo dos bancos (`wacalls_main` + `wacalls_<id>`) |
| `WACALLS_API_KEY` | — | Se setada, exige `X-API-Key` em `/api/*` (**use em produção**) |
| `WACALLS_PUBLIC_IP` | — | IP público p/ NAT 1:1 / ICE-TCP / SDP do SIP (`auto` detecta) |
| `WACALLS_UDP_PORT` | — | Porta de mídia WebRTC (UDP + ICE-TCP) |
| `WACALLS_DEFAULT_TRANSPORT` | `auto` | Transporte de áudio do navegador: `auto` (WebRTC → fallback WS), `webrtc`, `websocket` |
| `WACALLS_MAX_CALLS` | `8` | Equivalente a `-max-calls-per-session` |
| `WACALLS_RING_TIMEOUT_SECONDS` | — | Expira chamada recebida que nunca é encerrada |
| `WACALLS_RECORDING_DIR` | `$TMPDIR/wacalls-recordings` | Onde ficam os MP3s (retenção ~48h) |
| `WACALLS_PUBLIC_BASE_URL` | — | Base pública p/ URLs de gravação, formulários e widget (ex.: `https://call.seudominio.com`) |
| `WACALLS_WIDGET_KEY` | — | Chave-mestra para emitir tokens de widget (`POST /api/widget-tokens`) |
| `WACALLS_WIDGET_SIGNING_SECRET` | — | Segredo de assinatura dos tokens de widget |
| `WACALLS_SIP_ADDR` | `0.0.0.0:5060` | Endereço do servidor SIP (UDP + TCP) |
| `WACALLS_SIP_ADVERTISE_PORT` | `5060` | Porta SIP anunciada no `Contact` (registro em PBX externo) |
| `WACALLS_RTP_PORT_MIN` / `_MAX` | `40000` / `40049` | Faixa fixa de portas RTP do SIP (publique em `mode=host`) |
| `WACALLS_SIP_DEBUG` | off | Logs detalhados de mídia SIP/grupo (**só para diagnóstico** — gera muito log) |
| `WACALLS_GROUP_CALLS` | off | `1` liga a chamada em grupo (experimental) |

> **Gravação de chamada (opt-in por sessão).** Ligue em `PUT /api/sessions/{sid}/recording {"enabled":true}`
> (ou pelo toggle "Gravar" no painel). O áudio dos dois lados é mixado num MP3 mono 16 kHz
> que vira **nota privada** no Chatwoot e evento `recording` no webhook; o arquivo fica em
> `GET /recordings/{callId}.mp3`. Requer `ffmpeg` (já na imagem).

---

## 🐳 Deploy em produção (Docker Swarm + Traefik)

A imagem oficial é **multi-arquitetura** (`linux/amd64` + `linux/arm64`) e é publicada pelo
CI a cada push: `astraonline/astracalls:develop` (contínua) e `astraonline/astracalls:vX.Y.Z`
+ `:latest` (releases).

```bash
# deploy da stack (Postgres + servidor em rede de host + proxy Traefik)
docker stack deploy -c sua-stack.yml astracalls

# atualizar para a imagem mais nova
docker service update --image astraonline/astracalls:develop --force astracalls_astracalls
```

Notas de produção:
- O servidor roda em **rede de host** para a mídia WebRTC/SIP enxergar a interface real.
- Um serviço **socat** com labels do Traefik publica o HTTP em **HTTPS** (necessário porque
  `getUserMedia` só funciona em contexto seguro).
- O **PostgreSQL** dedicado escuta apenas em `127.0.0.1` (não exposto à internet).
- Defina `WACALLS_PUBLIC_IP` (ou `auto`), `WACALLS_UDP_PORT`, `WACALLS_PG_URL`,
  `WACALLS_PUBLIC_BASE_URL` e uma `WACALLS_API_KEY` forte.
- Para o **SIP**, publique `5060/udp`, `5060/tcp` e a faixa `WACALLS_RTP_PORT_MIN..MAX/udp`
  em `mode=host`, e aponte `WACALLS_PUBLIC_IP` para o IP real da máquina.
- Em nuvens que bloqueiam UDP de entrada, o **ICE-TCP** (WebRTC) e o transporte
  **WebSocket** (`WACALLS_DEFAULT_TRANSPORT=auto`) mantêm o áudio funcionando.
- Para buildar a própria imagem: `docker build -t astraonline/astracalls:develop .`
  (o Dockerfile compila o codec a partir do submódulo `opus_mlow`).

---

## 🔌 API

A documentação completa (OpenAPI 3, **140 rotas**) é servida pelo próprio servidor em
**`/api-docs.html`** (Swagger UI) e está em [`client/public/openapi.yaml`](./client/public/openapi.yaml).

Todas as rotas são escopadas por sessão (`/api/sessions/{sid}/…`). Os eventos chegam por um
único canal SSE (`GET /api/events?clientId=…`), marcados com o `sessionId` de origem. Se
`WACALLS_API_KEY` estiver setada, envie `X-API-Key` (ou `?apiKey=` no SSE/WS).

| Área | Rotas (resumo) |
|---|---|
| **Sessões** | `GET/POST /api/sessions`, `DELETE /{sid}`, `/logout`, `/pair`, `/pair-code`, `/pair-passkey`, `/proxy`, `/recording`, `/restriction-status` |
| **Chamadas** | `POST /calls` (`{phone, video?, record?}`), `/calls/{id}/webrtc` (SDP), `GET /calls/{id}/ws` (PCM16), `/accept`, `/reject`, `DELETE /calls/{id}`, `/hold`, `/resume`, `/pickup`, `/transfer`, `/mute`, `/hand`, `/video/{request\|accept\|reject\|stop}`, `POST /calls/fake`, `GET /calls`, `GET /history` |
| **Grupo** | `POST /calls/group`, `POST /calls/group/end`, `GET /calls/group/ws`, `GET /calls/group/video-ws`, `GET /calls/group/roster` |
| **Disparos** | `POST /broadcast` (ligações, áudio/vídeo), `POST /blast` + `GET /blasts`, `/blasts/{id}`, `/blasts/{id}/cancel` |
| **Mensagens** | `POST /messages/{text\|image\|audio\|video\|ptv\|document\|sticker\|contact\|location\|link-preview\|poll\|poll-vote\|event\|event-response\|pix\|product\|product-native\|interactive\|buttons\|list\|carousel\|form\|disappearing\|seen\|typing\|forward}`, `PUT /messages/{edit\|react}`, `GET /messages`, `/messages/search`, `/messages/{id}/media`, `/messages/new-message-id`, `DELETE /messages`, `/schedule` |
| **Status / presença** | `POST /status/{text\|image\|video\|audio}`, `POST /presence`, `/presence/{jid}/subscribe` |
| **Contatos / grupos / canais** | `GET /contacts`, `/contacts/check`, `/contacts/{jid}`, `/picture`, `/business`, `/block`, `/unblock`, `GET /blocklist`; `GET/POST /groups`, `/groups/{gid}/…` (participantes, convite, pedidos, configurações, foto, assunto, descrição, sair); `GET /channels`, `/channels/{id}/…` (seguir, mutar, mensagens) |
| **Chats / perfil / privacidade** | `GET /chats`, `/chats/{chatId}/messages`, `PUT /chats/{chatId}/disappearing`, `PUT /disappearing`; `GET /profile`, `/profile/qr`, `PUT /profile/status`; `GET/PUT /privacy`, `GET /privacy/status` |
| **Webhooks** | `GET/POST/DELETE /webhook` (eventos, `secret` HMAC), `GET /webhooks/status`, `/webhooks/dlq`, `POST /webhooks/dlq/{id}/replay`, `/webhooks/reenable` |
| **Chatwoot** | `GET/POST/DELETE /chatwoot`, `POST /chatwoot/webhook`, `/chatwoot/import-history`, `/chatwoot/groups/{gid}/open`, `/chatwoot/channels/{id}/open`, `GET /api/chatwoot/resolve` |
| **SIP** | `GET /api/sip/status`, `GET/POST /sip` (credenciais p/ o PBX registrar aqui), `GET/POST /sip-ext` (registrar num PBX externo: host, porta, usuário, senha, ramal, proxy) |
| **Widget / formulários** | `GET /api/widget-key`, `POST /api/widget-tokens`, `GET /forms/{token}`, `POST /forms/{token}/submit` (públicas, token como capability) |
| **Infra** | `GET /api/config` (flags, versão, commit), `GET /livez`, `/readyz`, `/metrics`, `GET /recordings/{id}` |

---

## 🧪 Testes

```bash
go test ./...                 # mídia (SRTP/SRTCP, STUN, RTP, H.264, cripto de grupo), sinalização, servidor
cd client && npm run build    # type-check + build de produção do painel
```

---

## 🔒 Segurança

- Em produção, **sempre** defina `WACALLS_API_KEY` — sem ela qualquer um com acesso HTTP
  pode criar contas, fazer chamadas, enviar mensagens e ler histórico.
- O banco de cada sessão guarda **credenciais do WhatsApp**. Mantenha o Postgres protegido
  e fora da internet.
- Exponha sempre por **HTTPS** (`getUserMedia` exige contexto seguro).
- Use `secret` no webhook e verifique `X-Webhook-Signature` no consumidor.
- O SIP na 5060 recebe varredura da internet o tempo todo: só sessões com credenciais
  válidas registram (Digest MD5); considere restringir a porta por firewall aos IPs dos
  PBXs.

---

## 👥 Colaboradores

O AstraCalls é construído sobre o excelente trabalho da equipe do **WaCalls**. Todos os
créditos do projeto original:

<div align="center">

<a href="https://github.com/jotadev66"><img src="https://github.com/jotadev66.png" width="72" height="72" style="border-radius:50%" alt="jotadev66"/></a>
<a href="https://github.com/edgardmessias"><img src="https://github.com/edgardmessias.png" width="72" height="72" style="border-radius:50%" alt="edgardmessias"/></a>
<a href="https://github.com/w3nder"><img src="https://github.com/w3nder.png" width="72" height="72" style="border-radius:50%" alt="w3nder"/></a>
<a href="https://github.com/purpshell"><img src="https://github.com/purpshell.png" width="72" height="72" style="border-radius:50%" alt="purpshell"/></a>

[**@jotadev66**](https://github.com/jotadev66) · [**@edgardmessias**](https://github.com/edgardmessias) · [**@w3nder**](https://github.com/w3nder) · [**@purpshell**](https://github.com/purpshell)

**Projeto original:** [WaCalls](https://github.com/JotaDev66/WaCalls)

</div>

---

## 🙏 Agradecimentos

- [**whatsmeow**](https://github.com/tulir/whatsmeow) — biblioteca Go do protocolo WhatsApp Web
- [**pion/webrtc**](https://github.com/pion/webrtc) — pilha WebRTC em Go puro (ICE + DTLS + SCTP)
- [**opus_mlow**](https://github.com/edgardmessias/opus_mlow) — codec MLow nativo
- [**meowcaller**](https://github.com/purpshell/meowcaller) — referência do motor de chamadas (inclusive **grupo**: epoch, relay SFU, SRTCP)
- [**zapo**](https://github.com/w3nder/zapo) — referência da pilha de mídia (MediaClock, mute/raise hand)
- [**sipgo**](https://github.com/emiago/sipgo) — pilha SIP em Go
- [**WAHA**](https://github.com/devlikeapro/waha) — inspiração para o storage por sessão, a API de mensagens e a integração Chatwoot

---

## 🛠️ Suporte Profissional

Precisa de ajuda para melhorar, customizar ou implementar o projeto?

📱 **WhatsApp:** +55 61 9 9687-8959

💼 Temos uma equipe especializada para:

✅ Customizações e melhorias
✅ Implementação e deploy completo
✅ Configuração de arquitetura SaaS
✅ Integração com outras APIs
✅ Desenvolvimento de features específicas
✅ Suporte técnico dedicado
✅ Consultoria em automação WhatsApp
✅ Treinamento e documentação

---

## 📄 Licença

O AstraCalls é distribuído sob a licença **GNU AGPL-3.0** — veja [LICENSE](./LICENSE).

Isso significa que qualquer uso em rede (inclusive SaaS) exige disponibilizar o
código-fonte das modificações aos usuários do serviço.

O AstraCalls é um fork do [WaCalls](https://github.com/JotaDev66/WaCalls), que é
licenciado sob **MIT**. Conforme exigido pela MIT, o aviso de copyright original
(© 2026 jotadev66) é preservado em [LICENSE.WaCalls](./LICENSE.WaCalls). As porções
originais permanecem sob os termos MIT; o trabalho derivado, como um todo, é
licenciado sob AGPL-3.0.
