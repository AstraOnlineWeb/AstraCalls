# Chamada em GRUPO — plano de implementação (branch feat/group-calls)

Status: **fundação iniciada**. Esta branch tem a base portável e testável; a parte de
mídia que precisa bater byte-a-byte com o servidor do WhatsApp (e só valida em
chamada real) ainda NÃO está. Referência: `github.com/purpshell/meowcaller` (MIT).

## Por que não dá pra usar o meowcaller como lib
Ele roda sobre o fork `polymorfa/hypermeow`; nós sobre `go.mau.fi/whatsmeow`. Tipos
incompatíveis, puxaria um 2º whatsmeow e exigiria 2 sessões. => **portar**, não importar.
(O vídeo do meowcaller, aliás, foi portado do NOSSO código — JotaDev66/WaCalls.)

## Já feito nesta branch
- `internal/voip/call/groupmix/` — mixer de áudio multi-participante (soma + clamp) e
  reenquadramento em frames de 960, Go puro, com testes. Portado de
  `meowcaller/group_audio_mixer.go`.
- `internal/voip/media/group_crypto.go` — derivação de chave de epoch + SSRC de grupo +
  FormatParticipantID. **Validado byte-a-byte** contra o meowcaller (vetores dourados em
  `group_crypto_test.go`). Reusa a nossa SRTP (idêntica à E2E do meowcaller).
- `internal/voip/signaling/group.go` — signaling de grupo portada (offer+group_info,
  group_update, enc_rekey; build e parse).
- `internal/voip/transport/subscriptions.go` — `BuildGroupSubscriptionList` (N PIDs).
- `internal/voip/call/groupcall.go` — `GroupCallManager`: offer, roster, distribuir/ingerir
  a chave de epoch (reusa CreateParticipantNodes/DecryptCallKey), per-participant SRTP+codec,
  demux de recepção por SSRC → mixer → áudio, loop de envio. Atrás da flag.
- `cmd/server/groupcall.go` — endpoints `POST /calls/group` e `/calls/group/{id}/end`
  (501 sem a flag `WACALLS_GROUP_CALLS=1`); roteamento de eventos de grupo no
  handleUnknownCall. Documentado no OpenAPI.

## Teste ao vivo 2026-10-03 (grupo Geral1, 3 membros) — achados
Disparado via POST /calls/group pro grupo existente. 3 BUGS REAIS corrigidos:
1. `<call>` sem atributo `id` → servidor IGNORA o offer (sem ack, sem ring). O
   gCallWrap do grupo não gerava id. FIX: gera GenerateCallStanzaID() (commit 633ef48).
2. (investigado) self no group_info com o nosso device → ack `error=427` no self.
3. (investigado) offer completo estilo 1:1 (group_info + `<destination>` + `<encopt>`)
   → servidor volta a ficar SILENCIOSO (sem ack). Misturar group_info + destination
   NÃO é o shape certo.

Comportamento observado do servidor (ACK logado em StartGroupCall):
- group_info-only + self incluído → `<ack class=call error=427 type=offer>` com
  `<user error=427 jid=SELF>` e os 2 REMOTOS ACEITOS (devices resolvidos, sem erro).
- group_info-only + self excluído → `error=400` num user fantasma `<callid>@call`
  (o servidor ESPERA o self no roster), remotos ainda aceitos.
- group_info + destination/encopt (estilo 1:1) → SEM ack (drop silencioso).
Em TODOS os casos os 2 destinos são aceitos, mas NENHUM device toca.

CONCLUSÃO: o servidor processa o offer e resolve os membros, mas o handshake exato
que faz TOCAR não bate. Para fechar é preciso CAPTURAR uma chamada de grupo REAL do
WhatsApp Web (extensão `meowcaller/diag`) e comparar o offer/ack/sequência
byte-a-byte — iterar às cegas é caro (servidor alterna erro/drop por formato).
Estado do código: voltou ao group_info-only (ref. meowcaller) + id + log de ACK.

## Precisa de CHAMADA REAL pra validar/fechar (não dá pra testar sem número)
- Aceitação do offer de grupo pelo servidor (formato do group_info/capability).
- Estrutura do RELAY de grupo (tokens/WARP/HBH-FEC diferem do 1:1): o mapeamento em
  `connectRelay` é best-effort; os PIDs por participante na assinatura
  (`BuildGroupSubscriptionList`) ainda não estão ligados ao allocate do relay.
