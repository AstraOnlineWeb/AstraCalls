package media

// Desempacotamento das camadas EXTERNAS do payload MLow do WhatsApp, antes de
// entregar o frame "nu" ao decoder (opus_decode com SMPL). São Go puro (sem cgo)
// de propósito: compilam em qualquer build e são testáveis sem a libopus_mlow.
//
// Duas camadas aparecem no fio e QUEBRAM o áudio se passadas cruas ao decoder:
//
//   1. Container multi-frame 0x92 — o WhatsApp empacota vários frames MLow de 60ms
//      sequenciais num único payload RTP. Aparece em CHAMADA DE VÍDEO com DTX ligado
//      ("depois do upgrade de vídeo"). Sem desempacotar, o decoder lê o header
//      0x92/contagem/tamanhos COMO SE FOSSE áudio -> silêncio/ruído e a voz real
//      nunca é decodificada. É aplicado SEMPRE (marcador 0x92 é distinto do TOC nu).
//
//   2. SplitRed/RED — redundância ligada em rede ruim. OPCIONAL: só pode ser aplicada
//      quando a redundância foi NEGOCIADA (redundancy > 0); num frame nu, rodar RED
//      corromperia (o 1º byte com bit alto seria lido como bloco redundante). Por isso
//      fica desligada por padrão (igual ao meowcaller, que também nunca liga em prod).
//
// Portado de github.com/purpshell/meowcaller (MIT, Rajeh Taher): mlow/decoder.go
// (splitContainer), mlow/red.go (DepackSplitRed) e mlow/toc.go (parse do TOC), cuja
// fonte é github.com/JotaDev66/WaCalls/.../mlow/decoder.go e oxidezap/whatsapp-rust.

// MLowDebug, se não-nil, recebe eventos de desempacotamento (diagnóstico opt-in,
// ligado pelo pacote call quando WACALLS_SIP_DEBUG=1). Mantém o pacote media sem
// dependência de logger.
var MLowDebug func(msg string, kv ...any)

// splitContainer separa um container multi-frame 0x92 em seus sub-frames sequenciais:
//
//	0x92 <count> [ <len> <frame> ]*(count-1) <último frame = resto>
//
// Devolve ok=false pra qualquer coisa que não seja um container bem-formado (aí o
// chamador decodifica o payload como frame nu). O marcador 0x92 é distinto dos bytes
// de TOC de um frame nu, e a validação exige que os tamanhos fechem com o payload.
func splitContainer(p []byte) ([][]byte, bool) {
	if len(p) < 3 || p[0] != 0x92 {
		return nil, false
	}
	count := int(p[1])
	if count < 2 || count > 8 {
		return nil, false
	}
	frames := make([][]byte, 0, count)
	off := 2
	for i := 0; i < count-1; i++ {
		if off >= len(p) {
			return nil, false
		}
		flen := int(p[off])
		off++
		if off+flen > len(p) {
			return nil, false
		}
		frames = append(frames, p[off:off+flen])
		off += flen
	}
	if off >= len(p) {
		return nil, false
	}
	frames = append(frames, p[off:])
	return frames, true
}

// depackSplitRedMain extrai o frame PRINCIPAL (o atual, que vem por último) de um
// payload SplitRed/RED. Só deve ser chamada quando a redundância foi negociada —
// num frame nu o cabeçalho seria mal interpretado. Devolve ok=false se o header não
// fecha (aí o chamador trata como frame nu). Os blocos redundantes (anteriores ao
// principal) servem só pra PLC em perda; aqui decodificamos apenas o principal.
//
// Layout: [ (code|0x80) <size> ]* <mainCode <0x80> > <blocos redundantes...> <main>
func depackSplitRedMain(p []byte) ([]byte, bool) {
	n := len(p)
	if n == 0 {
		return nil, false
	}
	// Soma dos tamanhos redundantes + bytes de cabeçalho, pra achar onde começa o main.
	cur := 0
	rem := n
	var redSizes []int
	for {
		if rem == 0 {
			return nil, false // header sem marcador de main
		}
		b0 := p[cur]
		if b0 < 0x80 {
			// marcador do main (bit alto limpo) termina o cabeçalho
			if rem <= 1 {
				return nil, false
			}
			break
		}
		if rem <= 2 {
			return nil, false
		}
		size := int(p[cur+1])
		if size+2 >= rem {
			return nil, false
		}
		redSizes = append(redSizes, size)
		cur += 2
		rem -= size + 2
	}
	// Exige ao menos 1 bloco redundante: sem isso é indistinguível de um frame nu
	// (cujo TOC também começa com bit alto limpo) e NÃO devemos mexer.
	if len(redSizes) == 0 {
		return nil, false
	}
	cur++ // pula o byte do mainCode
	for _, s := range redSizes {
		cur += s // pula os dados dos blocos redundantes
	}
	mainSize := rem - 1 // total - header - sum(redundantes)
	if cur < 0 || mainSize <= 0 || cur+mainSize > n {
		return nil, false
	}
	return p[cur : cur+mainSize], true
}

// isStdOpusTOC diz se o byte de TOC indica um pacote Opus padrão (SID+VAD ambos 1 ->
// 0xC0), que o WhatsApp roteia ao CELT em vez do caminho MLow. Mantido para
// diagnóstico/roteamento futuro.
func isStdOpusTOC(b byte) bool { return b&0xC0 == 0xC0 }

// mlowTOC é o mínimo do primeiro byte de um frame MLow nu, para diagnóstico.
type mlowTOC struct {
	SID     bool
	Active  bool
	LowRate bool // bit 2: false = low-rate (6kbps), true = high-rate
	StdOpus bool
}

func parseMlowTOC(b byte) mlowTOC {
	if isStdOpusTOC(b) {
		return mlowTOC{StdOpus: true}
	}
	bit1 := (b>>1)&1 != 0
	vad := (b>>6)&1 != 0
	return mlowTOC{
		SID:     b>>7 != 0,
		Active:  vad || bit1,
		LowRate: (b>>2)&1 != 0,
	}
}
