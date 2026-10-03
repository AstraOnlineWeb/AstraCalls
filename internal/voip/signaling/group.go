package signaling

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"net"
	"strconv"
	"time"

	waBinary "go.mau.fi/whatsmeow/binary"
	"go.mau.fi/whatsmeow/types"
)

// Sinalização de chamada em GRUPO, portada de github.com/purpshell/meowcaller (MIT,
// Rajeh Taher) signaling/group.go, adaptada do fork hypermeow para go.mau.fi/whatsmeow.
// O formato de fio (tags/atributos) é preservado byte-a-byte — tem que casar com o
// servidor do WhatsApp. Helpers locais usam prefixo "g" para não colidir com o 1:1.

// OfferDeviceKey é uma entrada de callKey cifrada por-device (usada no enc_rekey).
type OfferDeviceKey struct {
	DeviceJid  types.JID
	Ciphertext []byte
	EncType    string // "pkmsg" ou "msg"
}

// CallControlEnvelope é o roteamento + a ação única de um nó <call> cru.
type CallControlEnvelope struct {
	From        types.JID
	Participant types.JID
	Recipient   types.JID
	Timestamp   time.Time
	CallID      string
	CallCreator types.JID
	Action      waBinary.Node
}

// ParseCallControlEnvelope extrai a ação de um <call> sem depender de eventos do fork.
func ParseCallControlEnvelope(node *waBinary.Node) (*CallControlEnvelope, error) {
	if node == nil || node.Tag != "call" {
		return nil, fmt.Errorf("signaling: parse call control envelope: unexpected node")
	}
	children := node.GetChildren()
	if len(children) != 1 {
		return nil, fmt.Errorf("signaling: parse call control envelope: got %d actions, want 1", len(children))
	}
	attrs := node.AttrGetter()
	childAttrs := children[0].AttrGetter()
	envelope := &CallControlEnvelope{
		From:        attrs.JID("from"),
		Participant: attrs.OptionalJIDOrEmpty("participant"),
		Recipient:   attrs.OptionalJIDOrEmpty("recipient"),
		Timestamp:   attrs.OptionalUnixTime("t"),
		CallID:      childAttrs.String("call-id"),
		CallCreator: childAttrs.JID("call-creator"),
		Action:      children[0],
	}
	if err := attrs.Error(); err != nil {
		return nil, fmt.Errorf("signaling: parse call control routing: %w", err)
	}
	if err := childAttrs.Error(); err != nil {
		return nil, fmt.Errorf("signaling: parse call control identity: %w", err)
	}
	return envelope, nil
}

// GroupCallUpdate é um snapshot autoritativo do roster + relay da chamada em grupo.
type GroupCallUpdate struct {
	CallID         string
	CallCreator    types.JID
	GroupJID       types.JID
	TransactionID  uint32
	Media          string
	ConnectedLimit uint32
	Joinable       bool
	AVUpgradable   bool
	RekeyRequested bool
	Participants   []GroupCallParticipant
	Relay          *GroupCallRelay
}

// GroupCallParticipant é um usuário no roster da chamada em grupo.
type GroupCallParticipant struct {
	JID     types.JID
	PN      types.JID
	State   string
	Type    string
	Devices []GroupCallDevice
}

// GroupCallDevice é um dispositivo de participante no roster.
type GroupCallDevice struct {
	JID               types.JID
	Platform          string
	PID               uint32
	HasPID            bool
	CapabilityVersion uint32
	Capability        []byte
}

// GroupCallRelay descreve o relay compartilhado alocado para a chamada em grupo.
type GroupCallRelay struct {
	TransactionID      uint32
	SelfPID            uint32
	HasSelfPID         bool
	UUID               string
	ParticipantUUID    string
	AttributePadding   bool
	WarpMITagLength    uint32
	HasWarpMITagLength bool
	Key                []byte
	HBHKey             []byte
	Tokens             [][]byte
	AuthTokens         [][]byte
	Endpoints          []GroupCallRelayEndpoint
}

// GroupCallRelayEndpoint é um endereço na alocação do relay de grupo.
type GroupCallRelayEndpoint struct {
	RelayID     uint32
	TokenID     uint32
	AuthTokenID uint32
	RelayName   string
	DomainName  string
	RTT         uint32
	IsFNA       bool
	Address     []byte
	IPv4        string
	Port        uint16
}

