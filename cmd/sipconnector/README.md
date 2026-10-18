# Conector SIP (PABX IP → AstraChat)

Serviço **separado** do `wacalls` que liga um PABX IP qualquer (Asterisk, FreePBX,
Issabel, 3CX, tronco hospedado…) ao AstraChat, **sem WhatsApp**. Ele fala SIP/RTP com o
PABX e entrega ao AstraChat a **mesma API de chamadas do wacalls**. Por isso o CallDock, o
listener, o histórico e o Chatbot por voz do AstraChat funcionam com a linha SIP sem
precisar saber que do outro lado é um PABX.

```
PABX IP ◄─ SIP/RTP (G.711) ─► sipconnector ◄─ API do wacalls ─► AstraChat
                               • linha da caixa (tronco ou ramal)
                               • 1 ramal por atendente (e-mail → ramal)
                               • navegador: WebRTC PCMU (sem transcodificar)
                               • IA: WS pcm16 16 kHz
```

## Linhas

Uma **linha** corresponde a uma caixa do AstraChat. É o AstraChat que a cria e atualiza, ao salvar
a aba "Ligações SIP (PABX)", com `PUT /api/lines/{id}`.

| modo | quem registra | uso |
|---|---|---|
| `register_out` | o conector registra como ramal no PABX (`host`, `user`, `password`) | o PABX encaminha a URA/fila para esse ramal |
| `register_in` | o PABX registra no conector (`user`/`password` são do tronco) | PABX com tronco SIP apontando para o conector |

**`agents`** (`[{email, extension, authUser, password}]`): para cada atendente, o conector
registra **um ramal no PABX da linha**.

**`authUser`** (na linha e em cada ramal) é o login do digest quando o PABX separa o ramal do
usuário de autenticação. No **3CX** é o "ID de autenticação" do ramal (um código aleatório).
O ramal continua sendo a identidade SIP (From/To/Contact). Vazio = autentica com o próprio ramal.
- Uma ligação para esse ramal toca **só** para aquele atendente: o evento `incoming` traz
  `agentEmail`.
- Quando o atendente disca pelo ticket (`agentEmail` no `POST .../calls`), a ligação sai
  **pelo ramal dele**.

**Roteamento de uma ligação recebida**, nesta ordem:
1. pelo usuário do `Contact` que registramos (cada linha/ramal tem o seu);
2. pelo número discado (ramal do agente, usuário da linha ou `did`);
3. pelo IP de origem, quando só uma linha confia nele.

**Origem confiável**: só aceitamos INVITE vindo de um destes endereços:
- o `host` ou o `proxy` da linha;
- o IP de onde o PABX registrou (modo `register_in`).

## API

Use `X-API-Key: <SIPC_API_KEY>` (chave-mestra) ou, no navegador, o token de widget (`apiKey=`).

| Método | Rota | Quem | O que faz |
|---|---|---|---|
| GET | `/api/me` | mestra | valida a chave (o AstraChat chama ao salvar) |
| POST | `/api/widget-tokens` | mestra | `{accountId, ttlSeconds?}` → token efêmero **preso à conta** |
| GET/PUT/DELETE | `/api/lines/{id}` | mestra | config da linha (senhas nunca voltam; vazio no PUT mantém) |
| GET | `/api/lines/{id}/status` | mestra | estado do registro da linha e de cada ramal |
| GET | `/api/sessions` | mestra | `[{id, name, state}]` no formato do wacalls |
| GET | `/api/events` | mestra/token | SSE: `incoming`, `call-status`, `call-list`, `incoming-claimed`, `call-ended`, `call-transfer-offer` — **sempre escopado pela conta** |
| POST | `/api/sessions/{sid}/calls` | mestra/token | `{phone, agentEmail?}` → `{call:{callId}}` |
| POST | `/api/sessions/{sid}/calls/{id}/accept` \| `reject` \| `pickup` | mestra/token | atender / recusar / assumir |
| DELETE | `/api/sessions/{sid}/calls/{id}` | mestra/token | desligar (CANCEL, recusa ou BYE, conforme o estado) |
| POST | `/api/sessions/{sid}/calls/{id}/webrtc` | mestra/token | `{sdp_offer}` → `{sdp_answer}` (navegador, só PCMU) |
| GET | `/api/sessions/{sid}/calls/{id}/ws` | mestra/token | WS `pcm16` (PCM16 LE 16 kHz; `events=1` manda eventos em texto) |
| POST | `/api/sessions/{sid}/calls/{id}/transfer` | mestra/token | `{"target":"sip","extension":"201"}` (REFER) ou `{"target":"agent"}` (`{}` também = agent, formato do wacalls) |

