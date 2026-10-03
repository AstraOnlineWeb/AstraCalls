package call

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"wacalls/internal/voip/call/groupmix"
	"wacalls/internal/voip/core"
	"wacalls/internal/voip/media"
	"wacalls/internal/voip/signaling"
	"wacalls/internal/voip/transport"

	waBinary "go.mau.fi/whatsmeow/binary"
	"go.mau.fi/whatsmeow/types"
)

// GroupCallManager orquestra uma chamada em GRUPO (MVP: áudio). Reaproveita os
// blocos do 1:1 (SRTP, codec MLow, relay SCTP) mas com N participantes: cada
// participante remoto tem um contexto SRTP e um decoder próprios, derivados da chave
// de EPOCH compartilhada da call; o áudio de todos é somado por um mixer e entregue
// como um único stream. Mando 1 stream (meu áudio), recebo N.
//
// ATENÇÃO: o control plane (offer, roster, distribuição/ingestão da chave de epoch)
// e a derivação de chave/SSRC estão implementados e a cripto é validada byte-a-byte
// (ver media/group_crypto_test.go). O acoplamento com o RELAY de grupo (estrutura de
// tokens/WARP/HBH-FEC diferente do 1:1 e os PIDs por participante) e a aceitação do
// offer pelo servidor só se validam em CHAMADA REAL — ver internal/voip/call/groupmix/README.md.
// Tudo fica atrás da flag WACALLS_GROUP_CALLS.

type groupParticipant struct {
	participantID string // FormatParticipantID(device JID) — info do HKDF (chave/SSRC)
	deviceJID     types.JID
	pid           int
	audioSSRC     uint32
	srtp          *media.SrtpContext
	codec         media.Codec
}

type GroupCallManager struct {
	sock core.VoipSocket
	log  *slog.Logger

	relay RelayTransport

	mu        sync.Mutex
	callID    string
	creator   types.JID
	selfLID   types.JID
	selfID    string
	groupJID  types.JID
	video     bool
	isCreator bool

	epochKey  []byte
	epochTxID uint32

	selfSsrcs  [9]uint32
	sendSrtp   *media.SrtpContext
	sendCodec  media.Codec
	rtpSession *media.RtpSession

	byDevice map[string]*groupParticipant // participantID -> participante
	bySSRC   map[uint32]*groupParticipant // audioSSRC -> participante

	mixer   *groupmix.Mixer
	framer  groupmix.Framer
	started bool

	captureBuf []float32
	stop       chan struct{}

	OnPeerAudio func([]float32) // áudio MIXADO de todos os participantes (frames de 960)
	OnEnded     func(callID string)
}

// NewGroupCallManager cria um gerenciador de chamada em grupo.
func NewGroupCallManager(sock core.VoipSocket, log *slog.Logger) *GroupCallManager {
	if log == nil {
		log = slog.Default()
	}
	m := &GroupCallManager{
		sock:     sock,
		log:      log,
		byDevice: make(map[string]*groupParticipant),
		bySSRC:   make(map[uint32]*groupParticipant),
		mixer:    groupmix.NewMixer(),
	}
	relay := transport.NewSctpRelayManager(log)
	relay.SetOnReceive(func(data []byte) { m.onRelayData(data) })
	m.relay = relay
	return m
}

// CallID devolve o id da call em andamento (vazio se não houver).
func (m *GroupCallManager) CallID() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.callID
}

