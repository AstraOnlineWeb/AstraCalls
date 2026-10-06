package media

import (
	"testing"

	"wacalls/internal/voip/core"
)

// Rotação de chave preserva o estado de ROC/sequência (continuidade do stream).
func TestSrtpContextWithKeyingPreservesROC(t *testing.T) {
	km1 := core.SrtpKeyingMaterial{MasterKey: make([]byte, 16), MasterSalt: make([]byte, 14)}
	km2 := core.SrtpKeyingMaterial{MasterKey: append([]byte{1}, make([]byte, 15)...), MasterSalt: make([]byte, 14)}
	c, err := NewSrtpContext(km1, 4)
	if err != nil {
		t.Fatal(err)
	}
	c.updateRoc(65000)
	c.updateRoc(10) // wrap -> roc 1
	if c.roc != 1 {
		t.Fatalf("roc = %d, want 1", c.roc)
	}
	n, err := c.WithKeying(km2)
	if err != nil {
		t.Fatal(err)
	}
	if n.roc != 1 || n.lastSeq != 10 || !n.initialized {
		t.Fatalf("state not preserved: roc=%d lastSeq=%d init=%v", n.roc, n.lastSeq, n.initialized)
	}
	if string(n.sessionKey) == string(c.sessionKey) {
		t.Fatal("session key did not change")
	}
}
