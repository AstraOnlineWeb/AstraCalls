package call

func (m *CallManager) FeedCapturedVideo(au []byte) {
	m.video.FeedCaptured(au)
}
