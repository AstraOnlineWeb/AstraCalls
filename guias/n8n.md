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

## 📚 10 fluxos de exemplo prontos

Baixe o `.json`, importe no n8n pelo menu **⋯ → Import from File**, escolha a credencial e a **Sessão** em cada node AstraCalls e troque os números de exemplo (`5511999999999`). Cada fluxo tem uma nota amarela explicando como funciona.

| # | Fluxo | O que faz |
|---|---|---|
| 01 | [Resposta fora do horário](n8n/exemplos/exemplo-01-resposta-fora-do-horario.json) | Responde automaticamente a quem escreve fora do expediente |
| 02 | [Menu com botões](n8n/exemplos/exemplo-02-menu-com-botoes.json) | Envia um menu com botões e segue um caminho para cada opção (Switch) |
| 03 | [Lista de serviços e agendamento](n8n/exemplos/exemplo-03-lista-de-servicos-e-agendamento.json) | Envia uma lista interativa, confirma a opção escolhida e reage com ✅ |
| 04 | [Atendente de IA com ferramentas](n8n/exemplos/exemplo-04-atendente-de-ia-com-ferramentas.json) | AI Agent com memória que usa o AstraCalls como ferramenta (requer credencial OpenAI) |
| 05 | [Pesquisa de satisfação (enquete)](n8n/exemplos/exemplo-05-pesquisa-de-satisfacao-enquete.json) | Envia uma enquete para os clientes, agradece cada voto e avisa o gestor se a nota for ruim |
| 06 | [Boas-vindas no grupo](n8n/exemplos/exemplo-06-boas-vindas-no-grupo.json) | Dá boas-vindas a quem entra no grupo, mencionando a pessoa |
| 07 | [Disparo de ligações com áudio](n8n/exemplos/exemplo-07-disparo-de-ligacoes-com-audio.json) | Liga para uma lista tocando um áudio gravado e acompanha o status |
| 08 | [Validar lista antes da campanha](n8n/exemplos/exemplo-08-validar-lista-antes-da-campanha.json) | Confere quais números têm WhatsApp e só envia para eles |
| 09 | [Lembrete diário com toque](n8n/exemplos/exemplo-09-lembrete-diario-com-toque.json) | Todo dia, no horário marcado, envia um lembrete e faz o telefone tocar |
| 10 | [Alerta de desconexão e restrição](n8n/exemplos/exemplo-10-alerta-de-desconexao-e-restricao.json) | Avisa quando a sessão cai ou recebe restrição do WhatsApp |

> Os fluxos com **AstraCalls Trigger** (01 a 06 e 10) trocam o webhook da sessão quando ativados. Ative **um por sessão**, ou junte os caminhos num só fluxo (veja o aviso abaixo).

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