- Ligar o áudio ao navegador: `gc.OnPeerAudio` (saída mixada) e `gc.FeedCapturedPCM`
  (entrada do atendente) ao bridge WS, como no 1:1.
- Vídeo de grupo, call-links e waiting-room (fases seguintes).

## O que falta (ordem sugerida) — tudo valida só em CHAMADA REAL
1. **Estado (callstate.go):** roster `[]Participant` (device, participantID/PID, estado),
   `GroupEpochKey []byte` + `EpochTxID`, usar o placeholder `GroupJid`. `peerSsrcs`
   virar N (hoje é sempre 1 elemento).
2. **Derivação de chave/SSRC de grupo** (o ponto mais sensível — tem que casar com o
   servidor): porta de `meowcaller/rtp/ssrc.go` (`DeriveWasmParticipantSsrc` HKDF
   salt=slotWord LE32/ikm=callID/info=lid; 9 slots `WasmRelayStreamSlotWords`;
   `FormatE2ESrtpParticipantID`) e `meowcaller/srtp/e2e.go` (`DeriveE2eKeysFromRaw` =
   HKDF(salt=zeros32, ikm=epoch[:32], info=participantID, 46) -> AES-CM KDF p/
   cipher/auth/salt). OBS: nosso `media.DerivePerJidSrtpKey` já é o MESMO HKDF; falta
   a variante com `participantID` formatado e o AES-CM KDF das session keys.
3. **Epoch keygen-v2:** gerar a chave única da call, distribuir cifrada por-device no
   stanza `enc_rekey` (cifrar com `DangerousInternals().EncryptMessageForDevices` ou
   equivalente; conteúdo = `waE2E.Message.Call.CallKey`), e INGERIR do peer no nosso
   `handleUnknownCall` (já existe) decifrando com `DecryptDM`.
4. **SRTP multi-stream:** nossa `media.SrtpSession` tem 1 recvCtx -> N contexts, 1 por
   SSRC/participante (chaves do item 2). Registry de recepção estilo
   `meowcaller/group_media_receive.go` (mapas bySSRC/byVideoSSRC/byAppDataSSRC).
5. **Demux RX (callmanager_media.go):** tirar a trava "primeiro SSRC vence"
   (linhas ~185-192) e rotear SSRC->participante; alimentar o `groupmix.Mixer`.
6. **Relay de grupo:** Allocate com N assinaturas (9 stream SSRCs + appDataSSRC +
   2 HBH-FEC + PIDs dos participantes), estendendo `transport/subscriptions.go`
   (`BuildSSRCSubscriptionList`) + `transport/sctprelay.go` (`SetStreamSsrcs`).
   Mando 1 stream (meu áudio), recebo N; o relay faz o fan-in/out.
7. **Signaling de grupo:** porta de `meowcaller/signaling/group.go` (builders/parsers de
   `offer`+`group_info`, `group_update`, `enc_rekey`), trocando os tipos
   hypermeow->go.mau.fi/whatsmeow. Plugar no `handleUnknownCall`.
8. **Endpoints** (atrás de flag `WACALLS_GROUP_CALLS` até validar):
   - `POST /api/sessions/{sid}/calls/group` `{targets[2..31], groupJid?, video?}`
   - `POST /api/sessions/{sid}/calls/{id}/participants` `{target, ring?}`
   - `GET /api/sessions/{sid}/calls/{id}` (roster + estados)
   - Fase 2/3: call-links, waiting-room, hand/screenshare, vídeo de grupo.

## Riscos
- EXPERIMENTAL até no meowcaller (vários `// NOT VALIDATED`). A maior fatia de esforço
  é **validar em chamada real** (começar com 2 participantes).
- Derivação SSRC/chave tem que casar com o WhatsApp oficial; divergência = relay não
  faz bridge / áudio não autentica. Usar o `meowcaller/diag` (extensão de browser que
  captura a call do WhatsApp Web oficial) pra comparar.
- Mexe no caminho crítico de mídia (hoje estável no 1:1) -> manter atrás de flag e NÃO
  regredir o 1:1.

## MVP
Grupo só de ÁUDIO, ad-hoc (sem link/waiting room/vídeo): itens 1-8 (sem vídeo/links),
validado com 2 participantes reais.
