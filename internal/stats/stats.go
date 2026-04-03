package stats

type InterfaceStats struct {
	RX    uint64 `json:"rx"`
	TX    uint64 `json:"tx"`
	Total uint64 `json:"total"`
}

type GlobalStats struct {
	Interfaces map[string]*InterfaceStats `json:"interfaces"`
	LastOSRX   map[string]uint64          `json:"-"`
	LastOSTX   map[string]uint64          `json:"-"`
}

func NewGlobalStats() GlobalStats {
	return GlobalStats{
		Interfaces: make(map[string]*InterfaceStats),
		LastOSRX:   make(map[string]uint64),
		LastOSTX:   make(map[string]uint64),
	}
}