// GroupCallEncRekey é um epoch de chave compartilhada cifrado entregue a este device.
type GroupCallEncRekey struct {
	CallID            string
	CallCreator       types.JID
	TransactionID     uint32
	KeyGeneration     uint32
	EncryptionType    string
	EncryptionVersion uint32
	Ciphertext        []byte
}

// InitialGroupOfferParams são as entradas de fio para iniciar uma chamada em grupo.
type InitialGroupOfferParams struct {
	CallID       string
	CallCreator  types.JID
	GroupJID     types.JID
	Participants []GroupCallParticipant
	Video        bool
	// ExtraNodes são anexados ao <offer> depois do <group_info> — usado para carregar
	// a distribuição de chave estilo 1:1 (<capability>, <destination> com enc por
	// device, <encopt keygen="2">, device-identity) que faz os devices TOCAREM.
	ExtraNodes []waBinary.Node
}

// BuildActiveGroupPreaccept constrói a resposta eager a um convite de call ativa.
func BuildActiveGroupPreaccept(callID string, callCreator types.JID, requestID string, video bool) (waBinary.Node, error) {
	if callID == "" || callCreator.IsEmpty() || requestID == "" {
		return waBinary.Node{}, fmt.Errorf("signaling: build active group preaccept: incomplete identity")
	}
	return gBuildPreacceptNode(callID, types.NewJID(callID, "call"), callCreator, requestID, []string{"16000"}, video), nil
}

// BuildActiveGroupAccept constrói a aceitação imediata de um convite de call ativa.
func BuildActiveGroupAccept(callID string, callCreator types.JID, requestID string) (waBinary.Node, error) {
	if callID == "" || callCreator.IsEmpty() || requestID == "" {
		return waBinary.Node{}, fmt.Errorf("signaling: build active group accept: incomplete identity")
	}
	accept := gBuildAcceptNode(callID, types.NewJID(callID, "call"), callCreator, []string{"16000"})
	accept.Attrs["id"] = requestID
	return accept, nil
}

// BuildInitialGroupOffer constrói o offer inicial de grupo (ad-hoc ou ligado a @g.us).
func BuildInitialGroupOffer(params InitialGroupOfferParams) (waBinary.Node, error) {
	if params.CallID == "" {
		return waBinary.Node{}, fmt.Errorf("signaling: build initial group offer: call ID is required")
	}
	if params.CallCreator.IsEmpty() {
		return waBinary.Node{}, fmt.Errorf("signaling: build initial group offer: call creator is required")
	}
	if len(params.Participants) < 2 {
		return waBinary.Node{}, fmt.Errorf("signaling: build initial group offer: at least two remote participants are required")
	}
	users, err := buildGroupUsers(params.Participants, params.CallCreator, params.Video)
	if err != nil {
		return waBinary.Node{}, err
	}
	children := []waBinary.Node{gAudioOpus("8000"), gAudioOpus("16000")}
	if params.Video {
		children = append(children, gVideoOfferNode())
	}
	children = append(children,
		waBinary.Node{Tag: "net", Attrs: waBinary.Attrs{"medium": "3"}},
		waBinary.Node{Tag: "group_info", Content: users},
	)
	children = append(children, params.ExtraNodes...)
	offer := gOfferAction("offer", params.CallID, params.CallCreator, children)
	if !params.GroupJID.IsEmpty() {
		offer.Attrs["group-jid"] = params.GroupJID
	}
	return gCallWrap(types.NewJID(params.CallID, "call"), nil, offer), nil
}

// GroupInviteOfferParams são as entradas para convidar/tocar um participante.
type GroupInviteOfferParams struct {
	CallID        string
	To            types.JID
	CallCreator   types.JID
	TargetDevices []types.JID
	Participants  []GroupCallParticipant
	Video         bool
}

