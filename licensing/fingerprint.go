package licensing

import (
	"crypto/sha256"
	"fmt"
	"net"
	"runtime"
	"strings"

	"github.com/denisbrodbeck/machineid"
	"github.com/shirou/gopsutil/v3/cpu"
	"github.com/shirou/gopsutil/v3/host"
)

type Fingerprint struct {
	MachineID  string `json:"machine_id"`
	CPUInfo    string `json:"cpu_info"`
	HostID     string `json:"host_id"`
	OS         string `json:"os"`
	Arch       string `json:"arch"`
	PrimaryMAC string `json:"primary_mac"`
}

func GenerateFingerprint() (*Fingerprint, error) {
	fp := &Fingerprint{
		OS:   runtime.GOOS,
		Arch: runtime.GOARCH,
	}

	mid, err := machineid.ID()
	if err == nil {
		fp.MachineID = mid
	}

	if cpuInfo, err := cpu.Info(); err == nil && len(cpuInfo) > 0 {
		fp.CPUInfo = cpuInfo[0].ModelName
	}

	if h, err := host.Info(); err == nil {
		fp.HostID = h.HostID
	}

	fp.PrimaryMAC = getPrimaryMAC()

	return fp, nil
}

func (fp *Fingerprint) Hash() string {
	raw := fmt.Sprintf("%s|%s|%s|%s",
		fp.MachineID, fp.CPUInfo, fp.HostID, fp.PrimaryMAC)
	hash := sha256.Sum256([]byte(raw))
	return fmt.Sprintf("%x", hash[:16])
}

func getPrimaryMAC() string {
	interfaces, err := net.Interfaces()
	if err != nil {
		return ""
	}
	for _, iface := range interfaces {
		if iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		if iface.Flags&net.FlagUp == 0 {
			continue
		}
		if len(iface.HardwareAddr) == 0 {
			continue
		}
		mac := iface.HardwareAddr.String()
		if strings.HasPrefix(mac, "00:00:00:00:00:00") {
			continue
		}
		return mac
	}
	return ""
}

func (fp *Fingerprint) MatchScore(stored *Fingerprint) float64 {
	if stored == nil {
		return 0
	}
	score := 0.0
	total := 0.0

	total += 3.0
	if fp.MachineID == stored.MachineID {
		score += 3.0
	}

	total += 2.0
	if fp.CPUInfo == stored.CPUInfo {
		score += 2.0
	}

	total += 1.0
	if fp.HostID == stored.HostID {
		score += 1.0
	}

	total += 1.0
	if fp.PrimaryMAC == stored.PrimaryMAC {
		score += 1.0
	}

	return score / total
}

const MatchThreshold = 0.6
