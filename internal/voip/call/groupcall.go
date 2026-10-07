package call

import (
	"bytes"
	"context"
	"crypto/rand"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"wacalls/internal/voip/call/groupmix"
	callvideo "wacalls/internal/voip/call/video"
	"wacalls/internal/voip/core"
	"wacalls/internal/voip/media"
	"wacalls/internal/voip/signaling"
	"wacalls/internal/voip/transport"
	"wacalls/internal/voip/wanode"

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

	videoSSRC uint32              // SSRC de vídeo do participante (slot 2)
	videoPipe *callvideo.Pipeline // pipeline de recepção de vídeo (H264 → AU)

	srtcpKeys    *media.SrtcpKeys // chaves SRTCP do participante (p/ autenticar o RTCP dele)
	keysTxID     uint32           // epoch (transaction-id) do qual as chaves acima foram derivadas
	wasConnected bool             // já esteve "connected" no roster (p/ detectar saída: volta a invited)
	videoPkts    uint64           // diagnóstico: pacotes de vídeo recebidos dele
	gotIDR       bool             // já entregamos um AU com keyframe dele (vídeo decodificável)
	lastPLI      time.Time        // último PLI que mandamos pra ele (rate limit)
}

// groupVideoRelay adapta o canal DTLS de grupo à interface Relay do pipeline de vídeo
// (só o Broadcast importa; o relay de grupo gerencia a assinatura via Allocate).
type groupVideoRelay struct{ ch *transport.GroupRelayChannel }

func (r groupVideoRelay) Broadcast(data []byte) {
	if r.ch != nil {
		_, _ = r.ch.Send(data)
	}
}
func (r groupVideoRelay) BufferedAmount() uint64       { return 0 }
func (r groupVideoRelay) HasConnection() bool          { return r.ch != nil }
func (r groupVideoRelay) SetStreamSsrcs(_, _ []uint32) {}

type GroupCallManager struct {
	sock core.VoipSocket
	log  *slog.Logger

	// Canal de mídia do relay de GRUPO (DTLS direto). O grupo NÃO usa o
	// SctpRelayManager (WebRTC/ICE) do 1:1 — conecta direto, igual ao WhatsApp Web.
	groupChan *transport.GroupRelayChannel
	groupKey  []byte
	recvStop  chan struct{}

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

	groupRelay    *signaling.GroupCallRelay // relay do grupo (do group_update)
	rosterTxID    uint32                    // transaction-id do último group_update aplicado (descarta antigos/fora de ordem)
	connectedPIDs []uint32                  // PIDs dos participantes remotos conectados
	relayStarted  bool                      // já configurou o relay de grupo

	selfSsrcs  [9]uint32
	sendSrtp   *media.SrtpContext
	sendCodec  media.Codec
	rtpSession *media.RtpSession

	byDevice    map[string]*groupParticipant // participantID -> participante
	bySSRC      map[uint32]*groupParticipant // audioSSRC -> participante
	byVideoSSRC map[uint32]*groupParticipant // videoSSRC -> participante
	bySrtcpSSRC map[uint32]*groupParticipant // qualquer SSRC de stream do participante -> participante (RTCP)
	epochSentTo map[string]bool              // device JID -> já mandamos a chave de epoch (p/ joiners tardios)

	sendVideoPipe *callvideo.Pipeline // pipeline de ENVIO do nosso vídeo (câmera)
	selfVideoSSRC uint32              // SSRC do nosso vídeo (slot 2)

	// SRTCP: os celulares associam os streams RTP a uma sessão SRTCP (SR+SDES
	// periódicos) e só mandam keyframe de vídeo sob PEDIDO (PLI). Sem isso o vídeo
	// de grupo não abre de forma confiável nos dois sentidos.
	srtcpAudio     *media.SrtcpSender
	srtcpVideo     *media.SrtcpSender
	audioStats     media.RtcpSenderStats
	videoAnnounced bool // já mandamos <video state=1> anunciando nossa câmera

	wrappedN      uint64 // diagnóstico: pacotes com cabeçalho de group-forwarding
	rtcpN         uint64 // diagnóstico: RTCP autenticado recebido
	pliN          uint64 // diagnóstico: PLIs enviados
	videoUnknownN uint64 // diagnóstico: vídeo de SSRC desconhecido

	mixer   *groupmix.Mixer
	framer  groupmix.Framer
	started bool

	captureBuf []float32
	stop       chan struct{}

	recvN  uint64 // diagnóstico: pacotes recebidos do relay de grupo
	audioN uint64 // diagnóstico: pacotes de áudio decodificados
	sentN  uint64 // diagnóstico: pacotes RTP enviados

	OnPeerAudio func([]float32)                       // áudio MIXADO de todos os participantes (frames de 960)
	OnPeerVideo func(participantID string, au []byte) // vídeo H264 (AU) por participante → navegador
	OnEnded     func(callID string)

	OnKeyframeRequest      func()                                      // um participante pediu keyframe (PLI/FIR) do NOSSO vídeo
	OnPeerVideoState       func(participantID string, state int)       // participante ligou/desligou a câmera (<video state=N>)
	OnPeerVideoOrientation func(participantID string, orientation int) // orientação da câmera do participante mudou (0..3)
	OnParticipantLeft      func(participantID string)                  // participante saiu da chamada (sumiu do roster)
}

