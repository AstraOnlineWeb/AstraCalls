package call

import "testing"

// Regressão do "participante preso em conectando" na 2ª chamada: End() precisa
// zerar os contadores de transação (roster e epoch); senão os group_update e
// enc_rekey da chamada seguinte (que reusam tx 9/11/15/17) são descartados.
func TestGroupEndResetsTransactionState(t *testing.T) {
	m := NewGroupCallManager(nil, nil)
	m.callID = "OLD"
	m.rosterTxID = 17
	m.epochTxID = 15
	m.epochKey = make([]byte, 32)
	m.groupKey = []byte{1, 2, 3}
	m.relayStarted = true
	m.selfSsrcs[0] = 42
	ended := ""
	m.OnEnded = func(id string) { ended = id }

	m.End()

	if ended != "OLD" {
		t.Fatalf("OnEnded = %q, want OLD", ended)
	}
	if m.CallID() != "" || m.rosterTxID != 0 || m.epochTxID != 0 || m.epochKey != nil || m.groupKey != nil || m.relayStarted || m.selfSsrcs[0] != 0 {
		t.Fatalf("estado não zerado: callID=%q roster=%d epoch=%d key=%v relay=%v", m.CallID(), m.rosterTxID, m.epochTxID, m.epochKey != nil, m.relayStarted)
	}
	// End() de novo, sem chamada: não pode chamar OnEnded nem explodir.
	ended = ""
	m.End()
	if ended != "" {
		t.Fatalf("OnEnded chamado sem chamada ativa: %q", ended)
	}
}