// BuildGroupInviteOffer constrói um offer singular de participante em call ativa.
func BuildGroupInviteOffer(params GroupInviteOfferParams) (waBinary.Node, error) {
	if params.CallID == "" || params.To.IsEmpty() || params.CallCreator.IsEmpty() {
		return waBinary.Node{}, fmt.Errorf("signaling: build group invite offer: call identity and target are required")
	}
	if len(params.TargetDevices) == 0 || len(params.Participants) == 0 {
		return waBinary.Node{}, fmt.Errorf("signaling: build group invite offer: target devices and roster are required")
	}
	users, err := buildGroupUsers(params.Participants, types.EmptyJID, false)
	if err != nil {
		return waBinary.Node{}, err
	}
	children := []waBinary.Node{gAudioOpus("16000")}
	if params.Video {
		children = append(children, gVideoOfferNode())
	}
	children = append(children,
		waBinary.Node{Tag: "net", Attrs: waBinary.Attrs{"medium": "2"}},
		gDestinationTo(params.TargetDevices),
		waBinary.Node{Tag: "group_info", Content: users},
	)
	return gCallWrap(params.To, nil, gOfferAction("offer", params.CallID, params.CallCreator, children)), nil
}

func buildGroupUsers(participants []GroupCallParticipant, creator types.JID, video bool) ([]waBinary.Node, error) {
	users := make([]waBinary.Node, len(participants))
	for participantIndex, participant := range participants {
		if participant.JID.IsEmpty() || len(participant.Devices) == 0 {
			return nil, fmt.Errorf("signaling: build group users: participant %d is incomplete", participantIndex)
		}
		userAttrs := waBinary.Attrs{"jid": participant.JID}
		if participant.State != "" {
			userAttrs["state"] = participant.State
		}
		devices := make([]waBinary.Node, len(participant.Devices))
		for deviceIndex, device := range participant.Devices {
			if device.JID.IsEmpty() {
				return nil, fmt.Errorf("signaling: build group users: participant %d device %d JID is required", participantIndex, deviceIndex)
			}
			var content []waBinary.Node
			if device.Capability != nil {
				value := bytes.Clone(device.Capability)
				if video && device.JID == creator && bytes.Equal(value, capabilityOffer) {
					value = bytes.Clone(capabilityVideoOffer)
				}
				attrs := make(waBinary.Attrs)
				if device.CapabilityVersion != 0 {
					attrs["ver"] = strconv.FormatUint(uint64(device.CapabilityVersion), 10)
				}
				content = []waBinary.Node{{Tag: "capability", Attrs: attrs, Content: value}}
			}
			devices[deviceIndex] = waBinary.Node{Tag: "device", Attrs: waBinary.Attrs{"jid": device.JID}, Content: content}
		}
		users[participantIndex] = waBinary.Node{Tag: "user", Attrs: userAttrs, Content: devices}
	}
	return users, nil
}

// ParseGroupInviteSnapshot parseia o snapshot de grupo embutido num offer de call ativa.
func ParseGroupInviteSnapshot(offer *waBinary.Node) (*GroupCallUpdate, bool, error) {
	if offer == nil || offer.Tag != "offer" {
		return nil, false, fmt.Errorf("signaling: parse group invite snapshot: unexpected offer")
	}
	groupInfo, ok := offer.GetOptionalChildByTag("group_info")
	if !ok {
		return nil, false, nil
	}
	attrs := offer.AttrGetter()
	update := &GroupCallUpdate{CallID: attrs.String("call-id"), CallCreator: attrs.JID("call-creator")}
	if err := attrs.Error(); err != nil {
		return nil, false, fmt.Errorf("signaling: parse group invite identity: %w", err)
	}
	groupAttrs := groupInfo.AttrGetter()
	if callID := groupAttrs.OptionalString("call-id"); callID != "" && callID != update.CallID {
		return nil, false, fmt.Errorf("signaling: group invite call ID mismatch")
	}
	if creator := groupAttrs.OptionalJIDOrEmpty("call-creator"); !creator.IsEmpty() && creator != update.CallCreator {
		return nil, false, fmt.Errorf("signaling: group invite creator mismatch")
	}
	if err := parseGroupInfo(&groupInfo, update); err != nil {
		return nil, false, err
	}
	update.Joinable = attrs.OptionalString("joinable") == "1"
	return update, true, nil
}