// StartGroupCall cria uma chamada em grupo ad-hoc: resolve os devices de cada alvo,
// monta o roster (self + alvos) e envia o offer inicial com group_info. A chave de
// epoch é distribuída depois, quando chega o group_update pedindo rekey.
func (m *GroupCallManager) StartGroupCall(ctx context.Context, targets []types.JID, groupJID types.JID, video bool) (string, error) {
	self := m.sock.OwnLID()
	if self.IsEmpty() {
		return "", &CallError{"no own LID on this session"}
	}
	if len(targets) < 2 {
		return "", &CallError{"group call needs at least 2 targets"}
	}

	// group_info lista só os DESTINOS (remotos). O criador é identificado pelo
	// call-creator; incluir o nosso próprio device como alvo faz o servidor rejeitar
	// com error=427 ("não oferecer pra si mesmo") e a chamada não toca.
	var participants []signaling.GroupCallParticipant
	for _, t := range targets {
		devs, err := m.sock.GetUSyncDevices(ctx, []types.JID{t.ToNonAD()})
		if err != nil {
			return "", fmt.Errorf("group: discover devices for %s: %w", t, err)
		}
		if len(devs) == 0 {
			return "", fmt.Errorf("group: %s has no reachable devices", t)
		}
		p := signaling.GroupCallParticipant{JID: t.ToNonAD()}
		for _, d := range devs {
			p.Devices = append(p.Devices, signaling.GroupCallDevice{JID: d})
		}
		participants = append(participants, p)
	}

	callID := newGroupCallID()
	offer, err := signaling.BuildInitialGroupOffer(signaling.InitialGroupOfferParams{
		CallID: callID, CallCreator: self, GroupJID: groupJID, Participants: participants, Video: video,
	})
	if err != nil {
		return "", err
	}

	m.mu.Lock()
	m.callID = callID
	m.creator = self
	m.selfLID = self
	m.selfID = media.FormatParticipantID(self.String())
	m.groupJID = groupJID
	m.video = video
	m.isCreator = true
	m.mu.Unlock()

	go func() {
		ack, err := m.sock.Query(context.Background(), offer)
		if err != nil {
			m.log.Error("group offer query error", "err", err, "call_id", callID)
			return
		}
		if ack != nil {
			// DIAGNÓSTICO: o ack do servidor diz se o offer de grupo foi aceito ou
			// rejeitado (e por quê). É o que falta pra saber por que não toca.
			m.log.Info("group offer ACK", "call_id", callID, "xml", ack.String())
		}
	}()
	m.log.Info("group offer sent", "call_id", callID, "targets", len(targets), "video", video)
	return callID, nil
}

// HandleUnknownCall roteia um nó de controle de chamada (UnknownCallEvent) para o
// tratamento de grupo: group_update (roster/relay) ou enc_rekey (chave de epoch).
func (m *GroupCallManager) HandleUnknownCall(ctx context.Context, node *waBinary.Node) {
	envelope, err := signaling.ParseCallControlEnvelope(node)
	if err != nil {
		return
	}
	m.log.Info("group control event", "tag", envelope.Action.Tag, "call_id", envelope.CallID, "from", envelope.From.String())
	m.mu.Lock()
	active := m.callID != "" && m.callID == envelope.CallID
	m.mu.Unlock()
	if !active {
		return
	}
	switch envelope.Action.Tag {
	case "group_update":
		update, perr := signaling.ParseGroupUpdate(&envelope.Action)
		if perr != nil {
			m.log.Warn("group: parse update failed", "err", perr)
			return
		}
		m.applyGroupUpdate(ctx, *update)
	case "enc_rekey":
		if err := m.ingestEpoch(ctx, envelope); err != nil {
			m.log.Warn("group: rekey ingest failed", "err", err)
		}
	}
}

// applyGroupUpdate guarda o roster/PIDs, conecta o relay quando vier e, se somos o
// criador e o servidor pediu rekey, distribui a chave de epoch.
func (m *GroupCallManager) applyGroupUpdate(ctx context.Context, update signaling.GroupCallUpdate) {
	m.mu.Lock()
	for _, p := range update.Participants {
		for _, d := range p.Devices {
			if d.JID.IsEmpty() || d.JID == m.selfLID {
				continue
			}
			pid := media.FormatParticipantID(d.JID.String())
			gp := m.byDevice[pid]
			if gp == nil {
				gp = &groupParticipant{participantID: pid, deviceJID: d.JID}
				m.byDevice[pid] = gp
			}
			if d.HasPID {
				gp.pid = int(d.PID)
			}
		}
	}
	relay := update.Relay
	rekey := update.RekeyRequested && m.isCreator
	m.mu.Unlock()

	if relay != nil {
		m.connectRelay(relay)
	}
	if rekey {
		if err := m.distributeEpoch(ctx, update); err != nil {
			m.log.Warn("group: epoch fanout failed", "err", err)
		}
	}
	m.maybeStartMedia()
}

