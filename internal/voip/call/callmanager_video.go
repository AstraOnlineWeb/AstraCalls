package call

func (m *CallManager) FeedCapturedVideo(au []byte) {
	m.video.FeedCaptured(au)
}

// VideoBufferedAmount expõe quanto está bufferizado no canal do relay (bytes). O
// disparo de vídeo (broadcast) usa isso pra PACING: só manda o próximo frame
// quando o canal drenou, em vez de deixar o pipeline dropar (que trava o vídeo).
func (m *CallManager) VideoBufferedAmount() uint64 {
	if m.relay == nil {
		return 0
	}
	return m.relay.BufferedAmount()
}