// ParseInitialGroupCallAck parseia o snapshot de grupo num offer ou ACK de link-join.
func ParseInitialGroupCallAck(node *waBinary.Node) (*GroupCallUpdate, bool, error) {
	if node == nil || node.Tag != "ack" {
		return nil, false, fmt.Errorf("signaling: parse initial group ACK: unexpected envelope")
	}
	groupInfo, ok := node.GetOptionalChildByTag("group_info")
	if !ok {
		return nil, false, nil
	}
	attrs := groupInfo.AttrGetter()
	update := &GroupCallUpdate{CallID: attrs.String("call-id"), CallCreator: attrs.JID("call-creator")}
	if err := attrs.Error(); err != nil {
		return nil, true, fmt.Errorf("signaling: parse initial group identity: %w", err)
	}
	if err := parseGroupInfo(&groupInfo, update); err != nil {
		return nil, true, err
	}
	if relay, ok := node.GetOptionalChildByTag("relay"); ok {
		parsed, err := parseGroupRelay(&relay)
		if err != nil {
			return nil, true, err
		}
		update.Relay = parsed
	}
	return update, true, nil
}

// ParseGroupUpdate parseia uma ação group_update.
func ParseGroupUpdate(node *waBinary.Node) (*GroupCallUpdate, error) {
	if node == nil || node.Tag != "group_update" {
		return nil, fmt.Errorf("signaling: parse group update: unexpected node")
	}
	attrs := node.AttrGetter()
	update := &GroupCallUpdate{CallID: attrs.String("call-id"), CallCreator: attrs.JID("call-creator")}
	if err := attrs.Error(); err != nil {
		return nil, fmt.Errorf("signaling: parse group update identity: %w", err)
	}
	groupInfo, ok := node.GetOptionalChildByTag("group_info")
	if !ok {
		return nil, fmt.Errorf("signaling: parse group update: missing group_info")
	}
	if err := parseGroupInfo(&groupInfo, update); err != nil {
		return nil, err
	}
	if avUpgrade, ok := node.GetOptionalChildByTag("av_upgrade"); ok {
		update.AVUpgradable = avUpgrade.AttrGetter().OptionalString("av-upgradable") == "1"
	}
	if relay, ok := node.GetOptionalChildByTag("relay"); ok {
		parsed, err := parseGroupRelay(&relay)
		if err != nil {
			return nil, err
		}
		update.Relay = parsed
	}
	return update, nil
}

func parseGroupInfo(node *waBinary.Node, update *GroupCallUpdate) error {
	attrs := node.AttrGetter()
	update.GroupJID = attrs.OptionalJIDOrEmpty("group-jid")
	update.Media = attrs.String("media")
	update.Joinable = attrs.OptionalString("joinable") == "1"
	update.RekeyRequested = attrs.OptionalString("rekey") == "1"
	var err error
	if update.TransactionID, err = requiredUint32Attr(attrs, "transaction-id"); err != nil {
		return fmt.Errorf("signaling: parse group transaction ID: %w", err)
	}
	if update.ConnectedLimit, err = requiredUint32Attr(attrs, "connected-limit"); err != nil {
		return fmt.Errorf("signaling: parse group connected limit: %w", err)
	}
	if err = attrs.Error(); err != nil {
		return fmt.Errorf("signaling: parse group_info attributes: %w", err)
	}
	for _, child := range node.GetChildren() {
		if child.Tag != "user" {
			continue
		}
		participant, parseErr := parseGroupParticipant(&child)
		if parseErr != nil {
			return parseErr
		}
		update.Participants = append(update.Participants, participant)
	}
	return nil
}

func parseGroupParticipant(node *waBinary.Node) (GroupCallParticipant, error) {
	attrs := node.AttrGetter()
	participant := GroupCallParticipant{
		JID: attrs.JID("jid"), PN: attrs.OptionalJIDOrEmpty("user_pn"),
		State: attrs.String("state"), Type: attrs.OptionalString("type"),
	}
	if err := attrs.Error(); err != nil {
		return participant, fmt.Errorf("signaling: parse group participant: %w", err)
	}
	for _, child := range node.GetChildren() {
		if child.Tag != "device" {
			continue
		}
		device, err := parseGroupDevice(&child)
		if err != nil {
			return participant, err
		}
		participant.Devices = append(participant.Devices, device)
	}
	return participant, nil
}