// NewGroupCallManager cria um gerenciador de chamada em grupo.
func NewGroupCallManager(sock core.VoipSocket, log *slog.Logger) *GroupCallManager {
	if log == nil {
		log = slog.Default()
	}
	return &GroupCallManager{
		sock:        sock,
		log:         log,
		byDevice:    make(map[string]*groupParticipant),
		epochSentTo: make(map[string]bool),
		bySSRC:      make(map[uint32]*groupParticipant),
		byVideoSSRC: make(map[uint32]*groupParticipant),
		bySrtcpSSRC: make(map[uint32]*groupParticipant),
		mixer:       groupmix.NewMixer(),
	}
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

	// Capability do nosso device: áudio ou vídeo de grupo.
	selfCap := signaling.CapabilityGroupOffer
	if video {
		selfCap = signaling.CapabilityGroupVideoOffer
	}
	// NOSSO usuário precisa listar TODOS os nossos devices (celular + vinculados), não
	// só o device do gateway — senão o servidor recusa nossa própria entrada com
	// erro 411 (phash/device-list incompatível) e a chamada não engata de verdade. O
	// celular só precisa estar LISTADO (p/ o hash bater); ele NÃO precisa entrar na call.
	// A capability de grupo vai só no nosso device (o gateway).
	var selfDevices []signaling.GroupCallDevice
	seenSelf := false
	if devs, err := m.sock.GetUSyncDevices(ctx, []types.JID{self.ToNonAD()}); err == nil {
		for _, d := range devs {
			dev := signaling.GroupCallDevice{JID: d}
			if d == self {
				dev.CapabilityVersion = 1
				dev.Capability = append([]byte(nil), selfCap...)
				seenSelf = true
			}
			selfDevices = append(selfDevices, dev)
		}
	} else {
		m.log.Warn("group: não consegui listar nossos devices, uso só o gateway", "err", err)
	}
	if !seenSelf {
		selfDevices = append(selfDevices, signaling.GroupCallDevice{
			JID: self, CapabilityVersion: 1, Capability: append([]byte(nil), selfCap...),
		})
	}
	// Descobre devices de cada alvo (um participante por usuário, com seus devices).
	participants := []signaling.GroupCallParticipant{{
		JID:     self.ToNonAD(),
		Devices: selfDevices,
	}}
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

	// Zera TODO o estado da chamada anterior antes de instalar a nova. Sem isso,
	// uma chamada que não foi encerrada por End() (ex.: todos saíram, o atendente
	// não desligou e já ligou de novo) deixava rosterTxID/epochTxID da chamada
	// velha; os group_update/enc_rekey da nova eram descartados como "antigos"
	// ou "conflitantes", o epoch nunca instalava, o relay nunca conectava e os
	// participantes ficavam presos em "conectando" (bug reportado 07/10).
	if prev := m.CallID(); prev != "" {
		m.log.Info("group: chamada anterior ainda ativa; encerrando antes de iniciar outra", "prev_call_id", prev)
	}
	m.End()

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
	case "video":
		m.handlePeerVideoState(ctx, node, envelope)
	case "terminate":
		// O servidor encerrou a chamada de grupo (group_call_ended, todos saíram…):
		// derruba a nossa ponta também, senão o painel/widget fica "em chamada".
		reason, _ := envelope.Action.Attrs["reason"].(string)
		m.log.Info("group: terminate recebido", "call_id", envelope.CallID, "reason", reason)
		m.End()
	}
}

// handlePeerVideoState trata o <video state=N> de um participante do grupo: manda o
// ack TIPADO (type="video") que o WhatsApp exige — sem ele o aparelho fica
// retransmitindo (transaction-id crescente) e trata como não-aceito —, marca que o
// vídeo dele vai (re)começar (pede keyframe) e avisa o painel.
func (m *GroupCallManager) handlePeerVideoState(ctx context.Context, node *waBinary.Node, envelope *signaling.CallControlEnvelope) {
	if ack, ok := signaling.BuildVideoAck(node); ok {
		if err := m.sock.SendNode(ctx, ack); err != nil {
			m.log.Warn("group: ack tipado de <video> falhou", "err", err)
		}
	}
	state := wanode.AttrInt(envelope.Action.Attrs, "state", -1)
	orientation := wanode.AttrInt(envelope.Action.Attrs, "device_orientation", -1)
	pid := media.FormatParticipantID(envelope.From.String())

	m.mu.Lock()
	// O from vem SEM o id de device (ex.: 1449…@lid) e o roster é por device
	// (1449…:55@lid): casa pelo usuário.
	for id, gp := range m.byDevice {
		if gp.deviceJID.User == envelope.From.User {
			pid = id
			if state == signaling.VideoStateEnabled {
				// Câmera (re)ligada: o próximo vídeo dele começa num keyframe novo — pede.
				gp.gotIDR = false
				gp.lastPLI = time.Time{}
			}
		}
	}
	cb := m.OnPeerVideoState
	m.mu.Unlock()

	m.log.Info("group: vídeo do participante", "pid", pid, "state", state, "orientation", orientation)
	if cb != nil {
		cb(pid, state)
	}
	// Rotação do aparelho anunciada no stanza (além dos bits CVO do RTP).
	if orientation >= 0 && orientation <= 3 && state == signaling.VideoStateEnabled {
		m.mu.Lock()
		ocb := m.OnPeerVideoOrientation
		m.mu.Unlock()
		if ocb != nil {
			ocb(pid, orientation)
		}
	}
}

// announceVideoState anuncia aos participantes que NOSSA câmera está ligada
// (<video state=1 dec=H264>), como o WhatsApp Web faz numa chamada de grupo com
// vídeo. Sem o anúncio os aparelhos ficam com nosso tile em "conectando".
func (m *GroupCallManager) announceVideoState(ctx context.Context, enabled bool) {
	m.mu.Lock()
	callID, creator := m.callID, m.creator
	if callID == "" {
		m.mu.Unlock()
		return
	}
	if enabled && m.videoAnnounced {
		m.mu.Unlock()
		return
	}
	m.videoAnnounced = enabled
	m.mu.Unlock()

	orientation := 0
	state, dec := signaling.VideoStateDisabled, ""
	if enabled {
		state, dec = signaling.VideoStateEnabled, signaling.VideoDecRequest
	}
	node := signaling.BuildVideoStateStanza(signaling.VideoStateParams{
		CallID: callID, To: types.NewJID(callID, "call"), CallCreator: creator,
		State: state, Dec: dec, DeviceOrientation: &orientation,
	})
	if err := m.sock.SendNode(ctx, node); err != nil {
		m.log.Warn("group: anúncio <video state> falhou", "err", err, "state", state)
		return
	}
	m.log.Info("group: anunciamos nosso vídeo", "call_id", callID, "state", state)
}

