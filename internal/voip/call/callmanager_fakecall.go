package call

import (
	"context"
	"time"

	"wacalls/internal/voip/media"
	"wacalls/internal/voip/signaling"

	"go.mau.fi/whatsmeow/types"
)

// FakeCall dispara um "toque fantasma": envia o offer de chamada (o aparelho do
// destinatário TOCA assim que o recebe) e, após `dur`, envia o terminate — sem
// nunca estabelecer mídia/WebRTC. Serve só para chamar a atenção do usuário, na
// mesma ideia dos endpoints de "fake call" da WAHA/Evolution.
//
// É fire-and-forget: NÃO ocupa o slot de chamada real (currentCall) nem entra no
// state machine, então pode ser disparado mesmo com outra chamada em andamento e
// não interfere nela. Retorna o call-id gerado (o terminate é agendado em bg).
func (m *CallManager) FakeCall(ctx context.Context, peerJid types.JID, isVideo bool, dur time.Duration) (string, error) {
	creator := m.sock.OwnLID()
	if creator.IsEmpty() {
		creator = m.sock.OwnPN()
	}
	resolved := m.sock.ResolveLIDForPN(ctx, peerJid)

	callID := signaling.GenerateCallID()
	callKey := media.GenerateCallKey()

	offer, err := signaling.BuildOfferStanza(ctx, m.sock, callID, callKey, resolved, isVideo)
	if err != nil {
		return "", err
	}
	if err := m.sock.SendNode(ctx, offer); err != nil {
		return "", err
	}
	m.log.Info("fake call offer sent", "call_id", callID, "peer", resolved.String(), "duration", dur.String())

	// Encerra em background: usamos context.Background() porque o ctx da requisição
	// HTTP é cancelado assim que o handler responde, e o terminate precisa sair depois.
	go func() {
		timer := time.NewTimer(dur)
		defer timer.Stop()
		<-timer.C
		term := signaling.BuildTerminateStanza(resolved, callID, creator)
		if err := m.sock.SendNode(context.Background(), term); err != nil {
			m.log.Error("fake call terminate failed", "call_id", callID, "err", err)
			return
		}
		m.log.Info("fake call terminated", "call_id", callID)
	}()

	return callID, nil
}
