# Tutorial: AstraCalls no n8n

Neste tutorial você conecta o AstraCalls ao **n8n** e monta um robô que responde mensagens de WhatsApp. Leva uns 10 minutos e não precisa programar.

📦 Node: [`n8n-nodes-astracalls`](https://www.npmjs.com/package/n8n-nodes-astracalls). A documentação completa, com as 55 operações e os 15 eventos, está na página do npm.

---

## O que você precisa

- Um AstraCalls rodando, com **pelo menos uma sessão conectada** (número pareado pelo QR Code no painel).
- A **API Key** do servidor, que é o valor de `WACALLS_API_KEY`.
- Um n8n onde você seja o dono (owner) da instância, porque só o owner instala community nodes.

---

## Passo 1: instalar o node

1. No n8n, abra **Configurações** (⚙️) → **Community Nodes** → **Install a community node**.
2. Digite `n8n-nodes-astracalls`, marque a ciência dos riscos e clique em **Install**.
3. Ao adicionar um node, procure **AstraCalls**. Aparecem dois:
   - **AstraCalls**: faz ações, como enviar mensagem, ligar ou criar grupo.
   - **AstraCalls Trigger**: inicia o fluxo quando algo acontece, como uma mensagem recebida.

> **n8n em Docker:** se preferir, adicione `N8N_COMMUNITY_PACKAGES=n8n-nodes-astracalls` nas variáveis do serviço e reinicie.

## Passo 2: criar a credencial

1. **Credentials** → **Add credential** → **AstraCalls API**.
2. Preencha os dois campos:

   | Campo | Exemplo |
   |---|---|
   | **URL do Servidor** | `https://calls.seudominio.com`, o endereço do painel, sem `/api` no final |
   | **API Key** | o valor de `WACALLS_API_KEY` |

3. Clique em **Save**. Se aparecer ✅, está conectado.
   - **401**: a API Key está errada.
   - **Não conecta**: a URL está errada ou o servidor está fora do ar.

## Passo 3: enviar a primeira mensagem

1. Crie um fluxo com um **Manual Trigger** e adicione o node **AstraCalls** depois dele.
2. Configure o node:
   - **Recurso** `Mensagem` · **Operação** `Enviar Texto`
   - **Sessão**: escolha o número na lista
   - **Para**: `5511999999999`. Use DDI + DDD + número; com ou sem o 9º dígito funciona.
   - **Texto**: `Olá! Primeira mensagem pelo n8n 🚀`
3. Clique em **Test step**. A mensagem chega no WhatsApp, e a resposta do node traz o `id` dela.

## Passo 4: responder mensagens automaticamente

> ⚡ **Atalho:** baixe [`n8n/responder-mensagem.json`](n8n/responder-mensagem.json) e importe no n8n pelo menu **⋯ → Import from File**. Depois escolha a credencial e a sessão nos dois nodes.

Para montar na mão:

1. Adicione o **AstraCalls Trigger**:
   - **Sessão**: o número que vai receber
   - **Eventos**: `Mensagem (Recebida ou Enviada)`
   - Deixe **Ignorar Mensagens Enviadas por Mim** ligado, para o fluxo não responder a si mesmo em loop.
2. Depois do gatilho, adicione o node **AstraCalls** (`Mensagem → Enviar Texto`):
   - **Sessão**: `{{ $json.sessao }}`
   - **Para**: `{{ $json.responderPara }}`. Esse campo já traz o contato certo, ou o grupo quando a mensagem veio de um.
   - **Texto**: `Oi {{ $json.nome }}! Recebi: "{{ $json.texto }}"`
3. **Ative** o fluxo e mande uma mensagem para o número para testar.

Para responder conforme o conteúdo, coloque um **Switch** entre os dois nodes, olhando o campo `texto`. Por exemplo: se contém "preço", envia a tabela.

## Passo 5 (opcional): agente de IA

Para que uma IA atenda pelo WhatsApp:

1. Use o fluxo do passo 4, mas troque o envio por um node **AI Agent**, com o modelo de sua preferência e uma memória.
2. Ligue o **AstraCalls** como **ferramenta (Tool)** do agente, por exemplo com a operação "Enviar Texto". O agente decide sozinho quando enviar mensagens, consultar contatos etc.

> Outra opção é a ferramenta **MCP Client** do n8n apontando para o `/mcp` do AstraCalls, que dá ao agente acesso a todas as ferramentas, sem instalar o node. Veja a seção "MCP para agentes de IA" no [README](../README.md#-mcp-para-agentes-de-ia).

---

## ⚠️ Antes de usar em produção

- **Cada sessão tem um único webhook.** Ao ativar um fluxo com o AstraCalls Trigger, o node coloca o webhook do n8n no lugar do que estava configurado. Ao desativar, restaura o anterior. **Use um só gatilho por sessão** e separe os caminhos com o **Switch**. A integração nativa com o Chatwoot não é afetada.
- **Ligações e disparos em massa** só para quem autorizou o contato. Envio não solicitado bloqueia o número e viola a LGPD e as regras da Anatel.

## Problemas comuns

| Sintoma | Solução |
|---|---|
| A sessão não aparece na lista | Confira a credencial e se a sessão está conectada no painel |
| A sessão aparece como *aguardando QR Code* | Abra o painel do AstraCalls e pareie o número |
| O gatilho não dispara | O fluxo precisa estar **ativo**, e o n8n precisa ser acessível pelo servidor do AstraCalls (URL pública ou mesma rede) |
| Outro fluxo parou de receber eventos | Dois gatilhos na mesma sessão: o último ativado vence (veja o aviso acima) |