// applyGroupUpdate guarda o roster/PIDs, conecta o relay quando vier e, se somos o
// criador e o servidor pediu rekey, distribui a chave de epoch.
func (m *GroupCallManager) applyGroupUpdate(ctx context.Context, update signaling.GroupCallUpdate) {
	m.mu.Lock()
	// Os nós são tratados em goroutines: um update antigo pode chegar DEPOIS de um
	// mais novo. O roster é autoritativo por transaction-id — só avança.
	if update.TransactionID != 0 && update.TransactionID <= m.rosterTxID {
		m.mu.Unlock()
		m.log.Info("group: update de roster antigo ignorado", "tx", update.TransactionID, "roster_tx", m.rosterTxID)
		return
	}
	if update.TransactionID != 0 {
		m.rosterTxID = update.TransactionID
	}
	var pids []uint32
	present := make(map[string]bool, len(m.byDevice))
	for _, p := range update.Participants {
		connected := p.State == "connected"
		if groupStateDeparted(p.State) {
			continue // saiu/recusou: não (re)cria receiver; cai na remoção abaixo
		}
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
			// Quem SAI não some do roster: o WhatsApp mantém o convite e o usuário volta
			// de "connected" pra "invited". Essa transição é a saída.
			if gp.wasConnected && !connected {
				continue // cai na remoção abaixo (não marca presente)
			}
			if connected {
				gp.wasConnected = true
			}
			present[pid] = true
			if d.HasPID {
				gp.pid = int(d.PID)
				if connected {
					pids = append(pids, d.PID) // só conectados entram na assinatura do relay
				}
			}
			// Configura o receiver (SSRC/SRTP/codec/videoPipe) de CADA participante —
			// inclusive os que entram DEPOIS do epoch. Sem isto, um joiner tardio (3º+)
			// não era decodificado e o host não via/ouvia ele. Idempotente (no-op sem epoch
			// ou se já montado).
			m.setupReceiverLocked(gp)
		}
	}
	m.connectedPIDs = pids
	if update.Relay != nil {
		m.groupRelay = update.Relay
	}
	rekey := update.RekeyRequested && m.isCreator
	// Quem SAIU (sumiu do roster ou veio com estado terminal): remove o receiver
	// dele — senão fica um stream morto no mixer/painel até o fim da chamada.
	var left []*groupParticipant
	for id, gp := range m.byDevice {
		if present[id] {
			continue
		}
		left = append(left, gp)
		delete(m.byDevice, id)
		delete(m.bySSRC, gp.audioSSRC)
		delete(m.byVideoSSRC, gp.videoSSRC)
		for s, g := range m.bySrtcpSSRC {
			if g == gp {
				delete(m.bySrtcpSSRC, s)
			}
		}
	}
	// O mixer só soma quem está no roster atual (quem saiu para de contribuir; quem
	// entra passa a ser aceito).
	ids := make([]string, 0, len(m.byDevice))
	for id := range m.byDevice {
		ids = append(ids, id)
	}
	m.mixer.Retain(ids)
	cbLeft := m.OnParticipantLeft
	m.mu.Unlock()

	for _, gp := range left {
		if gp.codec != nil {
			gp.codec.Close()
		}
		if gp.videoPipe != nil {
			gp.videoPipe.Reset()
		}
		m.log.Info("group: participante saiu da chamada", "pid", gp.participantID, "tx", update.TransactionID)
		if cbLeft != nil {
			cbLeft(gp.participantID)
		}
	}

	// Como criador, garantimos que TODO device (inclusive joiners tardios) receba a
	// chave de epoch. allowGenerate=rekey: só geramos uma chave nova quando o servidor
	// pede rekey; nas demais updates só reenviamos a chave existente aos devices novos.
	if m.isCreator {
		if err := m.distributeEpoch(ctx, update, rekey); err != nil {
			m.log.Warn("group: epoch fanout failed", "err", err)
		}
	}
	m.tryStartRelay()
	m.maybeStartMedia()
}