func parseGroupDevice(node *waBinary.Node) (GroupCallDevice, error) {
	attrs := node.AttrGetter()
	device := GroupCallDevice{JID: attrs.JID("jid"), Platform: attrs.OptionalString("platform")}
	var err error
	if device.PID, device.HasPID, err = optionalUint32Attr(attrs, "pid"); err != nil {
		return device, fmt.Errorf("signaling: parse group device PID: %w", err)
	}
	if err = attrs.Error(); err != nil {
		return device, fmt.Errorf("signaling: parse group device: %w", err)
	}
	if capability, ok := node.GetOptionalChildByTag("capability"); ok {
		capAttrs := capability.AttrGetter()
		device.CapabilityVersion, _, err = optionalUint32Attr(capAttrs, "ver")
		if err != nil {
			return device, fmt.Errorf("signaling: parse device capability: %w", err)
		}
		device.Capability = bytes.Clone(nodeBytes(&capability))
	}
	return device, nil
}

func parseGroupRelay(node *waBinary.Node) (*GroupCallRelay, error) {
	attrs := node.AttrGetter()
	relay := &GroupCallRelay{
		UUID: attrs.String("uuid"), ParticipantUUID: attrs.String("participant_uuid"),
		AttributePadding: attrs.OptionalString("attribute_padding") == "1",
		Tokens:           parseIndexedTokens(node, "token"), AuthTokens: parseIndexedTokens(node, "auth_token"),
	}
	var err error
	if relay.TransactionID, _, err = optionalUint32Attr(attrs, "transaction-id"); err != nil {
		return nil, err
	}
	if relay.SelfPID, relay.HasSelfPID, err = optionalUint32Attr(attrs, "self_pid"); err != nil {
		return nil, err
	}
	if relay.WarpMITagLength, relay.HasWarpMITagLength, err = optionalUint32Attr(attrs, "warp_mi_tag_len"); err != nil {
		return nil, err
	}
	if err = attrs.Error(); err != nil {
		return nil, fmt.Errorf("signaling: parse group relay: %w", err)
	}
	for _, child := range node.GetChildren() {
		switch child.Tag {
		case "key":
			relay.Key = bytes.Clone(nodeBytes(&child))
		case "hbh_key":
			relay.HBHKey = bytes.Clone(nodeBytes(&child))
		case "te2":
			endpoint, parseErr := parseGroupRelayEndpoint(&child)
			if parseErr != nil {
				return nil, parseErr
			}
			relay.Endpoints = append(relay.Endpoints, endpoint)
		}
	}
	return relay, nil
}

func parseGroupRelayEndpoint(node *waBinary.Node) (GroupCallRelayEndpoint, error) {
	attrs := node.AttrGetter()
	endpoint := GroupCallRelayEndpoint{
		RelayName: attrs.String("relay_name"), DomainName: attrs.OptionalString("domain_name"),
		IsFNA: attrs.OptionalString("is_fna") == "1", Address: bytes.Clone(nodeBytes(node)),
	}
	if len(endpoint.Address) == net.IPv4len+2 {
		endpoint.IPv4 = net.IP(endpoint.Address[:net.IPv4len]).String()
		endpoint.Port = binary.BigEndian.Uint16(endpoint.Address[net.IPv4len:])
	}
	var err error
	if endpoint.RelayID, _, err = optionalUint32Attr(attrs, "relay_id"); err != nil {
		return endpoint, err
	}
	if endpoint.TokenID, _, err = optionalUint32Attr(attrs, "token_id"); err != nil {
		return endpoint, err
	}
	if endpoint.AuthTokenID, _, err = optionalUint32Attr(attrs, "auth_token_id"); err != nil {
		return endpoint, err
	}
	if endpoint.RTT, _, err = optionalUint32Attr(attrs, "c2r_rtt"); err != nil {
		return endpoint, err
	}
	if err = attrs.Error(); err != nil {
		return endpoint, fmt.Errorf("signaling: parse group relay endpoint: %w", err)
	}
	return endpoint, nil
}

// GroupEncRekeyParams é uma entrega direta de um epoch de chave compartilhada.
type GroupEncRekeyParams struct {
	CallID        string
	To            types.JID
	CallCreator   types.JID
	TransactionID uint32
	RequestID     string
	DeviceKey     OfferDeviceKey
}

