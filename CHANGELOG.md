# Changelog

Todas as mudanças relevantes do AstraCalls.

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
