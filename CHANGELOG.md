# Changelog

Todas as mudanças relevantes do AstraCalls.

## v1.0.2 — 2026-09-25

Rodada de API de mensagens: **botões/listas interativas** que entregam de verdade,
**agendamento**, **encaminhar**, **busca**, **webhook assinado**, envio idempotente
e mais robustez no envio.

### 🔘 Mensagens interativas (novo)

- **Botões que renderizam no WhatsApp** (número não-oficial): `POST /messages/interactive`
  (CTA — abrir URL, copiar código, ligar, resposta rápida), `POST /messages/buttons`
  (botões de resposta, até 3) e `POST /messages/list` (menu de lista). Usa o formato
  native flow com o stanza correto que faz o WhatsApp entregar/exibir. *A lista só
  renderiza no aplicativo do celular — no WhatsApp Web ela pede "use o celular"
  (limitação do Web).* Para botão com garantia total, a API oficial (coexistência)
  segue sendo o caminho.

### 💬 Envio & mensagens (novo)

- **Envio idempotente:** `GET /messages/new-message-id` gera um id, e o envio aceita
  um `id` do cliente (header `X-Message-ID` ou `?id=`) em **todos** os endpoints —
  reenviar com o mesmo id **não duplica** a mensagem (ótimo para retry).
- **Agendamento (`send_at`):** `POST /schedule` agenda texto/imagem para o futuro;
  `GET /schedule` lista e `DELETE /schedule/{id}` cancela. Persiste (sobrevive a
  restart) e um worker envia na hora marcada.
- **Encaminhar:** `POST /messages/forward {to, messageId}` encaminha uma mensagem
  do histórico (inclusive mídia), marcada como encaminhada.
- **Busca:** `GET /messages/search?q=&chatId=` procura no histórico de mensagens.
- **Nota de vídeo (PTV):** `POST /messages/ptv` envia o "balãozinho" de vídeo redondo.

### 🔒 Webhook

- **Assinatura HMAC-SHA256:** configure um `secret` no `POST /webhook` e cada
  entrega leva o header `X-Webhook-Signature: sha256=…` para o consumidor verificar
  a autenticidade. O `GET` não devolve o segredo em claro.
- **Filtro de eventos:** `POST /webhook {events:[...]}` entrega só os tipos de
  evento assinados (vazio = todos), reduzindo ruído.

### 🛠️ Robustez de envio

- **Correção do 9º dígito (LID) em TODOS os envios diretos.** Antes só o caminho do
  Chatwoot resolvia o LID; agora os endpoints de envio da API também reenviam
  corretamente quando o servidor retorna "no LID found" (comum no 9º dígito BR).

## v1.0.1 — 2026-09-25

Correção pontual no vídeo recebido.

### 📹 Vídeo

- **Câmera do cliente não fica mais preta no painel.** O vídeo do peer só era
  exibido quando o WhatsApp mandava a sinalização `<video state=1>` — o que nem
  sempre acontece (vídeo desde o início da chamada, ou sinal fora do caminho
  tratado). Nesses casos o vídeo do cliente chegava e era decodificado, mas o
  painel não o exibia (ficava preto), embora o operador aparecesse normalmente
  para o cliente. Agora, assim que chegam **frames de vídeo reais** do peer, o
  painel passa a exibir a câmera dele — sem depender apenas da sinalização.

## v1.0.0 — 2026-09-25

Primeira versão estável. 🎉 Rodada grande de confiabilidade de chamada (não cair
aos ~20s, recusa que para o telefone, religar vídeo), **disparo em massa**,
**entrega confiável de webhook**, **observabilidade**, **anti-SSRF** e transporte
de chamada **auto/WebSocket** que conecta mesmo atrás de firewall.

### ☎️ Chamadas — confiabilidade

- **Consent freshness (RFC 7675).** A chamada WebRTC **não cai mais sozinha por
  volta dos ~20s**: o keepalive do relay renova o consent com STUN Binding
  periódico (período base 5s, randomizado 4–6s conforme a RFC). Antes o WhatsApp
  derrubava a mídia mesmo com áudio fluindo.
- **Recusa (`/reject`) para o telefone de quem ligou.** O `<reject>` passou a
  levar `count="0"` **e** `from=ownID` (alinhado ao `RejectCall` do whatsmeow) —
  antes o aparelho do chamador continuava tocando até o timeout.
