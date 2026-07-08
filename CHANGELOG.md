# Changelog

Todas as mudanças relevantes do AstraCalls.

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