// distributeEpoch (somos o criador): gera a chave de epoch e manda cifrada por device
// a cada participante, via enc_rekey. Reaproveita CreateParticipantNodes (mesmo
// mecanismo do callKey 1:1).
func (m *GroupCallManager) distributeEpoch(ctx context.Context, update signaling.GroupCallUpdate) error {
	m.mu.Lock()
	if len(m.epochKey) == 32 {
		m.mu.Unlock()
		return nil // já temos epoch desta geração
	}
	callID := m.callID
	creator := m.creator
	txid := update.TransactionID
	if txid == 0 {
		txid = 1
	}
	m.mu.Unlock()

	epoch := media.GenerateCallKey() // 32 bytes
	for _, p := range update.Participants {
		for _, d := range p.Devices {
			if d.JID.IsEmpty() || d.JID == m.selfLID {
				continue
			}
			nodes, _, err := m.sock.CreateParticipantNodes(ctx, []types.JID{d.JID}, epoch, waBinary.Attrs{})
			if err != nil || len(nodes) == 0 {
				m.log.Warn("group: encrypt epoch for device failed", "device", d.JID, "err", err)
				continue
			}
			enc := nodes[0]
			encType, _ := enc.Attrs["type"].(string)
			var ct []byte
			if b, ok := enc.Content.([]byte); ok {
				ct = b
			}
			node, err := signaling.BuildGroupEncRekey(signaling.GroupEncRekeyParams{
				CallID: callID, To: d.JID, CallCreator: creator, TransactionID: txid,
				DeviceKey: signaling.OfferDeviceKey{DeviceJid: d.JID, Ciphertext: ct, EncType: encType},
			})
			if err != nil {
				continue
			}
			go func() { _, _ = m.sock.Query(context.Background(), node) }()
		}
	}
	m.installEpoch(txid, epoch)
	return nil
}

// ingestEpoch (somos participante): decifra a chave de epoch do enc_rekey.
func (m *GroupCallManager) ingestEpoch(ctx context.Context, envelope *signaling.CallControlEnvelope) error {
	rekey, err := signaling.ParseGroupCallEncRekey(&envelope.Action)
	if err != nil {
		return err
	}
	enc, ok := envelope.Action.GetOptionalChildByTag("enc")
	if !ok {
		return fmt.Errorf("group rekey without enc payload")
	}
	key, err := m.sock.DecryptCallKey(ctx, envelope.From, &enc)
	if err != nil {
		return fmt.Errorf("decrypt group epoch: %w", err)
	}
	if len(key) != 32 {
		return fmt.Errorf("group epoch is %d bytes, want 32", len(key))
	}
	txid := rekey.TransactionID
	if txid == 0 {
		txid = 1
	}
	m.installEpoch(txid, key)
	return nil
}

// installEpoch guarda a chave, deriva nossas chaves/SSRCs de envio e as de recepção
// de cada participante já conhecido, e tenta iniciar a mídia.
func (m *GroupCallManager) installEpoch(txid uint32, key []byte) {
	m.mu.Lock()
	m.epochKey = key
	m.epochTxID = txid
	callID := m.callID
	selfID := m.selfID

	// Chaves/SSRCs de ENVIO (nosso stream).
	if ssrcs, err := media.DeriveRelayStreamSSRCs(callID, selfID); err == nil {
		m.selfSsrcs = ssrcs
	}
	if km, err := media.DeriveGroupSrtpKeying(key, selfID); err == nil {
		if ctx, err := media.NewSrtpContext(km, core.SRTPAuthTagLen); err == nil {
			m.sendSrtp = ctx
		}
	}
	if m.rtpSession == nil && m.selfSsrcs[0] != 0 {
		m.rtpSession = media.NewWhatsAppOpusSession(m.selfSsrcs[0])
	}
	if m.sendCodec == nil {
		if c, err := media.NewMLowCodec(media.DefaultCodecOptions); err == nil {
			m.sendCodec = c
		}
	}

	// Chaves/SSRCs de RECEPÇÃO por participante.
	for _, gp := range m.byDevice {
		m.setupReceiverLocked(gp)
	}
	m.mu.Unlock()
}

