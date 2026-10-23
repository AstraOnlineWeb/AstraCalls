package main

import (
	"testing"

	"go.mau.fi/whatsmeow/proto/waCompanionReg"
	"go.mau.fi/whatsmeow/store"
)

func TestConfigureDeviceIdentity(t *testing.T) {
	t.Setenv("WACALLS_DEVICE_NAME", "")
	t.Setenv("WACALLS_DEVICE_BROWSER", "")
	configureDeviceIdentity()
	if store.DeviceProps.GetOs() != "Windows" || store.DeviceProps.GetPlatformType() != waCompanionReg.DeviceProps_CHROME {
		t.Fatalf("default = %q/%v", store.DeviceProps.GetOs(), store.DeviceProps.GetPlatformType())
	}
	t.Setenv("WACALLS_DEVICE_NAME", "Atendimento")
	t.Setenv("WACALLS_DEVICE_BROWSER", "Desktop")
	configureDeviceIdentity()
	if store.DeviceProps.GetOs() != "Atendimento" || store.DeviceProps.GetPlatformType() != waCompanionReg.DeviceProps_DESKTOP {
		t.Fatalf("custom = %q/%v", store.DeviceProps.GetOs(), store.DeviceProps.GetPlatformType())
	}
	t.Setenv("WACALLS_DEVICE_BROWSER", "netscape")
	if _, b := configureDeviceIdentity(); b != "chrome" {
		t.Fatalf("inválido deveria cair em chrome, veio %q", b)
	}
}
