package call

import "testing"

func TestNextMohFrameCustomLoop(t *testing.T) {
	m := &CallManager{mohCustom: []float32{1, 2, 3}}
	f := m.nextMohFrameLocked(5)
	want := []float32{1, 2, 3, 1, 2}
	for i := range want {
		if f[i] != want[i] {
			t.Fatalf("frame=%v", f)
		}
	}
	m.mohCustom = nil // volta para a padrão sem estourar índice
	m.mohPos = 2
	_ = m.nextMohFrameLocked(4)
}