// setupReceiverLocked deriva o SSRC de áudio, o contexto SRTP e o decoder de um
// participante (chamar com m.mu travado).
func (m *GroupCallManager) setupReceiverLocked(gp *groupParticipant) {
	if len(m.epochKey) != 32 || gp == nil {
		return
	}
	ssrcs, err := media.DeriveRelayStreamSSRCs(m.callID, gp.participantID)
	if err != nil {
		return
	}
	gp.audioSSRC = ssrcs[0] // slot 0 = áudio
	if gp.srtp == nil {
		if km, err := media.DeriveGroupSrtpKeying(m.epochKey, gp.participantID); err == nil {
			if ctx, err := media.NewSrtpContext(km, core.SRTPAuthTagLen); err == nil {
				gp.srtp = ctx
			}
		}
	}
	if gp.codec == nil {
		if c, err := media.NewMLowCodec(media.DefaultCodecOptions); err == nil {
			gp.codec = c
		}
	}
	if gp.audioSSRC != 0 {
		m.bySSRC[gp.audioSSRC] = gp
	}
}

// connectRelay mapeia o relay de grupo para a nossa config e conecta. OBS: a estrutura
// de tokens/WARP/HBH-FEC do relay de grupo difere do 1:1 — este mapeamento é
// best-effort e precisa de validação em chamada real.
func (m *GroupCallManager) connectRelay(relay *signaling.GroupCallRelay) {
	var endpoints []core.RelayEndpoint
	for _, ep := range relay.Endpoints {
		var rawToken []byte
		if int(ep.TokenID) < len(relay.Tokens) {
			rawToken = relay.Tokens[ep.TokenID]
		}
		var rawAuth []byte
		if int(ep.AuthTokenID) < len(relay.AuthTokens) {
			rawAuth = relay.AuthTokens[ep.AuthTokenID]
		}
		endpoints = append(endpoints, core.RelayEndpoint{
			IP: ep.IPv4, Port: int(ep.Port), Key: string(relay.Key),
			RawToken: rawToken, RawAuthToken: rawAuth,
			RelayID: int(ep.RelayID), RelayName: ep.RelayName, Protocol: 0,
		})
	}
	relays := buildRelayConfigs(endpoints)
	if len(relays) == 0 {
		m.log.Warn("group: no usable relay configs (precisa validar estrutura do relay de grupo)")
		return
	}

	m.mu.Lock()
	selfSsrcs := m.selfSsrcs[:]
	var peerSsrcs []uint32
	for ssrc := range m.bySSRC {
		peerSsrcs = append(peerSsrcs, ssrc)
	}
	m.mu.Unlock()

	m.relay.SetSsrc(selfSsrcsFirst(selfSsrcs))
	m.relay.SetStreamSsrcs(selfSsrcs, peerSsrcs)
	m.relay.ConfigureRelays(relays)
	m.log.Info("group relay configured", "connected", m.relay.ConnectedCount(), "peers", len(peerSsrcs))
}

// maybeStartMedia liga os loops de mídia quando já há epoch e relay conectado.
func (m *GroupCallManager) maybeStartMedia() {
	m.mu.Lock()
	if m.started || len(m.epochKey) != 32 || !m.relay.HasConnection() {
		m.mu.Unlock()
		return
	}
	m.started = true
	m.stop = make(chan struct{})
	stop := m.stop
	m.mu.Unlock()

	go m.mixLoop(stop)
	go m.sendLoop(stop)
	m.log.Info("group media started", "call_id", m.CallID())
}