// distributeEpoch (somos o criador): gera a chave de epoch e manda cifrada por device
// a cada participante, via enc_rekey. Reaproveita CreateParticipantNodes (mesmo
// mecanismo do callKey 1:1).
func (m *GroupCallManager) distributeEpoch(ctx context.Context, update signaling.GroupCallUpdate, allowGenerate bool) error {
	m.mu.Lock()
	callID := m.callID
	creator := m.creator
	txid := update.TransactionID
	if txid == 0 {
		txid = 1
	}
	epoch := append([]byte(nil), m.epochKey...)
	newEpoch := false
	if len(epoch) != 32 {
		if !allowGenerate {
			m.mu.Unlock()
			return nil // ainda sem epoch e o servidor não pediu rekey: espera
		}
		epoch = media.GenerateCallKey() // 32 bytes
		newEpoch = true
	}
	// Devices que ainda NÃO receberam o epoch (inclui joiners tardios). Reenviar aos que
	// já têm só glitcha a mídia deles, então mandamos a cada device UMA vez.
	var targets []types.JID
	for _, p := range update.Participants {
		for _, d := range p.Devices {
			if d.JID.IsEmpty() || d.JID == m.selfLID || m.epochSentTo[d.JID.String()] {
				continue
			}
			m.epochSentTo[d.JID.String()] = true
			targets = append(targets, d.JID)
		}
	}
	m.mu.Unlock()

	for _, dj := range targets {
		nodes, _, err := m.sock.CreateParticipantNodes(ctx, []types.JID{dj}, epoch, waBinary.Attrs{})
		if err != nil || len(nodes) == 0 {
			m.log.Warn("group: encrypt epoch for device failed", "device", dj, "err", err)
			m.mu.Lock()
			delete(m.epochSentTo, dj.String()) // falhou: permite nova tentativa na próxima update
			m.mu.Unlock()
			continue
		}
		enc := nodes[0]
		encType, _ := enc.Attrs["type"].(string)
		var ct []byte
		if b, ok := enc.Content.([]byte); ok {
			ct = b
		}
		node, err := signaling.BuildGroupEncRekey(signaling.GroupEncRekeyParams{
			CallID: callID, To: dj, CallCreator: creator, TransactionID: txid,
			DeviceKey: signaling.OfferDeviceKey{DeviceJid: dj, Ciphertext: ct, EncType: encType},
		})
		if err != nil {
			continue
		}
		go func() { _, _ = m.sock.Query(context.Background(), node) }()
	}
	if newEpoch {
		m.installEpoch(txid, epoch)
	}
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
// installEpoch instala UM epoch compartilhado da call (transaction-id crescente).
// A chave é ÚNICA para todos e é ROTACIONADA a cada entrada: quem entra distribui
// um epoch novo via enc_rekey. Ao aceitar um epoch, TODAS as chaves são rederivadas
// (nosso envio de áudio/vídeo/SRTCP e a recepção de cada participante) preservando
// ROC/sequência — sem isso, a partir do 3º participante o áudio/vídeo embaralha.
func (m *GroupCallManager) installEpoch(txid uint32, key []byte) {
	m.mu.Lock()
	if len(m.epochKey) == 32 {
		switch {
		case txid < m.epochTxID:
			m.mu.Unlock()
			m.log.Info("group: epoch antigo ignorado", "tx", txid, "installed_tx", m.epochTxID)
			return
		case txid == m.epochTxID && bytes.Equal(m.epochKey, key):
			m.mu.Unlock()
			return
		case txid == m.epochTxID:
			m.mu.Unlock()
			m.log.Warn("group: epoch conflitante p/ a mesma transaction ignorado", "tx", txid)
			return
		}
	}
	rotation := len(m.epochKey) == 32
	m.epochKey = key
	m.epochTxID = txid
	callID := m.callID
	selfID := m.selfID

	// Chaves/SSRCs de ENVIO (nosso stream).
	if ssrcs, err := media.DeriveRelayStreamSSRCs(callID, selfID); err == nil {
		m.selfSsrcs = ssrcs
	}
	if km, err := media.DeriveGroupSrtpKeying(key, selfID); err == nil {
		if m.sendSrtp != nil {
			if ctx, err := m.sendSrtp.WithKeying(km); err == nil {
				m.sendSrtp = ctx
			}
		} else if ctx, err := media.NewSrtpContext(km, core.SRTPAuthTagLen); err == nil {
			m.sendSrtp = ctx
		}
		if m.sendVideoPipe != nil {
			if err := m.sendVideoPipe.RekeyGroup(km, km); err != nil {
				m.log.Warn("group: rekey do pipeline de vídeo de envio falhou", "err", err)
			}
		}
	}
	if m.rtpSession == nil && m.selfSsrcs[0] != 0 {
		m.rtpSession = media.NewWhatsAppOpusSession(m.selfSsrcs[0])
	}
	// Emissores SRTCP (SR+SDES periódicos + PLI) dos nossos streams.
	if m.selfSsrcs[0] != 0 {
		if m.srtcpAudio != nil {
			_ = m.srtcpAudio.Rekey(key, selfID)
		} else if s, err := media.NewSrtcpSender(key, selfID, m.selfSsrcs[0], false); err == nil {
			m.srtcpAudio = s
		}
	}
	if m.video {
		if vs, err := media.DeriveParticipantSSRC(callID, selfID, media.GroupVideoSlotWord); err == nil && vs != 0 {
			m.selfVideoSSRC = vs
			if m.srtcpVideo != nil {
				_ = m.srtcpVideo.Rekey(key, selfID)
			} else if s, err := media.NewSrtcpSender(key, selfID, vs, true); err == nil {
				m.srtcpVideo = s
			}
		}
	}
	if m.sendCodec == nil {
		if c, err := media.NewMLowCodec(media.DefaultCodecOptions); err == nil {
			m.sendCodec = c
		}
	}

	// Chaves/SSRCs de RECEPÇÃO por participante (rederiva as de quem já existe).
	for _, gp := range m.byDevice {
		m.setupReceiverLocked(gp)
	}
	n := len(m.byDevice)
	m.mu.Unlock()
	m.log.Info("group: epoch instalado", "tx", txid, "rotation", rotation, "participants", n)

	m.tryStartRelay()
	m.maybeStartMedia()
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
	gp.audioSSRC = ssrcs[0]             // slot 0 = áudio
	rekey := gp.keysTxID != m.epochTxID // epoch rotacionou: rederiva as chaves existentes
	if km, err := media.DeriveGroupSrtpKeying(m.epochKey, gp.participantID); err == nil {
		switch {
		case gp.srtp == nil:
			if ctx, err := media.NewSrtpContext(km, core.SRTPAuthTagLen); err == nil {
				gp.srtp = ctx
			}
		case rekey:
			if ctx, err := gp.srtp.WithKeying(km); err == nil {
				gp.srtp = ctx
			}
		}
		if rekey && gp.videoPipe != nil {
			_ = gp.videoPipe.RekeyGroup(km, km)
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
	// RTCP do participante vem assinado com as chaves SRTCP dele e com o SSRC de
	// qualquer um dos streams dele como emissor: mapeia os 9.
	if gp.srtcpKeys == nil || rekey {
		if k, err := media.DeriveGroupSrtcpKeys(m.epochKey, gp.participantID); err == nil {
			gp.srtcpKeys = &k
		}
	}
	for _, s := range ssrcs {
		if s != 0 {
			m.bySrtcpSSRC[s] = gp
		}
	}
	gp.keysTxID = m.epochTxID

	// Vídeo: deriva o SSRC de vídeo (slot 2) e um pipeline de RECEPÇÃO H264 por
	// participante (só decodifica; o relay é nil-safe pois nunca chamamos Broadcast).
	if m.video {
		if vs, err := media.DeriveParticipantSSRC(m.callID, gp.participantID, media.GroupVideoSlotWord); err == nil {
			gp.videoSSRC = vs
		}
		if gp.videoPipe == nil {
			if km, err := media.DeriveGroupSrtpKeying(m.epochKey, gp.participantID); err == nil {
				pipe := callvideo.New(m.log, groupVideoRelay{})
				// Pipe só de RECEPÇÃO: usa km nos dois lados (o contexto de ENVIO nunca é
				// chamado). NÃO passar keying vazio — deriveSrtpKey estoura com salt vazio.
				if err := pipe.SetupGroup(0, km, km); err == nil {
					pid := gp.participantID
					pipe.OnFrame = func(au []byte) {
						idr := annexBHasIDR(au)
						m.mu.Lock()
						cb := m.OnPeerVideo
						first := !gp.gotIDR && idr
						if idr {
							gp.gotIDR = true
						}
						m.mu.Unlock()
						if first {
							m.log.Info("group vídeo: keyframe do participante recebido", "pid", pid, "bytes", len(au))
						}
						if cb != nil {
							cb(pid, au)
						}
					}
					pipe.OnOrientation = func(o int) {
						m.mu.Lock()
						cb := m.OnPeerVideoOrientation
						m.mu.Unlock()
						if cb != nil {
							cb(pid, o)
						}
					}
					gp.videoPipe = pipe
				}
			}
		}
		if gp.videoSSRC != 0 {
			m.byVideoSSRC[gp.videoSSRC] = gp
		}
	}
}

// tryStartRelay conecta o relay de grupo UMA vez, quando já há epoch (chaves) e o
// relay do group_update. Usa o Allocate de GRUPO (assinaturas de grupo + HBH-FEC),
// montado numa closure que lê os PIDs conectados correntes a cada (re)envio.
func (m *GroupCallManager) tryStartRelay() {
	m.mu.Lock()
	if m.relayStarted || len(m.epochKey) != 32 || m.groupRelay == nil {
		m.mu.Unlock()
		return
	}
	relay := m.groupRelay
	callID := m.callID
	selfID := m.selfID
	// escolhe o primeiro endpoint utilizável
	var ep *signaling.GroupCallRelayEndpoint
	for i := range relay.Endpoints {
		if relay.Endpoints[i].IPv4 != "" && relay.Endpoints[i].Port != 0 {
			ep = &relay.Endpoints[i]
			break
		}
	}
	if ep == nil {
		m.mu.Unlock()
		m.log.Warn("group: relay sem endpoint utilizável")
		return
	}
	var rawToken []byte
	if int(ep.TokenID) < len(relay.Tokens) {
		rawToken = relay.Tokens[ep.TokenID]
	}
	groupKey := append([]byte(nil), relay.Key...)
	ip, port, relayName := ep.IPv4, int(ep.Port), ep.RelayName
	m.relayStarted = true
	m.groupKey = groupKey
	m.mu.Unlock()

	// Conecta o canal de mídia (DTLS direto) ao endpoint do relay de grupo.
	m.log.Info("group relay: conectando (dtls direto)", "relay", relayName, "ip", ip, "port", port)
	ch, err := transport.ConnectGroupRelay(ip, port)
	if err != nil {
		m.log.Error("group relay: falha ao conectar", "err", err, "ip", ip, "port", port)
		m.mu.Lock()
		m.relayStarted = false
		m.mu.Unlock()
		return
	}
	m.mu.Lock()
	m.groupChan = ch
	m.recvStop = make(chan struct{})
	stop := m.recvStop
	// Vídeo: pipeline de ENVIO da câmera (nosso stream de vídeo, slot 2), já com o
	// canal DTLS aberto p/ o Broadcast.
	if m.video && m.sendVideoPipe == nil && len(m.epochKey) == 32 {
		if vs, err := media.DeriveParticipantSSRC(m.callID, m.selfID, media.GroupVideoSlotWord); err == nil {
			if km, err := media.DeriveGroupSrtpKeying(m.epochKey, m.selfID); err == nil {
				pipe := callvideo.New(m.log, groupVideoRelay{ch: ch})
				// Pipe só de ENVIO: usa km nos dois lados (o contexto de RECEPÇÃO nunca é
				// chamado). NÃO passar keying vazio — deriveSrtpKey estoura com salt vazio.
				if err := pipe.SetupGroup(vs, km, km); err == nil {
					m.sendVideoPipe = pipe
					m.selfVideoSSRC = vs
				}
			}
		}
	}
	video := m.video
	m.mu.Unlock()
	m.log.Info("group relay: canal DTLS aberto", "relay", relayName)
	if video {
		// Anuncia nossa câmera ligada (como o WhatsApp Web) assim que a mídia pode fluir.
		go m.announceVideoState(context.Background(), true)
	}

	// allocate builder (lê PIDs conectados correntes a cada envio/keepalive).
	allocate := func() []byte {
		xor, ok := transport.EncodeXorRelayEndpointBytes(ip, uint16(port))
		if !ok {
			return nil
		}
		var txid [12]byte
		_, _ = rand.Read(txid[:])
		m.mu.Lock()
		pids := append([]uint32(nil), m.connectedPIDs...)
		m.mu.Unlock()
		streamSsrcs, _ := media.DeriveRelayStreamSSRCs(callID, selfID)
		appData, _ := media.DeriveParticipantSSRC(callID, selfID, media.GroupAppDataSlotWord)
		hbhTx, _ := media.DeriveParticipantSSRC(callID, selfID, media.GroupHBHFECTXSlot)
		hbhRx, _ := media.DeriveParticipantSSRC(callID, selfID, media.GroupHBHFECRXSlot)
		return transport.BuildGroupAllocate(txid, rawToken, xor, streamSsrcs, appData, [2]uint32{hbhTx, hbhRx}, pids, groupKey)
	}

	go m.groupRecvLoop(ch, stop)
	go m.groupAllocateKeepalive(ch, stop, allocate)
	m.maybeStartMedia()
}

// groupAllocateKeepalive envia o Allocate de grupo logo e a cada 1s (mantém a
// assinatura no relay e atualiza os PIDs conforme os participantes conectam).
func (m *GroupCallManager) groupAllocateKeepalive(ch *transport.GroupRelayChannel, stop chan struct{}, allocate func() []byte) {
	defer m.recoverGroup("groupAllocateKeepalive")
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()
	sendAlloc := func() bool {
		pkt := allocate()
		if len(pkt) == 0 {
			return true
		}
		_, err := ch.Send(pkt)
		if mediaDebugEnabled {
			m.mu.Lock()
			np := len(m.connectedPIDs)
			m.mu.Unlock()
			m.log.Info("group allocate enviado", "bytes", len(pkt), "pids", np, "err", err)
		}
		return err == nil
	}
	sendAlloc()
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			if !sendAlloc() {
				return
			}
		}
	}
}

// recoverGroup evita que um panic no código EXPERIMENTAL de grupo derrube o processo
// (o gateway atende clientes reais). Chamar como `defer m.recoverGroup("<onde>")`.
func (m *GroupCallManager) recoverGroup(where string) {
	if r := recover(); r != nil {
		m.log.Error("group: panic recuperado", "where", where, "panic", r)
	}
}

// groupRecvLoop lê o canal do relay: responde binding requests e demultiplexa RTP.
func (m *GroupCallManager) groupRecvLoop(ch *transport.GroupRelayChannel, stop chan struct{}) {
	defer m.recoverGroup("groupRecvLoop")
	buf := make([]byte, 2048)
	for {
		select {
		case <-stop:
			return
		default:
		}
		n, err := ch.Recv(buf)
		if err != nil {
			return
		}
		if n <= 0 {
			continue
		}
		pkt := append([]byte(nil), buf[:n]...)
		// Em modo SFU (2+ remotos) o relay prefixa um cabeçalho de group-forwarding na
		// mídia reencaminhada dos outros participantes: desembrulha antes de classificar.
		inner, wrapped, valid := transport.UnwrapGroupForwardingPacket(pkt)
		if !valid {
			continue
		}
		if wrapped {
			pkt = inner
			if w := atomic.AddUint64(&m.wrappedN, 1); mediaDebugEnabled && (w == 1 || w%500 == 0) {
				m.log.Info("group relay: pacote group-forwarding desembrulhado", "pkts", w, "bytes", len(pkt))
			}
		}
		kind := transport.ClassifyGroupRelayPacket(pkt)
		if mediaDebugEnabled {
			n := atomic.AddUint64(&m.recvN, 1)
			if n <= 8 || n%200 == 0 {
				var mt uint16
				if len(pkt) >= 2 {
					mt = uint16(pkt[0])<<8 | uint16(pkt[1])
				}
				m.log.Info("group relay recv", "pkts", n, "kind", int(kind), "bytes", len(pkt), "stun_type", mt)
			}
		}
		switch kind {
		case transport.GroupRelayStun:
			resp, ok := transport.BuildGroupBindingSuccess(pkt, m.groupKey)
			if ok {
				_, _ = ch.Send(resp)
			}
			if mediaDebugEnabled {
				m.log.Info("group relay STUN", "binding_req", ok, "bytes", len(pkt))
			}
		case transport.GroupRelayRtp:
			m.onGroupRtp(pkt)
		case transport.GroupRelayRtcp:
			m.onGroupRtcp(pkt)
		}
	}
}

// onGroupRtcp autentica o RTCP de um participante (chaves SRTCP dele) e, se for um
// PLI/FIR pro NOSSO vídeo, pede um keyframe novo à câmera do navegador.
func (m *GroupCallManager) onGroupRtcp(pkt []byte) {
	senderSsrc, ok := media.ParseRtcpSenderSsrc(pkt)
	if !ok {
		return
	}
	m.mu.Lock()
	gp := m.bySrtcpSSRC[senderSsrc]
	var keys *media.SrtcpKeys
	if gp != nil {
		keys = gp.srtcpKeys
	}
	selfVideo := m.selfVideoSSRC
	onReq := m.OnKeyframeRequest
	m.mu.Unlock()
	if keys == nil {
		if mediaDebugEnabled {
			m.log.Info("group RTCP de SSRC desconhecido", "ssrc", senderSsrc, "bytes", len(pkt))
		}
		return
	}
	plain, _, ok := media.UnprotectSrtcp(keys, senderSsrc, pkt)
	if !ok {
		if mediaDebugEnabled {
			m.log.Info("group RTCP falhou autenticação", "ssrc", senderSsrc, "pid", gp.participantID)
		}
		return
	}
	if n := atomic.AddUint64(&m.rtcpN, 1); mediaDebugEnabled && (n == 1 || n%100 == 0) {
		m.log.Info("group RTCP autenticado", "pkts", n, "pid", gp.participantID, "pt", plain[1])
	}
	if selfVideo != 0 && media.RtcpRequestsKeyframe(plain, selfVideo) {
		m.log.Info("group: participante pediu keyframe do nosso vídeo (PLI/FIR)", "pid", gp.participantID)
		if onReq != nil {
			onReq()
		}
	}
}

// sendPLI pede um keyframe de vídeo ao participante (PLI assinado com nosso SRTCP de
// vídeo). Rate-limited por participante (300ms). Idempotente sem vídeo/relay.
func (m *GroupCallManager) sendPLI(gp *groupParticipant, force bool) {
	m.mu.Lock()
	s, ch, vs := m.srtcpVideo, m.groupChan, gp.videoSSRC
	if s == nil || ch == nil || vs == 0 || (!force && time.Since(gp.lastPLI) < 300*time.Millisecond) {
		m.mu.Unlock()
		return
	}
	gp.lastPLI = time.Now()
	m.mu.Unlock()
	pkt, err := s.PictureLossIndication(vs)
	if err != nil {
		return
	}
	_, _ = ch.Send(pkt)
	if n := atomic.AddUint64(&m.pliN, 1); mediaDebugEnabled || n <= 3 {
		m.log.Info("group: PLI enviado (pedido de keyframe)", "pid", gp.participantID, "video_ssrc", vs, "total", n)
	}
}

// RequestParticipantKeyframe pede keyframe ao participante pid ("" = todos). Usado
// quando o navegador precisa (decoder novo/erro) — quem consome é quem pede.
func (m *GroupCallManager) RequestParticipantKeyframe(pid string) {
	m.mu.Lock()
	var targets []*groupParticipant
	for id, gp := range m.byDevice {
		if pid == "" || id == pid || gp.deviceJID.User == pid {
			targets = append(targets, gp)
		}
	}
	m.mu.Unlock()
	for _, gp := range targets {
		m.sendPLI(gp, false)
	}
}

// RequestAllKeyframes marca que o vídeo de TODOS precisa recomeçar num keyframe (ex.:
// o navegador do atendente (re)conectou) e pede PLI a quem já está mandando vídeo.
func (m *GroupCallManager) RequestAllKeyframes() {
	m.mu.Lock()
	var targets []*groupParticipant
	for _, gp := range m.byDevice {
		gp.gotIDR = false
		if gp.videoPkts > 0 {
			targets = append(targets, gp)
		}
	}
	m.mu.Unlock()
	for _, gp := range targets {
		m.sendPLI(gp, true)
	}
}

// rtcpLoop manda os SR+SDES periódicos (1,5s) dos nossos streams de áudio e vídeo —
// o WhatsApp associa os streams RTP a essa sessão SRTCP; sem os reports o vídeo do
// criador pode nunca "engatar" no receptor.
func (m *GroupCallManager) rtcpLoop(stop chan struct{}) {
	defer m.recoverGroup("rtcpLoop")
	ticker := time.NewTicker(1500 * time.Millisecond)
	defer ticker.Stop()
	var sent uint64
	for {
		select {
		case <-stop:
			return
		case now := <-ticker.C:
			m.mu.Lock()
			ch := m.groupChan
			audio, video := m.srtcpAudio, m.srtcpVideo
			astats := m.audioStats
			vpipe := m.sendVideoPipe
			m.mu.Unlock()
			if ch == nil {
				continue
			}
			nowMs := uint64(now.UnixMilli())
			if audio != nil && astats.PacketsSent > 0 {
				if pkt, err := audio.SenderReport(astats, nowMs); err == nil {
					_, _ = ch.Send(pkt)
				}
			}
			if video != nil && vpipe != nil {
				if vstats := vpipe.SenderStats(); vstats.PacketsSent > 0 {
					if pkt, err := video.SenderReport(vstats, nowMs); err == nil {
						_, _ = ch.Send(pkt)
					}
				}
			}
			if sent++; sent == 1 {
				m.log.Info("group: SRTCP sender reports periódicos iniciados")
			}
		}
	}
}

// annexBHasIDR diz se um access unit Annex-B contém um NAL IDR (tipo 5).
func annexBHasIDR(au []byte) bool {
	for _, n := range transport.SplitAnnexB(au) {
		if len(n) > 0 && n[0]&0x1f == 5 {
			return true
		}
	}
	return false
}

// onGroupRtp decodifica um pacote RTP de um participante: áudio (Opus) → mixer,
// vídeo (H264) → pipeline de vídeo do participante → navegador.
func (m *GroupCallManager) onGroupRtp(data []byte) {
	if len(data) < 12 {
		return
	}
	pt := data[1] & 0x7f
	if pt == core.PayloadTypeWhatsAppH264 {
		vssrc := media.RTPSsrc(data)
		m.mu.Lock()
		gp := m.byVideoSSRC[vssrc]
		var first, needPLI bool
		if gp != nil {
			gp.videoPkts++
			first = gp.videoPkts == 1
			// Até entregarmos um keyframe dele, pede PLI a cada 1s: os celulares só
			// mandam IDR sob pedido, e sem IDR o decoder do painel não abre o vídeo.
			if !gp.gotIDR && time.Since(gp.lastPLI) >= time.Second {
				needPLI = true
			}
		}
		m.mu.Unlock()
		if gp == nil {
			if u := atomic.AddUint64(&m.videoUnknownN, 1); u == 1 || (mediaDebugEnabled && u%500 == 0) {
				m.log.Warn("group vídeo de SSRC desconhecido (participante sem receiver?)", "ssrc", vssrc, "pkts", u)
			}
			return
		}
		if first {
			m.log.Info("group vídeo: 1º pacote do participante", "pid", gp.participantID, "video_ssrc", vssrc)
		}
		if needPLI {
			m.sendPLI(gp, true)
		}
		if gp.videoPipe != nil {
			gp.videoPipe.HandleRelayData(data)
		}
		return
	}
	if pt != core.PayloadTypeWhatsAppOpus {
		return
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
	if mediaDebugEnabled {
		if d := atomic.AddUint64(&m.audioN, 1); d == 1 || d%200 == 0 {
			m.log.Info("group áudio decodificado do participante", "pkts", d, "pid", gp.participantID, "samples", len(pcm))
		}
	}
	m.mixer.Add(gp.participantID, pcm)
}

// maybeStartMedia liga os loops de mídia quando já há epoch e relay conectado.
func (m *GroupCallManager) maybeStartMedia() {
	m.mu.Lock()
	if m.started || len(m.epochKey) != 32 || m.groupChan == nil {
		m.mu.Unlock()
		return
	}
	m.started = true
	m.stop = make(chan struct{})
	stop := m.stop
	m.mu.Unlock()

	go m.mixLoop(stop)
	go m.sendLoop(stop)
	go m.rtcpLoop(stop)
	m.log.Info("group media started", "call_id", m.CallID())
}

// mixLoop puxa chunks mixados (10ms) e os reenquadra em frames de 960 p/ o sink.
func (m *GroupCallManager) mixLoop(stop chan struct{}) {
	defer m.recoverGroup("mixLoop")
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
			if frame, full := m.framer.Push(chunk); full {
				m.mu.Lock()
				cb := m.OnPeerAudio
				m.mu.Unlock()
				if cb != nil {
					cb(frame)
				}
			}
		}
	}
}