**Um consumidor de áudio por vez.** Quando a IA passa a ligação ao atendente, o navegador
abre o `/webrtc` e substitui o WS sem derrubar a chamada. Se o consumidor sai **sem ser
substituído**, a chamada cai, igual ao wacalls. A exceção é durante um REFER: quem decide
o fim é o PABX, pelo NOTIFY.

## Variáveis de ambiente

| Variável | Padrão | |
|---|---|---|
| `SIPC_API_KEY` | — | **obrigatória**; chave-mestra (também assina os tokens de widget) |
| `SIPC_PUBLIC_IP` | 1ª interface | IP anunciado no SDP/Contact/ICE. **Defina em VPS com NAT** |
| `SIPC_HTTP_ADDR` | `:8090` | API |
| `SIPC_SIP_ADDR` | `0.0.0.0:5070` | SIP UDP+TCP (não colide com o 5060 do wacalls) |
| `SIPC_SIP_ADVERTISE_PORT` | porta do `SIP_ADDR` | porta no Contact |
| `SIPC_RTP_PORT_MIN` / `MAX` | `40100` / `40299` | faixa RTP com o PABX (uma porta par por chamada) |
| `SIPC_WEBRTC_PORT` | `0` (efêmera) | porta fixa UDP+TCP do WebRTC com o navegador |
| `SIPC_DATA_DIR` | `./data` | `lines.json` (persistência das linhas) |
| `SIPC_RING_TIMEOUT_SECONDS` | `60` | tempo tocando sem ninguém atender |
| `SIPC_TRUST_ANY_SOURCE` | `0` | aceita INVITE de qualquer IP (**só laboratório**) |

## Deploy (Swarm)

A mesma imagem `astraonline/astracalls` traz o binário `sipconnector`. Ele sobe como **outro
serviço**: troque o entrypoint e publique as portas em `mode=host`.

```yaml
  sipconnector:
    image: astraonline/astracalls:develop
    entrypoint: ["sipconnector"]
    environment:
      SIPC_API_KEY: "<chave forte>"
      SIPC_PUBLIC_IP: "<ip público>"
      SIPC_WEBRTC_PORT: "50100"
      SIPC_DATA_DIR: /data
    volumes:
      - sipconnector_data:/data
    ports:
      - { target: 5070, published: 5070, protocol: udp, mode: host }
      - { target: 5070, published: 5070, protocol: tcp, mode: host }
      - { target: 50100, published: 50100, protocol: udp, mode: host }
      - { target: 50100, published: 50100, protocol: tcp, mode: host }
      # faixa RTP 40100-40299/udp, também em mode=host
    # a API (8090) vai atrás do Traefik com HTTPS: o navegador do atendente fala com ela
```

## Testes

```bash
go test ./cmd/sipconnector/
```

Rodam contra um **PABX falso** (sipgo) em loopback e cobrem:
- registro da linha e do ramal;
- ligação recebida para o ramal do agente, com escopo por conta;
- a IA falando pelo WS;
- a saída em PCMA;
- REFER com NOTIFY;
- tronco `register_in` com digest e nonce;
- WebRTC PCMU nos dois sentidos com um peer que simula o navegador.
