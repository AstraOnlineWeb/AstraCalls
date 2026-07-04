package main

import (
	"fmt"
	"strings"
	"time"

	"go.mau.fi/whatsmeow/proto/waE2E"
)

// brt é o fuso do Brasil (sem horário de verão desde 2019).
var brt = time.FixedZone("BRT", -3*3600)

func fmtEventTime(unixSec int64) string {
	return time.Unix(unixSec, 0).In(brt).Format("02/01/2006 15:04")
}

// eventText formata um EventMessage (evento do WhatsApp) recebido.
func eventText(ev *waE2E.EventMessage) string {
	if ev == nil {
		return ""
	}
	var b strings.Builder
	if ev.GetIsCanceled() {
		b.WriteString("📅 *Evento (CANCELADO)")
	} else {
		b.WriteString("📅 *Evento")
	}
	if name := ev.GetName(); name != "" {
		b.WriteString(": " + name)
	}
	b.WriteString("*")

	if d := ev.GetDescription(); d != "" {
		b.WriteString("\n" + d)
	}
	if st := ev.GetStartTime(); st > 0 {
		b.WriteString("\n🕒 " + fmtEventTime(st))
		if et := ev.GetEndTime(); et > 0 {
			b.WriteString(" até " + fmtEventTime(et))
		}
	}
	if loc := ev.GetLocation(); loc != nil {
		if n := loc.GetName(); n != "" {
			b.WriteString("\n📍 " + n)
		}
		if lat, lng := loc.GetDegreesLatitude(), loc.GetDegreesLongitude(); lat != 0 || lng != 0 {
			b.WriteString(fmt.Sprintf("\nhttps://maps.google.com/?q=%f,%f", lat, lng))
		}
	}
	if link := ev.GetJoinLink(); link != "" {
		b.WriteString("\n🔗 " + link)
	}
	return b.String()
}