// sendLoop codifica o áudio capturado (frames de 960) e envia pelo relay.
func (m *GroupCallManager) sendLoop(stop chan struct{}) {
	defer m.recoverGroup("sendLoop")
	ticker := time.NewTicker(60 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			m.mu.Lock()
			if m.sendCodec == nil || m.sendSrtp == nil || m.rtpSession == nil || m.groupChan == nil {
				m.mu.Unlock()
				continue
			}
			var frame []float32
			if len(m.captureBuf) >= 960 {
				frame = m.captureBuf[:960]
				m.captureBuf = m.captureBuf[960:]
			} else {
				// Sem áudio de entrada: envia SILÊNCIO. O criador precisa emitir mídia
				// contínua pro fluxo do grupo engatar (senão o outro lado fica "conectando").
				frame = make([]float32, 960)
			}
			codec, srtp, rtp := m.sendCodec, m.sendSrtp, m.rtpSession
			ch := m.groupChan
			m.mu.Unlock()

			if ch == nil {
				continue
			}
			opus, err := codec.Encode(frame)
			if err != nil || len(opus) == 0 {
				continue
			}
			pkt := rtp.CreatePacketWithDuration(opus, 960, false)
			protected, err := srtp.Protect(pkt)
			if err != nil {
				continue
			}
			_, _ = ch.Send(protected)
			m.mu.Lock()
			m.audioStats.PacketsSent++
			m.audioStats.OctetsSent += uint32(len(opus))
			m.audioStats.RtpTimestamp = pkt.Header.Timestamp
			m.mu.Unlock()
			if mediaDebugEnabled {
				if s := atomic.AddUint64(&m.sentN, 1); s == 1 || s%200 == 0 {
					m.log.Info("group RTP enviado", "pkts", s, "bytes", len(protected))
				}
			}
		}
	}
}