// BuildGroupEncRekey constrói um stanza de epoch de grupo (keygen-v2).
func BuildGroupEncRekey(params GroupEncRekeyParams) (waBinary.Node, error) {
	if params.CallID == "" || params.To.IsEmpty() || params.CallCreator.IsEmpty() ||
		params.TransactionID == 0 || params.RequestID == "" {
		return waBinary.Node{}, fmt.Errorf("signaling: build group rekey: incomplete identity")
	}
	if params.DeviceKey.DeviceJid != params.To || len(params.DeviceKey.Ciphertext) == 0 {
		return waBinary.Node{}, fmt.Errorf("signaling: build group rekey: encrypted device mismatch or empty ciphertext")
	}
	if params.DeviceKey.EncType != "msg" && params.DeviceKey.EncType != "pkmsg" {
		return waBinary.Node{}, fmt.Errorf("signaling: build group rekey: unsupported encryption type %q", params.DeviceKey.EncType)
	}
	action := waBinary.Node{
		Tag: "enc_rekey",
		Attrs: waBinary.Attrs{
			"call-id": params.CallID, "call-creator": params.CallCreator,
			"transaction-id": strconv.FormatUint(uint64(params.TransactionID), 10),
		},
		Content: []waBinary.Node{
			{Tag: "encopt", Attrs: waBinary.Attrs{"keygen": "2"}},
			{Tag: "enc", Attrs: waBinary.Attrs{"v": "2", "type": params.DeviceKey.EncType, "count": "0"}, Content: bytes.Clone(params.DeviceKey.Ciphertext)},
		},
	}
	return gCallWrap(params.To, &params.RequestID, action), nil
}

// ParseGroupCallEncRekey parseia uma ação enc_rekey.
func ParseGroupCallEncRekey(node *waBinary.Node) (*GroupCallEncRekey, error) {
	if node == nil || node.Tag != "enc_rekey" {
		return nil, fmt.Errorf("signaling: parse group rekey: unexpected node")
	}
	attrs := node.AttrGetter()
	rekey := &GroupCallEncRekey{CallID: attrs.String("call-id"), CallCreator: attrs.JID("call-creator")}
	var err error
	if rekey.TransactionID, err = requiredUint32Attr(attrs, "transaction-id"); err != nil {
		return nil, err
	}
	encopt, hasEncopt := node.GetOptionalChildByTag("encopt")
	enc, hasEnc := node.GetOptionalChildByTag("enc")
	if !hasEncopt || !hasEnc {
		return nil, fmt.Errorf("signaling: parse group rekey: missing encopt or enc")
	}
	if rekey.KeyGeneration, err = requiredUint32Attr(encopt.AttrGetter(), "keygen"); err != nil || rekey.KeyGeneration != 2 {
		return nil, fmt.Errorf("signaling: parse group rekey: unsupported key generation")
	}
	encAttrs := enc.AttrGetter()
	rekey.EncryptionType = encAttrs.String("type")
	if rekey.EncryptionVersion, err = requiredUint32Attr(encAttrs, "v"); err != nil {
		return nil, err
	}
	if (rekey.EncryptionType != "msg" && rekey.EncryptionType != "pkmsg") || rekey.EncryptionVersion != 2 {
		return nil, fmt.Errorf("signaling: parse group rekey: unsupported encryption")
	}
	ciphertext, ok := enc.Content.([]byte)
	if !ok {
		return nil, fmt.Errorf("signaling: parse group rekey: ciphertext is %T", enc.Content)
	}
	rekey.Ciphertext = bytes.Clone(ciphertext)
	return rekey, nil
}

// ---- helpers de fio locais (prefixo g para não colidir com o 1:1) ----

func gAudioOpus(rate string) waBinary.Node {
	return waBinary.Node{Tag: "audio", Attrs: waBinary.Attrs{"enc": "opus", "rate": rate}}
}

func gVideoOfferNode() waBinary.Node {
	return waBinary.Node{Tag: "video", Attrs: waBinary.Attrs{
		"enc": "h.264", "dec": "H264",
		"screen_width": "1920", "screen_height": "1080", "device_orientation": "0",
	}}
}