- **Chamada recebida não cai sozinha antes de atender.** Um dispositivo secundário
  `@hosted.lid` da própria conta que responde `uncallable` não derruba mais a
  chamada — os controles Atender/Recusar continuam disponíveis. (#27 / #28)
- **Timeout de toque.** Chamada de entrada que nunca recebe o encerramento expira
  sozinha, sem prender a vaga da sessão.

### 📹 Vídeo

- **Religar a câmera no meio da chamada funciona.** Desligar e ligar o vídeo de
  novo agora envia `<video state=1>` (em vez de um novo pedido de upgrade), então
  a câmera reativa no aparelho do cliente.

### 📞 Transporte de chamada (áudio no navegador)

- **Fallback automático WebRTC → WebSocket** (modo `auto`, padrão): se o WebRTC não
  fecha (ex.: UDP bloqueado por firewall), cai para WebSocket automaticamente — a
  chamada conecta **sem precisar abrir portas**.
- **`WACALLS_DEFAULT_TRANSPORT`**: fixa o transporte por instância
  (`websocket` | `webrtc` | `auto`).
- **WebSocket no widget de chamada do Chatwoot** (áudio full-duplex, sem depender
  de UDP).
- **`no-cache` no `index.html`** do painel: o navegador sempre carrega a última
  versão publicada.

### 💬 Envio & mensagens

- **Correção do 9º dígito BR / erro `no LID found`.** Resolve o JID canônico
  (via `IsOnWhatsApp`), grava o mapeamento **PN↔LID** e reenvia pelo PN —
  mensagens que falhavam/duplicavam passam a entregar corretamente.
- **O webhook do Chatwoot não engole mais falha de envio:** responde **502**
  quando o WhatsApp recusa (antes retornava 200 em qualquer caso).
- **`POST /messages/event`** (envia mensagem de evento) e a flag
  `astracall_rich_sent` para o Chatwoot **não reenviar** mensagem rica já enviada
  pela API.
- **`quotedMessageId` / `quotedParticipant`** expostos no evento de mensagem do
  webhook (inclusive em grupos).

### 📢 Disparo em massa (novo)

- **`POST /blast`**: campanha de texto ou imagem com **pacing anti-ban** (worker
  sequencial, delay + jitter aleatório entre envios, retry por destinatário e
  cancelamento). Endpoints de status, listagem e cancelamento.

### 🔁 Entrega confiável de webhook (novo)

- **Retry com backoff**, **circuit breaker** por sessão, **DLQ** (dead-letter
  queue) consultável e **replay** por endpoint. Payload e headers idênticos — nada
  muda para quem consome (ex.: AstraChat).

### 📊 Observabilidade (novo)

- **`GET /livez`, `/readyz`, `/metrics`** (formato Prometheus, sem dependência
  nova): número de sessões carregadas, itens em DLQ e uptime.

### 🔒 Segurança

- **Guarda anti-SSRF** no download de mídia por URL do usuário (bloqueia IPs
  internos/privados; isenta o `data_url` do próprio Chatwoot).

### 🐳 Infra / CI

- Tag da imagem com prefixo **`v`** (`type=ref,event=tag`) e publicação de
  **`:latest`** nas tags `v*`; build multi-arch (amd64 + arm64) nativo por
  arquitetura.

## v0.0.9 — 2026-09-09

Correções de confiabilidade nas chamadas (recusa e encerramento) e imagem
Docker **multi-arquitetura** (amd64 + arm64).

### ☎️ Chamadas

- **Recusa (`/reject`) confiável.** O endpoint agora responde **404** quando o
  `callId` não existe / a chamada já encerrou, e **409** quando o WhatsApp recusa
  a operação — em vez de responder **200 em qualquer caso**. O cliente passa a
  distinguir "recusei de verdade" de "id errado". O `callId` é o mesmo dos eventos
  `incoming`/`call-list`.
- **Encerramento confiável quando a chamada é atendida ou encerrada em outro
  aparelho** (ex.: o próprio celular). O WhatsApp às vezes envia o
  `<terminate>`/`<reject>` junto com outros nós, o que caía como evento
  "desconhecido" e era **ignorado** — a chamada nunca encerrava. Isso deixava o
  **toque preso** nos atendentes e a **vaga da chamada presa**, esgotando o limite
  e fazendo novas chamadas serem **recusadas automaticamente**. Agora é tratado
  como encerramento: avisa **todos** os atendentes (`call-ended`) e **libera a
  vaga**.

### 🐳 Imagem & infraestrutura

- **Imagem Docker multi-arquitetura: `linux/amd64` + `linux/arm64`.** Roda também
  em servidores ARM (AWS Graviton, Ampere/Oracle Cloud, etc.), não só x86.
- Build multi-arch automatizado por CI, com build **nativo por arquitetura** (sem
  emulação).

## v0.0.8 — 2026-09-02

Chamadas por **SIP/PBX** (tronco e ramal, áudio bidirecional), correção de
vazamento de chamadas entre contas no multi-conta, token de widget por conta
para não expor a chave-mestra, notas de voz com waveform, download de mídia
recebida e citações/menções no envio.

### ☎️ Chamadas por SIP / PBX (novo)

- **Gateway SIP** (servidor Go na porta 5060 UDP/TCP) integrando tronco/PBX ↔
  WhatsApp por sessão, com **áudio bidirecional** (ponte RTP G.711 u-law 8kHz ↔
  PCM 16kHz do WhatsApp) e autenticação **Digest MD5** no REGISTER.
  - **Modelo 1 — o PBX registra no AstraCalls:** o painel mostra usuário/senha/
    servidor por sessão; o cliente cria um tronco no Asterisk/FreePBX. SIP→WhatsApp
    disca o número pelo WhatsApp (200 OK só quando a chamada conecta); WhatsApp→SIP
    toca no PBX/softphone registrado.
  - **Modelo 2 — o AstraCalls registra num PBX externo (UAC):** campos de host/
    porta/usuário/senha e ramal de destino no painel, REGISTER + re-REGISTER
    automático, desafio Digest e **status do registro** (Registrando/Registrado/
    Falhou) na tela.
- **Robustez:** RTP em faixa de portas fixa (funciona atrás do Docker Swarm/NAT);
  tratamento de `Expires: 0` (desregistro) e **expiração de registro** — um PBX
  que cai não deixa mais registro fantasma que derrubaria chamadas reais de entrada.

### 💬 Chatwoot & multi-conta

- **Correção de vazamento entre contas nos eventos.** Conexões de eventos (SSE)
  abertas sem `accountId` não são mais tratadas como "admin" (que recebia chamadas
  de TODAS as contas): a conta é derivada do `clientId` no padrão `astrachat_accN`
  quando o parâmetro não vem. Fim de chamada tocando/contato criado na conta errada.
- **Fim de contato com LID como número.** Contatos 1:1 vindos de `@lid` não são
  mais criados com o LID cru no `phone_number`; quando o número real (PN) resolve
  depois, o telefone é preenchido (backfill) no contato existente — encontrado
  também pelo identifier `@lid` — em vez de duplicar.

### 🔐 Integração & segurança

- **Token de widget por conta.** `POST /api/widget-tokens` (com a chave-mestra)
  devolve um token efêmero, escopado a uma conta (e opcionalmente inbox), que abre
  só a superfície de widget (eventos/resolve/chamadas) e, no SSE, **só recebe
  eventos da própria conta** (senão `403 account_scope_mismatch`). Evita pôr a
  chave-mestra no navegador. `GET /api/widget-key` devolve a chave estática por
  instância, ou **409 explícito** quando não configurada.

### 🎙️ Mensagens & mídia

- **Nota de voz (PTT) com duração e waveform.** O envio de áudio aceita `seconds`
  e `waveform`; quando não vêm, o servidor estima a duração pela granule do
  OGG/Opus e gera um waveform aproximado. Antes a nota chegava sem tempo e com
  barra reta.
- **Download de mídia recebida** (`GET /api/sessions/{sid}/messages/{id}/media`,
  decifra imagem/áudio/vídeo/documento/sticker), **citação (`quotedMessageId`) e
  menções** no envio (texto/imagem/vídeo/áudio/documento), novo evento de webhook
  **`deleted`** quando o contato apaga uma mensagem para todos, `senderPhone`/
  `chatPhone` (telefone real do `@lid`) no webhook, e marcação de tipo de conversa
  (individual/grupo/canal/broadcast) em `/chats`.

### 🖥️ Painel

- Mostra o **número conectado** e o **último número** após desconectar (formato BR),
  e remove o sufixo de device (`:N`) do número na lista de conexões.

### 🛠️ Infra & dependências

- whatsmeow atualizado (28-08-2026); build do codec MLow via **submódulo**
  (não depende mais de clonar o GitHub em tempo de build); **logs de diagnóstico
  de SIP/mídia opt-in** via `WACALLS_SIP_DEBUG` (desligados por padrão).

## v0.0.7 — 2026-08-26

Reflexo de mensagens editadas no Chatwoot, captura da origem de anúncios (Click
to WhatsApp), transporte de áudio da chamada por WebSocket para integrações de
voz/IA, e melhorias de estabilidade de conexão.

### 💬 Chatwoot & Mensagens

- **Mensagem editada pelo contato agora aparece.** Quando o cliente edita uma
  mensagem no WhatsApp, o Chatwoot passa a mostrar um balão **"✏️ Editada:"** com
  o texto novo, ligado à mensagem original. Cobre inclusive a edição
  **criptografada** (`secretEncryptedMessage`), que antes chegava vazia. O webhook
  ganhou os campos `edited` e `editedId`.
- **Origem de anúncio (Click to WhatsApp).** Quando o contato chega por um anúncio
  do Facebook/Instagram, a primeira mensagem traz um objeto `referral` no webhook
  (click id `ctwaClid`, link, título, `ref`, source/medium) — o "UTM" do WhatsApp
  — e a origem é registrada como nota na conversa do Chatwoot.
- **Correção: documento chegando como `.bin` no Android.** PDFs e arquivos
  enviados pelo Chatwoot chegavam sem extensão/mimetype correto e o celular do
  cliente abria como `.bin`. Agora o tipo é resolvido pelo cabeçalho HTTP e pela
  assinatura do arquivo.

### 🔌 Estabilidade & Conexão

- **Fim do "Será desconectado hoje".** A conta era marcada como inativa e agendada
  para remoção mesmo com o bridge conectado 24/7. Agora enviamos presença
  `available` (com espera pelo pushname real e reforço periódico), mantendo o
  dispositivo ativo.
- **Atualização do whatsmeow.** Endereçamento LID em conversas diretas, correção
  de download de mídia e isolamento da fila de handlers por conexão (mais
  estabilidade geral da API) + dependências atualizadas.

### 🎙️ Áudio da chamada (integrações de voz/IA)

- **Transporte de áudio da chamada por WebSocket** — PCM16 full-duplex direto (sem
  Opus) com eventos opt-in (`?events=1`). Contrato estável para um agente de
  voz/IA consumir o áudio da ligação em tempo real.

### 🛠️ Infra & Diagnóstico

- **Revisão de build embutida na imagem** (`org.opencontainers.image.revision`) —
  identifica exatamente qual commit está rodando.
- Logs de diagnóstico do SSE e do broadcast de chamada recebida; revalidação de
  cache do `widget.js`.

## v0.0.6 — 2026-08-14

Correção de sincronização de chamada entre dispositivos, conexão de sessão por
código, transporte de áudio alternativo para redes restritivas e documentação da
API completa.

### 📞 Chamadas

- **Correção: outros dispositivos paravam de tocar.** Numa ligação para um número
  com vários aparelhos vinculados (celular + WhatsApp Web etc.), ao atender em um
  device os demais continuavam tocando — e recusar em um podia derrubar a chamada
  já ativa. Agora, no primeiro atendimento é enviado
  `terminate reason="accepted_elsewhere"` para os aparelhos que não atenderam,
  vale o **primeiro accept** e só o aparelho que atendeu pode encerrar.
- **Transporte de áudio por WebSocket** (opt-in) — alternativa ao WebRTC para
  redes/proxies que bloqueiam UDP (Cloudflare, firewall corporativo). O áudio
  trafega como PCM sobre WSS/443. Ativação manual (`?transport=ws`); o padrão
  segue sendo WebRTC. Modo WebSocket é áudio-only.

### 🔗 Conexão de conta

- **Conectar por código, além do QR.** Na tela de pareamento há um seletor
  **QR / Código**: informe o número e receba um código de 8 dígitos para digitar
  no WhatsApp (*Aparelhos conectados → Conectar com número de telefone*).

### 📖 API & Documentação

- **OpenAPI 100% sincronizado.** Documentadas as 26 rotas que faltavam — incluindo
  Pix, produto, sticker, pareamento por código, controle de chamada
  (espera/transferência/pickup), privacidade e mensagens temporárias.
  (`/api-docs.html`)

## v0.0.5 — 2026-08-10

Atualização grande: chamadas de vídeo, controles de chamada (espera e
transferência), entrega resiliente ao Chatwoot e vários novos envios.

### 📞 Chamadas de voz e vídeo

- **Chamada de vídeo (H264)** — envio e recebimento, no formato de extensão RTP
  atual do WhatsApp.
- **Sinalização de chamada corrigida** para o WhatsApp atual — antes a chamada
  não tocava no destino.
- **Upgrade/downgrade de vídeo no meio da chamada** (painel +
  `POST /api/sessions/{sid}/calls/{id}/video/{action}`).
- **Espera (hold/resume)** com música de espera e **atender em espera**.
- **Transferência cega** entre atendentes (troca de dono + ponte, sem tocar a
  perna do WhatsApp).
- **Toque fantasma** (fake call) — `POST /api/sessions/{sid}/calls/fake`.
- **Gravação:** flag `record` por chamada no start (além do opt-in da sessão) e
  correção do **áudio picotado** (mixagem por cursor de amostras, resync só em
  drift > 300 ms).
- Chamada recebida mostra **nome/telefone reais** (resolve LID → PN) em vez de
  `@lid`.
- Transporte: desliga mDNS e limita relays discados/abertos por chamada.

### 💬 Integração com o Chatwoot

- **Fila de reentrega durável:** mensagens recebidas não se perdem se o Chatwoot
  ficar indisponível — retry com backoff exponencial, persistente (sobrevive a
  restart do serviço) e idempotente por `source_id` (não duplica).
- **Legenda de documento na entrada:** PDF/arquivo com legenda chegava com o
  texto vazio → corrigido.
- **Documento como ".bin" no Android:** passa a usar mimetype e nome reais do
  anexo, com fallback por extensão.
- **Importa o histórico** de conversas para o Chatwoot ao conectar a conta.
- **Espelha mensagens enviadas pela API** (toggle `mirror_api`).
- Mensagens enviadas **pelo aparelho** entram como nota privada ("Enviado pelo
  aparelho").
- **Aviso de sessão desconectada** no Chatwoot (LoggedOut / StreamReplaced /
  TemporaryBan / ClientOutdated) via contato de sistema + webhook.
- **Chamada recebida escopada por conta** (multi-tenant) — não vaza para o widget
  de outra empresa.
- **Vídeo no widget do Chatwoot** (WebCodecs H264 sobre datachannel).

### 📤 Envios (API)

- **Documento com legenda:** o texto agora vai junto com o arquivo — no envio pelo
  Chatwoot e na API (novo campo `caption`), embrulhado em
  `documentWithCaptionMessage` (formato oficial do WhatsApp).
- **Figurinha** (sticker WebP).
- **Pix** (BR Code).
- **Produto** (imagem + legenda) e **produto nativo** (catálogo).
- Helper `flexFloat`: aceita numérico como string (compatibilidade com n8n).

### 🔒 Segurança

- **Chave de widget escopada** (`WACALLS_WIDGET_KEY`): o widget só acessa o
  necessário (`/api/events`, resolução de contato e endpoints de chamada); o
  painel segue com a chave mestre.

### 🖥️ Painel & Build

- Exibe o **ID da sessão** no cabeçalho, com botão de copiar.
- **Dockerfile arch-aware:** habilita o build **ARM64** do codec MLow.

## v0.0.4 — 2026-07-10

### 🐛 Correções

- **Codec de áudio portável (corrige crash em CPUs sem AVX):** o codec MLow
  (`libopus_mlow.so`) era compilado com `-mavx` fixo, sem detecção de CPU em
  tempo de execução. Em servidores cujo processador não tem **AVX** (VPS com CPU
  restrita, processadores mais antigos), o AstraCalls **quebrava (SIGILL/SIGSEGV)**
  ao iniciar uma chamada — a ligação não completava. Agora o codec é compilado em
  baseline (SSE2), rodando em qualquer x86-64. Os fontes do MLow são C puro, sem
  perda funcional.

### ✨ Novidades

- **`groups_skip_incoming`** na config do Chatwoot: com `groups: true`, não reflete
  as mensagens dos outros membros do grupo (quando outra fonte já as traz pro mesmo
  inbox), postando só as mensagens do próprio aparelho e os avisos de entrada/saída —
  evita duplicação.

## v0.0.3 — 2026-07-10

Melhorias na integração com o Chatwoot: paridade com a Evolution e cobertura de
grupos. Todas as flags novas ficam na mesma config `POST /api/sessions/{sid}/chatwoot`.

### ✨ Novidades

**Assinatura do atendente e paridade com a Evolution**
- `sign_msg` — prefixa `*Nome do atendente*` no texto e na legenda de mídia das
  mensagens de saída (o nome vem do sender do webhook; não fica salvo na conversa)
- `always_online` — mantém a presença da conta sempre como online
- `read_messages` — confirma leitura automática das mensagens recebidas

**Grupos no Chatwoot**
- Mensagens que a conta envia **pelo aparelho** dentro de um grupo agora refletem
  no Chatwoot como nota privada (antes só conversas 1:1 espelhavam)
- Eventos de participantes de grupo (entrar, sair, virar/deixar de ser admin) —
  os mesmos avisos que o WhatsApp mostra na janela do grupo:
  - Novo evento de webhook `group_participants` (`group`, `actor`, `joined`, `left`, `promoted`, `demoted`)
  - Nota informativa na conversa do grupo (➕ entrou / ➖ saiu / ⭐ admin)

## v0.0.2 — 2026-07-08

Primeira versão estável desde a v0.0.1. Destaques: API completa estilo WAHA,
recepção de novos tipos de mensagem no Chatwoot, recursos avançados do whatsmeow
e suporte a pareamento por passkey.

### ✨ Novidades

**API completa (compatível com clientes estilo WAHA)**
- Mensagens e contatos
- Grupos (criar, participantes, admin)
- Canais, status, presença e perfil
- Histórico de conversas e mensagens
- Aliases de compatibilidade nos payloads (`id`/`chatId`/`subject`/`role`/`from`…)
- Documentação OpenAPI/Swagger de todas as rotas

**Novos tipos de mensagem recebida (renderizados no Chatwoot e no webhook)**
- Enquete/poll — criação, encaminhamento e recebimento de votos decodificados
- Figurinha (sticker/WebP) vira anexo
- Catálogo/produto e pedido do WhatsApp Business
- Cobrança Pix
- Evento do WhatsApp (nome/descrição/data BRT/local/link) + RSVP (Vou/Talvez/Não vou)
- Contato/vCard (nome + telefones)
- Reação (com o ID da mensagem reagida)
- Visualização única (desembrulha ViewOnce V2/V2Extension/legado; avisa quando indisponível)

**Recursos avançados do whatsmeow**
- Pareamento por código (sem QR)
- Privacidade da conta (leitura e alteração; privacidade de status)
- Mensagens temporárias (padrão e por conversa)
- Admin de grupo — solicitações de entrada, modo de aprovação e quem pode adicionar membros
- Perfil Business de contato e link/QR "me adicione"

**Passkey (WebAuthn)**
- Pareamento de contas que o WhatsApp passou a exigir passkey
- Extensão AstraCalls Passkey (Chrome) + integração no painel e download pelo próprio painel

**Integração Chatwoot**
- Abre conversas de grupos e canais (com toggles)
- Espelha como nota privada as mensagens 1:1 enviadas pelo aparelho (anti-loop por id)
- Resposta com citação bidirecional (Chatwoot ↔ WhatsApp)
- Grava `source_id` na mensagem de saída do agente
- Não reutiliza contato de grupo legado em conversa 1:1

**Chamadas**
- Gravação de chamada opt-in por sessão → nota privada no Chatwoot + webhook
- Disparo em massa de ligações com áudio pré-gravado

**Rede**
- Proxy de saída por sessão (http/https/socks5) via painel e API

**Interface**
- Redesign com identidade AstraCalls (logo, favicon, paleta navy/azul, layout de cards flutuantes, pt-BR)

### 🐛 Ajustes
- Selects de áudio responsivos (empilham no mobile, não estouram o box)
- Cabeçalho da conta responsivo (ações quebram linha; rótulos viram ícone no mobile)
- Bolinha do Switch encosta corretamente no fim
- Sidebar e conteúdo unificados num único container

## v0.0.1 — 2026-06-26

Versão inicial marcada do AstraCalls (chamadas WhatsApp no navegador + integração Chatwoot).
