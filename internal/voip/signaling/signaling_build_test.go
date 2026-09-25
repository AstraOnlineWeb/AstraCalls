package signaling

import (
	"testing"

	"wacalls/internal/voip/wanode"

	waBinary "go.mau.fi/whatsmeow/binary"
	"go.mau.fi/whatsmeow/types"
)

func findChild(n *waBinary.Node, tag string) *waBinary.Node {
	for _, c := range wanode.NodeChildren(n) {
		if c.Tag == tag {
			cc := c
			return &cc
		}
	}
	return nil
}

func TestBuildTerminateElsewhereStanza(t *testing.T) {
	peer := types.NewJID("62440234549366", types.HiddenUserServer)
	creator := types.NewJID("111", types.HiddenUserServer)
	dev0 := types.NewJID("62440234549366", types.HiddenUserServer)
	dev0.Device = 0
	dev5 := types.NewJID("62440234549366", types.HiddenUserServer)
	dev5.Device = 5

	node := BuildTerminateElsewhereStanza(peer, "CID", creator, []types.JID{dev0, dev5})

	if node.Tag != "call" {
		t.Fatalf("wrapper tag = %q", node.Tag)
	}
	children := wanode.NodeChildren(&node)
	if len(children) != 1 || children[0].Tag != "terminate" {
		t.Fatalf("esperava um único filho terminate, veio %+v", children)
	}
	term := children[0]
	if r := wanode.AttrString(term.Attrs, "reason"); r != "accepted_elsewhere" {
		t.Fatalf("reason = %q, want accepted_elsewhere", r)
	}
	if id := wanode.AttrString(term.Attrs, "call-id"); id != "CID" {
		t.Fatalf("call-id = %q", id)
	}
	dst := findChild(&term, "destination")
	if dst == nil {
		t.Fatal("destination ausente")
	}
	tos := wanode.NodeChildren(dst)
	if len(tos) != 2 {
		t.Fatalf("esperava 2 devices no destination, veio %d", len(tos))
	}
	if j := wanode.AttrString(tos[1].Attrs, "jid"); j != dev5.String() {
		t.Fatalf("segundo destination = %q, want %q", j, dev5)
	}
}

// O reject precisa carregar count="0" (igual ao whatsmeow); sem ele o chamador
// não registra a recusa e continua tocando.
func TestBuildRejectStanzaHasCount(t *testing.T) {
	peer := types.NewJID("62440234549366", types.HiddenUserServer)
	creator := types.NewJID("62440234549366", types.HiddenUserServer)
	own := types.NewJID("111222333", types.HiddenUserServer)
	own.Device = 3 // deve ser normalizado (ToNonAD) no from

	node := BuildRejectStanza(peer, "CID", creator, own)
	if node.Tag != "call" {
		t.Fatalf("wrapper tag = %q", node.Tag)
	}
	// o <call> precisa carregar from=ownID (ToNonAD) e to=callFrom
	if f := wanode.AttrString(node.Attrs, "from"); f != own.ToNonAD().String() {
		t.Fatalf("from = %q, want %q", f, own.ToNonAD())
	}
	if to := wanode.AttrString(node.Attrs, "to"); to != peer.String() {
		t.Fatalf("to = %q, want %q", to, peer)
	}
	rej := findChild(&node, "reject")
	if rej == nil {
		t.Fatal("nó reject ausente")
	}
	if c := wanode.AttrString(rej.Attrs, "count"); c != "0" {
		t.Fatalf("count = %q, want \"0\"", c)
	}
	if id := wanode.AttrString(rej.Attrs, "call-id"); id != "CID" {
		t.Fatalf("call-id = %q", id)
	}
}