func gVideoNodeDec() waBinary.Node {
	return waBinary.Node{Tag: "video", Attrs: waBinary.Attrs{"dec": "H264", "device_orientation": "0"}}
}

func gOfferAction(tag, callID string, callCreator types.JID, children []waBinary.Node) waBinary.Node {
	return waBinary.Node{
		Tag:     tag,
		Attrs:   waBinary.Attrs{"call-id": callID, "call-creator": callCreator},
		Content: children,
	}
}

func gDestinationTo(devices []types.JID) waBinary.Node {
	tos := make([]waBinary.Node, len(devices))
	for i, jid := range devices {
		tos[i] = waBinary.Node{Tag: "to", Attrs: waBinary.Attrs{"jid": jid}}
	}
	return waBinary.Node{Tag: "destination", Content: tos}
}

func gCallWrap(to types.JID, id *string, action waBinary.Node) waBinary.Node {
	stanzaID := GenerateCallStanzaID() // sem id o servidor IGNORA o <call> (não acka)
	if id != nil {
		stanzaID = *id
	}
	attrs := waBinary.Attrs{"to": to, "id": stanzaID}
	return waBinary.Node{Tag: "call", Attrs: attrs, Content: []waBinary.Node{action}}
}

func gBuildPreacceptNode(callID string, to, callCreator types.JID, wrapperID string, audioRates []string, video bool) waBinary.Node {
	children := make([]waBinary.Node, 0, len(audioRates)+3)
	for _, rate := range audioRates {
		children = append(children, gAudioOpus(rate))
	}
	if video {
		children = append(children, gVideoNodeDec())
	}
	children = append(children, waBinary.Node{Tag: "encopt", Attrs: waBinary.Attrs{"keygen": "2"}})
	capability := capabilityPreaccept
	if video {
		capability = capabilityOffer
	}
	children = append(children, waBinary.Node{Tag: "capability", Attrs: waBinary.Attrs{"ver": "1"}, Content: capability})
	return gCallWrap(to, &wrapperID, gOfferAction("preaccept", callID, callCreator, children))
}

func gBuildAcceptNode(callID string, to, callCreator types.JID, audioRates []string) waBinary.Node {
	children := make([]waBinary.Node, 0, len(audioRates)+2)
	for _, rate := range audioRates {
		children = append(children, gAudioOpus(rate))
	}
	children = append(children,
		waBinary.Node{Tag: "net", Attrs: waBinary.Attrs{"medium": "2"}},
		waBinary.Node{Tag: "encopt", Attrs: waBinary.Attrs{"keygen": "2"}},
	)
	return gCallWrap(to, nil, gOfferAction("accept", callID, callCreator, children))
}

func optionalUint32Attr(attrs *waBinary.AttrUtility, key string) (uint32, bool, error) {
	raw, ok := attrs.GetString(key, false)
	if !ok {
		return 0, false, nil
	}
	value, err := strconv.ParseUint(raw, 10, 32)
	if err != nil {
		return 0, true, fmt.Errorf("invalid %s %q: %w", key, raw, err)
	}
	return uint32(value), true, nil
}

func requiredUint32Attr(attrs *waBinary.AttrUtility, key string) (uint32, error) {
	value, ok, err := optionalUint32Attr(attrs, key)
	if err != nil {
		return 0, err
	}
	if !ok {
		return 0, fmt.Errorf("missing %s", key)
	}
	return value, nil
}

func nodeBytes(node *waBinary.Node) []byte {
	switch content := node.Content.(type) {
	case []byte:
		return content
	case string:
		return []byte(content)
	default:
		return nil
	}
}

func parseIndexedTokens(node *waBinary.Node, tag string) [][]byte {
	var tokens [][]byte
	for _, child := range node.GetChildren() {
		if child.Tag != tag {
			continue
		}
		value := nodeBytes(&child)
		if value == nil {
			continue
		}
		index := len(tokens)
		if raw := child.AttrGetter().OptionalString("id"); raw != "" {
			parsed, err := strconv.Atoi(raw)
			if err != nil || parsed < 0 || parsed >= 64 {
				continue
			}
			index = parsed
		}
		for len(tokens) <= index {
			tokens = append(tokens, nil)
		}
		tokens[index] = bytes.Clone(value)
	}
	return tokens
}
