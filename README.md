<div align="center">

# 📞 AstraCalls

**Chamadas de voz do WhatsApp em Go puro, direto do navegador — agora prontas para produção SaaS.**

Mídia VoIP nativa, multi-conta (multi-sessão), API de mensagens, webhooks, integração com **Chatwoot** e deploy em **Docker Swarm + Traefik**.

[![Go](https://img.shields.io/badge/Go-1.26+-00ADD8?logo=go&logoColor=white)](https://go.dev)
[![React](https://img.shields.io/badge/React-19-61DAFB?logo=react&logoColor=black)](https://react.dev)
[![whatsmeow](https://img.shields.io/badge/whatsmeow-VoIP-25D366?logo=whatsapp&logoColor=white)](https://github.com/tulir/whatsmeow)
[![pion](https://img.shields.io/badge/pion-WebRTC-FF6B6B)](https://github.com/pion/webrtc)
[![PostgreSQL](https://img.shields.io/badge/PostgreSQL-16-336791?logo=postgresql&logoColor=white)](https://www.postgresql.org)
[![Docker](https://img.shields.io/badge/Docker-Swarm-2496ED?logo=docker&logoColor=white)](https://docs.docker.com/engine/swarm/)
[![License](https://img.shields.io/badge/license-AGPL--3.0-blue.svg)](#-licença)

[Visão Geral](#-visão-geral) · [Novidades](#-o-que-o-astracalls-adiciona) · [Arquitetura](#-arquitetura) · [Início Rápido](#-início-rápido) · [Deploy](#-deploy-em-produção-docker-swarm--traefik) · [API](#-api) · [Suporte](#️-suporte-profissional)

</div>

---

> **AstraCalls** é um fork de produção do [**WaCalls**](https://github.com/JotaDev66/WaCalls)
> (de [@jotadev66](https://github.com/jotadev66)). Mantém todo o núcleo VoIP nativo em Go e
> adiciona a camada que faltava para operar como serviço: **PostgreSQL por sessão**,
> **API de mensagens**, **webhooks**, **integração nativa com Chatwoot** (inclusive um
> **widget de chamada dentro do Chatwoot**), **autenticação por API key** e **deploy
> containerizado em Docker Swarm com Traefik/HTTPS**. Todos os créditos do projeto original
> estão preservados em [Colaboradores](#-colaboradores).

---

## 📋 Visão Geral

O AstraCalls pareia uma ou mais contas do WhatsApp via **QR code** e permite **fazer e
receber chamadas de voz 1:1** de qualquer navegador. O microfone do navegador é enviado
por **WebRTC (Opus)** para o servidor Go, que transcodifica para o codec **MLow** da Meta
e injeta a mídia na malha de **relay SRTP** do WhatsApp — e o caminho inverso traz o áudio
do outro lado de volta ao navegador.

Toda a pilha VoIP roda **nativamente em Go**: o codec de voz MLow, a empacotagem
**RTP/SRTP**, **STUN**, o transporte **WebRTC/SCTP relay** e a sinalização `<call>`,
integrados ao [**whatsmeow**](https://github.com/tulir/whatsmeow) e servidos a um cliente
**React 19**. A única dependência em C é o codec `opus_mlow` (via cgo) — e ela é opcional:
sem ela o servidor roda em modo **somente sinalização** (pareamento e setup de chamada
funcionam; sem áudio ao vivo).

Várias contas do WhatsApp podem ser pareadas e operadas lado a lado, cada uma com seu QR,
status de conexão e histórico próprios. Uma única conta também pode manter **várias
chamadas 1:1 simultâneas** — uma por operador no navegador — roteadas de forma independente
por call ID.

> **Status:** estável e em produção. Chamadas de saída e de entrada chegam a `ACTIVE` com
> áudio bidirecional; chamadas recebidas abrem o widget e tocam dentro do Chatwoot. As
> sessões persistem em **PostgreSQL** (um banco por sessão, estilo WAHA).

---

## 🚀 O que o AstraCalls adiciona

Tudo abaixo foi construído **por cima** do WaCalls original, sem quebrar o núcleo VoIP:

### 🗄️ Persistência em PostgreSQL (1 banco por sessão)
Saímos do SQLite único para um modelo no estilo **WAHA**: um banco **principal**
(`wacalls_main`, com a tabela de config das sessões) + **um banco por sessão**
(`wacalls_<id>`, com o store do whatsmeow daquela conta — criado no `CREATE` e derrubado no
`DELETE`). Configurável por `WACALLS_PG_URL` e `WACALLS_PG_NAMESPACE`. Isola credenciais por
conta e escala muito melhor em cenário multi-tenant/SaaS.

### 💬 API de mensagens
Além de chamadas, a API agora **envia mensagens** via whatsmeow:
`POST /api/sessions/{sid}/messages/{text|image|audio|video|document}` (mídia por base64 ou
URL).

### 🔔 Webhooks por sessão
`GET/POST/DELETE /api/sessions/{sid}/webhook` — dispara eventos `message` e `receipt`
recebidos para a URL configurada, permitindo integrar com qualquer backend.

### 🤝 Integração nativa com Chatwoot
Módulo `chatwoot.go` (inspirado no app de Chatwoot do WAHA): contato e conversa
find/create por telefone, mensagens **WhatsApp → Chatwoot** (texto + mídia) e
**Chatwoot → WhatsApp** via webhook (`message_created/outgoing`). Tudo com **1 QR só** por
número — a mesma sessão serve chamadas, mensagens e Chatwoot.

### 📲 Widget de chamada dentro do Chatwoot
`widget.js` é injetado no Chatwoot via `<script src=".../widget.js" data-api-key="...">` e
adiciona um **botão de telefone na conversa**. Faz a chamada WebRTC direto do navegador do
agente, **abre e toca automaticamente quando chega uma ligação**, mostra "Chamando…" até o
outro lado atender, inicia o cronômetro só na conexão real e silencia o toque ao atender.

### 🔐 Autenticação por API key
Middleware `withAuth`: se `WACALLS_API_KEY` estiver setada, todas as rotas `/api/*` exigem
o header `X-API-Key` (ou `?apiKey=` no SSE). O cliente React ganhou tela de login (URL +
key). Essencial para expor o serviço fora de uma LAN confiável.

### 🌐 Mídia WebRTC pronta para nuvem (ICE-TCP / NAT 1:1)
Muitos provedores cloud (ex.: Hetzner) **bloqueiam UDP de entrada novo** na interface
pública, o que derruba o WebRTC padrão. O `bridge.go` agora configura
`SetNAT1To1IPs` + **ICE-TCP** na mesma porta, permitindo que o navegador conecte por TCP
quando o UDP não passa. Gated por `WACALLS_PUBLIC_IP` (aceita `auto` para auto-detecção) e
`WACALLS_UDP_PORT`.

### 🐳 Deploy em Docker Swarm + Traefik (HTTPS)
Stack pronta com Postgres dedicado, rede de host para a mídia enxergar a interface real,
proxy `socat` com labels do Traefik para publicar em HTTPS e imagem versionada no Docker
Hub. Detalhes em [Deploy](#-deploy-em-produção-docker-swarm--traefik).

### ⚡ UX e robustez
Envio do `offer`/`accept` assíncrono (a UI não trava mais até 15s no timeout do ack),
parsing correto do `<relay>` de entrada (áudio nas chamadas recebidas) e auto-detecção do
IP público.

---

## 🏗️ Arquitetura

```
┌──────────────────────────────────────────────────────────────────────────┐
│                BROWSER (cliente React)  +  Widget no Chatwoot               │
│   mic + alto-falante  ·  WebRTC (Opus 48 kHz)  ·  HTTP + SSE                │
└───────────────────────────────┬──────────────────────────────────────────┘
                                 │  POST /api/sessions/{sid}/calls/{id}/webrtc  (SDP)
                                 │  GET  /api/events                            (SSE)
                                 ▼
┌──────────────────────────── GO SERVER (cmd/server) ────────────────────────┐
│  SessionManager   registry de contas (client + CallManager + bridge)       │
│  Broker           hub SSE (sessões, auth, ciclo de vida das chamadas)       │
│  Bridge           ponte pion WebRTC (Opus do navegador ⇄ PCM 16 kHz)        │
│  Auth             middleware X-API-Key                                      │
│  Messaging        envio de texto/mídia via whatsmeow                        │
│  Webhook/Chatwoot integração externa (eventos + Chatwoot bidirecional)      │
│                                                                            │
│  internal/wa      adaptador VoipSocket sobre o whatsmeow                    │
│  internal/voip    call · signaling · media · transport · core · wanode     │
└───────────────┬──────────────────────────────────────┬────────────────────┘
                │ sinalização <call>                    │ mídia SRTP
                ▼                                        ▼
        ┌───────────────┐                    ┌──────────────────────┐
        │  WhatsApp WS  │                    │   relay do WhatsApp   │
        │  (whatsmeow)  │                    │  (SRTP sobre SCTP/DC) │
        └───────────────┘                    └──────────────────────┘
                                                         │
                                              ┌──────────────────────┐
                                              │  PostgreSQL 16        │
                                              │  wacalls_main + 1/db  │
                                              └──────────────────────┘
```

### Estrutura

| Caminho | Responsabilidade |
|---|---|
| `cmd/server` | Broker HTTP/SSE, session manager + store, ponte WebRTC, **auth, messaging, webhook, Chatwoot, db (Postgres)** |
| `internal/wa` | `VoipSocket` — envia/recebe stanzas `<call>` via whatsmeow |
| `internal/voip/core` | Tipos de domínio, constantes, interface `VoipSocket` |
| `internal/voip/wanode` | Helpers de node WhatsApp e JID |
| `internal/voip/media` | Codec MLow, RTP, SRTP, SSRC, resampling, derivação de chaves |
| `internal/voip/transport` | Relay SCTP, STUN, encoding de subscription |
| `internal/voip/signaling` | Build/parse de stanza `<call>`, cripto da call-key, parse do relay-ack |
| `internal/voip/call` | `CallManager` — orquestra uma chamada de ponta a ponta |
| `client/` | React 19 + Vite + Tailwind v4 + shadcn/ui (discador, cards de chamada, sessões, histórico, login) |
| `client/public/widget.js` | Widget de chamada embutível no Chatwoot |

---

## ⚙️ Início Rápido

```bash
git clone https://github.com/AstraOnlineWeb/AstraCalls.git
cd AstraCalls

go mod download                 # dependências Go
cd client && npm install && cd ..   # dependências do cliente React
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
> [opus_mlow](https://github.com/edgardmessias/opus_mlow), com a SONAME corrigida para
> `libopus_mlow.so`).

Abra `http://localhost:8080`, clique em **Nova sessão** e escaneie o QR (também impresso no
terminal) em **WhatsApp → Aparelhos conectados**.

### Cliente React em modo dev

```bash
cd client
npm run dev      # Vite na :5173, faz proxy de /api → http://localhost:8080
```

### Flags / variáveis de ambiente

| Flag / Env | Padrão | Significado |
|---|---|---|
| `-addr` | `:8080` | Endereço HTTP |
| `-static` | `client/dist` | Diretório do cliente estático (opcional) |
| `-debug` | `false` | Logs verbosos (inclui o log interno do whatsmeow) |
| `-max-calls-per-session` | `8` | Chamadas simultâneas por sessão (`0` = ilimitado) |
| `WACALLS_PG_URL` | — | URL do Postgres (usuário com `CREATEDB`) |
| `WACALLS_PG_NAMESPACE` | `wacalls` | Prefixo dos bancos por sessão |
| `WACALLS_API_KEY` | — | Se setada, exige `X-API-Key` em `/api/*` |
| `WACALLS_PUBLIC_IP` | — | IP público p/ NAT 1:1 / ICE-TCP (`auto` detecta) |
| `WACALLS_UDP_PORT` | — | Porta de mídia (UDP + ICE-TCP) |
| `WACALLS_MAX_CALLS` | `8` | Equivalente a `-max-calls-per-session` por env |
| `WACALLS_RECORDING_DIR` | `$TMPDIR/wacalls-recordings` | Diretório onde os MP3s de gravação ficam até serem baixados |
| `WACALLS_PUBLIC_BASE_URL` | — | Base pública do serviço p/ montar a URL de download da gravação (ex.: `https://voice.exemplo.com`) |
| `WACALLS_RECORDING_WEBHOOK_URL` | — | Endpoint que recebe o aviso "gravação pronta" (ex.: EnriqueceAI `/api/webhooks/wacalls`) |
| `WACALLS_WEBHOOK_SECRET` | — | Segredo compartilhado enviado no header `X-Webhook-Secret` do webhook de gravação |

> **Gravação de chamada (opcional).** Se `WACALLS_PUBLIC_BASE_URL`,
> `WACALLS_RECORDING_WEBHOOK_URL` e `WACALLS_WEBHOOK_SECRET` estiverem setadas, ao
> fim de cada chamada o áudio dos dois lados é mixado num MP3 mono 16 kHz,
> servido em `GET /recordings/{callId}.mp3` (rota pública — o `callId` é
> não-enumerável e atua como capability), e um webhook
> `POST {WACALLS_RECORDING_WEBHOOK_URL}` avisa o consumidor com
> `{ "service_call_id", "recording_url" }`. Sem essas envs, a gravação é
> desativada (degradação graciosa). Requer `ffmpeg` no PATH (já incluso na imagem
> Docker).

---

## 🐳 Deploy em produção (Docker Swarm + Traefik)

```bash
# imagem oficial publicada no Docker Hub:
#   astraonline/wacalls:develop
# para usar direto, basta referenciá-la na stack (PullImage).

# para buildar a sua própria a partir do código:
docker build -t astraonline/wacalls:develop .
docker push astraonline/wacalls:develop

# deploy da stack (Postgres + servidor em rede de host + proxy Traefik)
docker stack deploy -c astracalls-stack.yml astracalls
```

Notas de produção:
- O servidor roda em **rede de host** para a mídia WebRTC enxergar a interface real.
- Um serviço **socat** com labels do Traefik publica o HTTP em **HTTPS** (necessário porque
  `getUserMedia` só funciona em contexto seguro).
- O **PostgreSQL** dedicado escuta apenas em `127.0.0.1` (não exposto à internet).
- Defina `WACALLS_PUBLIC_IP=auto`, `WACALLS_UDP_PORT`, `WACALLS_PG_URL` e uma
  `WACALLS_API_KEY` forte nas variáveis da stack.

---

## 🔌 API

Todas as rotas são escopadas por sessão. Os eventos chegam por um único canal SSE,
marcados com o `sessionId` de origem. Se `WACALLS_API_KEY` estiver setada, envie
`X-API-Key` (ou `?apiKey=` no SSE).

### Sessões e chamadas

| Método | Rota | Função |
|---|---|---|
| `GET` | `/api/sessions` | Lista contas (id, nome, jid, status, pareado) |
| `POST` | `/api/sessions` | Cria uma conta e inicia o pareamento por QR |
| `DELETE` | `/api/sessions/{sid}` | Desloga e remove uma conta |
| `POST` | `/api/sessions/{sid}/logout` | Desconecta (mantém p/ re-parear) |
| `POST` | `/api/sessions/{sid}/pair` | Re-pareia (gera novo QR) |
| `POST` | `/api/sessions/{sid}/calls` | Inicia chamada de saída (`{ phone, duration_ms?, record? }`) |
| `POST` | `/api/sessions/{sid}/calls/{id}/webrtc` | Troca o SDP WebRTC do navegador |
| `POST` | `/api/sessions/{sid}/calls/{id}/accept` | Atende uma chamada recebida |
| `POST` | `/api/sessions/{sid}/calls/{id}/reject` | Rejeita uma chamada recebida |
| `DELETE` | `/api/sessions/{sid}/calls/{id}` | Encerra a chamada ativa |
| `GET` | `/api/sessions/{sid}/history` | Histórico recente (até 50 registros) |
| `GET` | `/api/events` | Server-sent events (sessões, auth, chamadas) |

### Mensagens, webhooks e Chatwoot *(novo no AstraCalls)*

| Método | Rota | Função |
|---|---|---|
| `POST` | `/api/sessions/{sid}/messages/text` | Envia texto |
| `POST` | `/api/sessions/{sid}/messages/{image\|audio\|video\|document}` | Envia mídia (base64 ou URL) |
| `GET/POST/DELETE` | `/api/sessions/{sid}/webhook` | Configura webhook de eventos da sessão |
| `GET/POST/DELETE` | `/api/sessions/{sid}/chatwoot` | Configura a integração Chatwoot |
| `POST` | `/api/sessions/{sid}/chatwoot/webhook` | Recebe eventos do Chatwoot (outgoing → WhatsApp) |
| `GET` | `/api/chatwoot/resolve` | Resolve sessão/contato para o widget (`?account_id=&conversation_id=`) |

---

## 🧪 Testes

```bash
go test ./...                 # pilha de mídia: SRTP, STUN, RTP, relay-ack, codec, estado
cd client && npm run build    # type-check + build de produção do cliente
```

---

## 🔒 Segurança

- Em produção, **sempre** defina `WACALLS_API_KEY` — sem ela qualquer um com acesso HTTP
  pode criar contas, fazer chamadas e ler histórico.
- O banco de cada sessão guarda **credenciais do WhatsApp**. Mantenha o Postgres protegido
  e fora da internet.
- Exponha sempre por **HTTPS** (o `getUserMedia` exige contexto seguro).

---

## 👥 Colaboradores

O AstraCalls é construído sobre o excelente trabalho da equipe do **WaCalls**. Todos os
créditos do projeto original:

<div align="center">

<a href="https://github.com/jotadev66"><img src="https://github.com/jotadev66.png" width="72" height="72" style="border-radius:50%" alt="jotadev66"/></a>
<a href="https://github.com/edgardmessias"><img src="https://github.com/edgardmessias.png" width="72" height="72" style="border-radius:50%" alt="edgardmessias"/></a>
<a href="https://github.com/w3nder"><img src="https://github.com/w3nder.png" width="72" height="72" style="border-radius:50%" alt="w3nder"/></a>

[**@jotadev66**](https://github.com/jotadev66) · [**@edgardmessias**](https://github.com/edgardmessias) · [**@w3nder**](https://github.com/w3nder)

**Projeto original:** [WaCalls](https://github.com/JotaDev66/WaCalls)

</div>

---

## 🙏 Agradecimentos

- [**whatsmeow**](https://github.com/tulir/whatsmeow) — biblioteca Go do protocolo WhatsApp Web
- [**pion/webrtc**](https://github.com/pion/webrtc) — pilha WebRTC em Go puro (ICE + DTLS + SCTP)
- [**opus_mlow**](https://github.com/edgardmessias/opus_mlow) — codec MLow nativo
- [**zapo**](https://github.com/w3nder/zapo) — referência da pilha de mídia VoIP
- [**WAHA**](https://github.com/devlikeapro/waha) — inspiração para o storage por sessão e a integração Chatwoot

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
