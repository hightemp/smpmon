//go:build windows

package gops

import (
	"context"
	"fmt"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"

	"github.com/hightemp/smpmon/internal/collect"
)

// systemPowerStatus mirrors SYSTEM_POWER_STATUS.
type systemPowerStatus struct {
	ACLineStatus        byte
	BatteryFlag         byte
	BatteryLifePercent  byte
	SystemStatusFlag    byte
	BatteryLifeTime     uint32
	BatteryFullLifeTime uint32
}

var procGetSystemPowerStatus = windows.NewLazySystemDLL("kernel32.dll").NewProc("GetSystemPowerStatus")

func readBatteries(context.Context) ([]collect.Battery, error) {
	var st systemPowerStatus
	if r, _, err := procGetSystemPowerStatus.Call(uintptr(unsafe.Pointer(&st))); r == 0 {
		return nil, fmt.Errorf("GetSystemPowerStatus: %w", err)
	}
	if st.BatteryFlag&128 != 0 || st.BatteryFlag == 255 || st.BatteryLifePercent == 255 {
		return nil, nil // no system battery
	}
	return []collect.Battery{parsePowerStatus(st)}, nil
}

func parsePowerStatus(st systemPowerStatus) collect.Battery {
	b := collect.Battery{Name: "battery", Percent: float64(st.BatteryLifePercent), Status: "unknown"}
	switch {
	case st.BatteryFlag&8 != 0:
		b.Status = "charging"
	case st.ACLineStatus == 1 && st.BatteryLifePercent >= 100:
		b.Status = "full"
	case st.ACLineStatus == 1:
		b.Status = "not charging"
	case st.ACLineStatus == 0:
		b.Status = "discharging"
	}
	if st.BatteryLifeTime != 0xFFFFFFFF && b.Status == "discharging" {
		b.TimeLeft = time.Duration(st.BatteryLifeTime) * time.Second
	}
	return b
}
