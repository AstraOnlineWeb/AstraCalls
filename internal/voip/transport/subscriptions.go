package transport

func encodeVarint(value uint64) []byte {
	var out []byte
	v := value
	for v > 0x7f {
		out = append(out, byte((v&0x7f)|0x80))
		v >>= 7
	}
	out = append(out, byte(v&0x7f))
	return out
}

func encodeProtobufVarintField(fieldNumber int, value uint64) []byte {
	tag := encodeVarint(uint64(fieldNumber << 3))
	return append(tag, encodeVarint(value)...)
}

func encodeProtobufLengthDelimited(fieldNumber int, data []byte) []byte {
	tag := encodeVarint(uint64((fieldNumber << 3) | 2))
	out := append(tag, encodeVarint(uint64(len(data)))...)
	return append(out, data...)
}

func BuildSenderSubscriptions(ssrc uint32) []byte {
	inner := concat(
		encodeProtobufVarintField(3, uint64(ssrc)),
		encodeProtobufVarintField(5, 0),
		encodeProtobufVarintField(6, 0),
	)
	return encodeProtobufLengthDelimited(1, inner)
}

func BuildSSRCSubscriptionList(selfSsrcs, peerSsrcs []uint32, selfPid, peerPid int) []byte {
	var entries [][]byte
	entries = append(entries, subscriptionEntries(selfPid, selfSsrcs)...)
	entries = append(entries, subscriptionEntries(peerPid, peerSsrcs)...)
	return concat(entries...)
}

// subscriptionEntries monta as entradas {pid, 1, ssrc} de um participante.
func subscriptionEntries(pid int, ssrcs []uint32) [][]byte {
	var entries [][]byte
	for _, ssrc := range ssrcs {
		if ssrc == 0 {
			continue
		}
		inner := concat(
			encodeProtobufVarintField(1, uint64(pid)),
			encodeProtobufVarintField(2, 1),
			encodeProtobufVarintField(3, uint64(ssrc)),
		)
		entries = append(entries, encodeProtobufLengthDelimited(1, inner))
	}
	return entries
}

// GroupSub é a assinatura de UM participante de chamada em grupo: o PID que o
// group_update atribuiu e os SSRCs de stream dele.
type GroupSub struct {
	Pid   int
	Ssrcs []uint32
}

// BuildGroupSubscriptionList monta a lista de assinatura para uma chamada em GRUPO:
// as entradas do próprio (selfPid) + as de cada participante remoto com o PID dele.
// Mesmo formato por-entrada do 1:1, mas com N PIDs distintos (em 1:1 era sempre 0).
func BuildGroupSubscriptionList(selfPid int, selfSsrcs []uint32, peers []GroupSub) []byte {
	var entries [][]byte
	entries = append(entries, subscriptionEntries(selfPid, selfSsrcs)...)
	for _, p := range peers {
		entries = append(entries, subscriptionEntries(p.Pid, p.Ssrcs)...)
	}
	return concat(entries...)
}
