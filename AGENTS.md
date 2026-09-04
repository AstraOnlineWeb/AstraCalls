# AGENTS.md — AstraCalls

Guia de rotina para quem (pessoa ou agente) for buildar, publicar e implantar o
AstraCalls. Foco na **build da imagem Docker** e no **deploy**.

## Regra de ouro da imagem: multi-arch obrigatório

A imagem `astraonline/astracalls` **precisa servir `linux/amd64` E `linux/arm64`**.
O `Dockerfile` é arch-aware (compila o codec MLow com NEON no ARM e baseline
SSE2 no x86). Uma build single-arch (`docker build` comum) gera um manifesto de
uma arquitetura só — **isso é considerado quebrado**.

## Como buildar: via CI (nunca na VPS de produção)

O build multi-arch é feito **na CI**, com build **nativo por arquitetura** (sem
QEMU):

- **amd64** → runner x86 do GitHub (`ubuntu-latest`)
- **arm64** → runner ARM **efêmero** do Ubicloud (`ubicloud-standard-8-arm`)

Cada job publica por **digest**; um job `merge` monta o **manifest list** nas
tags finais. Workflow: [`.github/workflows/docker-multiarch.yml`](.github/workflows/docker-multiarch.yml).

### Gatilhos

| Evento | Resultado |
|--------|-----------|
| push em `develop` / `main` | publica `:develop` (ou `:main`) multi-arch |
| tag `v*` | publica `:vX.Y.Z` + `:latest` multi-arch |
| manual (`workflow_dispatch`) | roda sob demanda |

Ou seja: **para gerar imagem nova, basta dar push** (ou criar a tag de versão).
Não é necessário — nem recomendado — buildar imagem na VPS.

### Pré-requisitos (configurar uma vez)

- Repositório conectado ao **Ubicloud** (GitHub Actions runners), para o label
  `ubicloud-standard-8-arm` resolver numa VM ARM efêmera.
- Secrets do repositório: `DOCKERHUB_USERNAME` e `DOCKERHUB_TOKEN`.
- O checkout usa `submodules: recursive` (o `opus_mlow` é submódulo).

### Por que não buildar na VPS

O build arm64 na VPS depende de emulação QEMU e, somado às bases das duas
arquiteturas, estoura o disco (cronicamente ~90%+) e pode derrubar o Postgres.
Se **em emergência** precisar buildar na VPS, faça **uma arquitetura por vez**
(`buildx --platform linux/<arch>`), com `docker builder prune` + `image prune`
antes e depois, e monte o manifesto com `docker buildx imagetools create`.

## Deploy (Docker Swarm)

```bash
docker service update --with-registry-auth \
  --image astraonline/astracalls:develop wacalls_wacalls
```

Num nó amd64 o manifesto resolve automaticamente para o sub-manifesto amd64.
Sempre verifique o site após o deploy (`https://call.trecofantastico.com.br/`).

## Versionamento

Não corte versão (tag `v*` / release / `CHANGELOG`) sem pedido explícito.
Correções na `develop` são livres. Atualize o `CHANGELOG.md` ao cortar release.
