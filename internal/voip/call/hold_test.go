package call

import (
	"testing"

	"wacalls/internal/voip/core"
)

// activeCallForTest leva uma chamada de saída até o estado Active.
func activeCallForTest(t *testing.T) *CallInfo {
	t.Helper()
	c := NewOutgoingCall("callid", "peer@s.whatsapp.net", "me@s.whatsapp.net", core.CallMediaTypeAudio)
	for _, tr := range []string{TransitionOfferSent, TransitionRemoteAccepted, TransitionMediaConnected} {
		if err := c.ApplyTransition(Transition{Type: tr}); err != nil {
			t.Fatalf("transição %s falhou: %v", tr, err)
		}
	}
	if c.StateData.State != core.CallStateActive {
		t.Fatalf("esperava active, veio %s", c.StateData.State)
	}
	return c
}

func TestHoldResumeCiclo(t *testing.T) {
	c := activeCallForTest(t)

	if err := c.ApplyTransition(Transition{Type: TransitionHold}); err != nil {
		t.Fatalf("hold falhou: %v", err)
	}
	if c.StateData.State != core.CallStateOnHold {
		t.Fatalf("esperava on_hold, veio %s", c.StateData.State)
	}
	// hold duas vezes é inválido (só sai de Active)
	if err := c.ApplyTransition(Transition{Type: TransitionHold}); err == nil {
		t.Fatal("esperava erro ao dar hold já em espera")
	}

	if err := c.ApplyTransition(Transition{Type: TransitionResume}); err != nil {
		t.Fatalf("resume falhou: %v", err)
	}
	if c.StateData.State != core.CallStateActive {
		t.Fatalf("esperava active após resume, veio %s", c.StateData.State)
	}
	// resume fora de espera é inválido
	if err := c.ApplyTransition(Transition{Type: TransitionResume}); err == nil {
		t.Fatal("esperava erro ao dar resume sem estar em espera")
	}
}

func TestMOHLoopValido(t *testing.T) {
	if len(mohLoop) == 0 {
		t.Fatal("mohLoop vazio")
	}
	// amplitude dentro de [-1,1] e com sinal não-nulo em algum ponto
	nonZero := false
	for _, s := range mohLoop {
		if s < -1 || s > 1 {
			t.Fatalf("amostra fora de faixa: %f", s)
		}
		if s != 0 {
			nonZero = true
		}
	}
	if !nonZero {
		t.Fatal("mohLoop só tem silêncio")
	}
}

func TestNextMohFrameEnrolaNoLoop(t *testing.T) {
	m := &CallManager{}
	frameSize := 960
	// consome mais que o tamanho do loop para forçar o wrap-around
	total := len(mohLoop) + frameSize
	got := 0
	for got < total {
		f := m.nextMohFrameLocked(frameSize)
		if len(f) != frameSize {
			t.Fatalf("frame com tamanho %d, esperado %d", len(f), frameSize)
		}
		got += frameSize
	}
	if m.mohPos >= len(mohLoop) {
		t.Fatalf("mohPos não deu wrap: %d >= %d", m.mohPos, len(mohLoop))
	}
}