// SetAudioSink liga/desliga (fn=nil) o destino do áudio mixado (navegador do operador).
func (m *GroupCallManager) SetAudioSink(fn func([]float32)) {
	m.mu.Lock()
	m.OnPeerAudio = fn
	m.mu.Unlock()
}

// SetVideoSink liga/desliga (fn=nil) o destino do vídeo dos participantes (navegador).
// Ao ligar, o vídeo de todos precisa recomeçar num keyframe (decoders novos no
// navegador): pede PLI a quem já manda vídeo.
func (m *GroupCallManager) SetVideoSink(fn func(participantID string, au []byte)) {
	m.mu.Lock()
	m.OnPeerVideo = fn
	m.mu.Unlock()
	if fn != nil {
		m.RequestAllKeyframes()
	}
}

// SetKeyframeRequestSink liga/desliga o aviso "pediram keyframe do nosso vídeo".
func (m *GroupCallManager) SetKeyframeRequestSink(fn func()) {
	m.mu.Lock()
	m.OnKeyframeRequest = fn
	m.mu.Unlock()
}

// SetPeerVideoOrientationSink liga/desliga o aviso de orientação da câmera de participante.
func (m *GroupCallManager) SetPeerVideoOrientationSink(fn func(participantID string, orientation int)) {
	m.mu.Lock()
	m.OnPeerVideoOrientation = fn
	m.mu.Unlock()
}