// mixLoop puxa chunks mixados (10ms) e os reenquadra em frames de 960 p/ o sink.
func (m *GroupCallManager) mixLoop(stop chan struct{}) {
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			chunk, ok := m.mixer.MixChunk()
			if !ok {
				continue
			}
			if frame, full := m.framer.Push(chunk); full && m.OnPeerAudio != nil {
				m.OnPeerAudio(frame)
			}
		}
	}
}

// sendLoop codifica o áudio capturado (frames de 960) e envia pelo relay.
func (m *GroupCallManager) sendLoop(stop chan struct{}) {
	ticker := time.NewTicker(60 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			m.mu.Lock()
			if m.sendCodec == nil || m.sendSrtp == nil || m.rtpSession == nil || len(m.captureBuf) < 960 {
				m.mu.Unlock()
				continue
			}
			frame := m.captureBuf[:960]
			m.captureBuf = m.captureBuf[960:]
			codec, srtp, rtp := m.sendCodec, m.sendSrtp, m.rtpSession
			m.mu.Unlock()

			opus, err := codec.Encode(frame)
			if err != nil || len(opus) == 0 {
				continue
			}
			pkt := rtp.CreatePacketWithDuration(opus, 960, false)
			protected, err := srtp.Protect(pkt)
			if err != nil {
				continue
			}
			m.relay.Broadcast(protected)
		}
	}
}

// onRelayData demultiplexa o áudio recebido por SSRC → participante → decode → mixer.
func (m *GroupCallManager) onRelayData(data []byte) {
	if !transport.IsRtpPacket(data) || len(data) < 12 {
		return
	}
	if data[1]&0x7f != core.PayloadTypeWhatsAppOpus {
		return // MVP: só áudio
	}
	ssrc := media.RTPSsrc(data)
	m.mu.Lock()
	gp := m.bySSRC[ssrc]
	m.mu.Unlock()
	if gp == nil || gp.srtp == nil || gp.codec == nil {
		return
	}
	pkt, err := gp.srtp.Unprotect(data)
	if err != nil || len(pkt.Payload) == 0 {
		return
	}
	pcm, err := gp.codec.Decode(pkt.Payload)
	if err != nil || len(pcm) == 0 {
		return
	}
	m.mixer.Add(gp.participantID, pcm)
}

// FeedCapturedPCM recebe o áudio do atendente (navegador) para enviar na call.
func (m *GroupCallManager) FeedCapturedPCM(data []float32) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(data) == 0 {
		return
	}
	m.captureBuf = append(m.captureBuf, data...)
	if max := 960 * 8; len(m.captureBuf) > max {
		m.captureBuf = m.captureBuf[len(m.captureBuf)-max:]
	}
}

// End encerra a chamada em grupo e libera os recursos.
func (m *GroupCallManager) End() {
	m.mu.Lock()
	if m.stop != nil {
		close(m.stop)
		m.stop = nil
	}
	callID := m.callID
	m.callID = ""
	m.started = false
	for _, gp := range m.byDevice {
		if gp.codec != nil {
			gp.codec.Close()
		}
	}
	if m.sendCodec != nil {
		m.sendCodec.Close()
		m.sendCodec = nil
	}
	m.byDevice = make(map[string]*groupParticipant)
	m.bySSRC = make(map[uint32]*groupParticipant)
	m.mu.Unlock()

	m.relay.Cleanup()
	if m.OnEnded != nil && callID != "" {
		m.OnEnded(callID)
	}
}

func selfSsrcsFirst(s []uint32) uint32 {
	for _, v := range s {
		if v != 0 {
			return v
		}
	}
	return 0
}

func newGroupCallID() string {
	return signaling.GenerateCallID()
}
