package collector

import (
	"bufio"
	"os"
	"strconv"
	"strings"
	"telemetry-exporter/internal/stats"
)

func CollectNetwork(s *stats.GlobalStats, initFromSystem bool) {
	file, err := os.Open("/proc/net/dev")
	if err != nil {
		return
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.Contains(line, ":") {
			continue
		}

		parts := strings.Split(line, ":")
		if len(parts) < 2 {
			continue
		}

		iface := strings.TrimSpace(parts[0])
		fields := strings.Fields(parts[1])
		if len(fields) < 9 {
			continue
		}

		rx, _ := strconv.ParseUint(fields[0], 10, 64)
		tx, _ := strconv.ParseUint(fields[8], 10, 64)

		if _, exists := s.Interfaces[iface]; !exists {
			if initFromSystem {
				s.Interfaces[iface] = &stats.InterfaceStats{
					RX:    rx,
					TX:    tx,
					Total: rx + tx,
				}
			} else {
				s.Interfaces[iface] = &stats.InterfaceStats{}
			}
		}

		prevOSRX, okRX := s.LastOSRX[iface]
		prevOSTX, okTX := s.LastOSTX[iface]

		var deltaRX, deltaTX uint64
		if okRX {
			if rx >= prevOSRX {
				deltaRX = rx - prevOSRX
			} else {
				deltaRX = rx
			}
		}

		if okTX {
			if tx >= prevOSTX {
				deltaTX = tx - prevOSTX
			} else {
				deltaTX = tx
			}
		}

		s.Interfaces[iface].RX += deltaRX
		s.Interfaces[iface].TX += deltaTX
		s.Interfaces[iface].Total = s.Interfaces[iface].RX + s.Interfaces[iface].TX

		s.LastOSRX[iface] = rx
		s.LastOSTX[iface] = tx
	}
}