// SetParticipantLeftSink liga/desliga o aviso de participante que saiu da chamada.
func (m *GroupCallManager) SetParticipantLeftSink(fn func(participantID string)) {
	m.mu.Lock()
	m.OnParticipantLeft = fn
	m.mu.Unlock()
}

// groupStateDeparted diz se o estado de um usuário no group_update é TERMINAL
// (saiu/recusou/falhou) — estados desconhecidos NÃO contam como saída.
func groupStateDeparted(state string) bool {
	switch state {
	case "left", "rejected", "declined", "failed", "timeout", "busy", "ended", "canceled", "cancelled":
		return true
	}
	return false
}

// SetPeerVideoStateSink liga/desliga o aviso de câmera ligada/desligada de participante.
func (m *GroupCallManager) SetPeerVideoStateSink(fn func(participantID string, state int)) {
	m.mu.Lock()
	m.OnPeerVideoState = fn
	m.mu.Unlock()
}

// FeedCapturedVideo recebe um access unit H264 da câmera do navegador p/ enviar na call.
func (m *GroupCallManager) FeedCapturedVideo(au []byte) {
	m.mu.Lock()
	pipe := m.sendVideoPipe
	m.mu.Unlock()
	if pipe != nil && len(au) > 0 {
		pipe.FeedCaptured(au)
	}
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
	if m.recvStop != nil {
		close(m.recvStop)
		m.recvStop = nil
	}
	ch := m.groupChan
	m.groupChan = nil
	callID := m.callID
	m.callID = ""
	m.started = false
	m.relayStarted = false
	for _, gp := range m.byDevice {
		if gp.codec != nil {
			gp.codec.Close()
		}
	}
	if m.sendCodec != nil {
		m.sendCodec.Close()
		m.sendCodec = nil
	}
	if m.sendVideoPipe != nil {
		m.sendVideoPipe.Reset()
		m.sendVideoPipe = nil
	}
	for _, gp := range m.byDevice {
		if gp.videoPipe != nil {
			gp.videoPipe.Reset()
		}
	}
	m.byDevice = make(map[string]*groupParticipant)
	m.epochSentTo = make(map[string]bool)
	m.bySSRC = make(map[uint32]*groupParticipant)
	m.byVideoSSRC = make(map[uint32]*groupParticipant)
	m.bySrtcpSSRC = make(map[uint32]*groupParticipant)
	m.srtcpAudio, m.srtcpVideo = nil, nil
	m.audioStats = media.RtcpSenderStats{}
	m.selfVideoSSRC = 0
	m.videoAnnounced = false
	m.epochKey = nil
	m.epochTxID = 0 // senão o enc_rekey da PRÓXIMA chamada com o mesmo tx é "conflitante"
	m.groupKey = nil
	m.groupRelay = nil
	m.rosterTxID = 0
	m.connectedPIDs = nil
	m.selfSsrcs = [9]uint32{}
	m.captureBuf = nil
	m.sendSrtp, m.rtpSession = nil, nil
	m.mu.Unlock()

	if ch != nil {
		_ = ch.Close()
	}
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
