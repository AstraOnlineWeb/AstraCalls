package main

import (
	"strings"

	"go.mau.fi/whatsmeow/proto/waCompanionReg"
	"go.mau.fi/whatsmeow/store"
)

// configureDeviceIdentity define como o dispositivo vinculado aparece no celular
// (Dispositivos conectados). Sem isto o whatsmeow se apresenta como
// "whatsmeow" com plataforma desconhecida — assinatura óbvia de cliente não
// oficial. Vale só para pareamentos NOVOS (o nome vai no registro do device).
//
//	WACALLS_DEVICE_NAME    nome exibido (default "Windows")
//	WACALLS_DEVICE_BROWSER chrome | edge | firefox | safari | desktop (default chrome)
func configureDeviceIdentity() (name, browser string) {
	name = strings.TrimSpace(envStr("WACALLS_DEVICE_NAME", "Windows"))
	browser = strings.ToLower(strings.TrimSpace(envStr("WACALLS_DEVICE_BROWSER", "chrome")))
	platform, ok := map[string]waCompanionReg.DeviceProps_PlatformType{
		"chrome":  waCompanionReg.DeviceProps_CHROME,
		"edge":    waCompanionReg.DeviceProps_EDGE,
		"firefox": waCompanionReg.DeviceProps_FIREFOX,
		"safari":  waCompanionReg.DeviceProps_SAFARI,
		"desktop": waCompanionReg.DeviceProps_DESKTOP,
	}[browser]
	if !ok {
		browser, platform = "chrome", waCompanionReg.DeviceProps_CHROME
	}
	store.DeviceProps.Os = &name
	store.DeviceProps.PlatformType = platform.Enum()
	return name, browser
}
