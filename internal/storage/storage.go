package storage

import (
	"encoding/json"
	"os"
	"telemetry-exporter/internal/stats"
)

func LoadStats(storageFile string) (map[string]*stats.InterfaceStats, error) {
	data, err := os.ReadFile(storageFile)
	if err != nil {
		if os.IsNotExist(err) {
			return make(map[string]*stats.InterfaceStats), nil
		}

		return nil, err
	}

	var interfaces map[string]*stats.InterfaceStats
	if err := json.Unmarshal(data, &interfaces); err != nil {
		return nil, err
	}

	return interfaces, nil
}

func SaveStats(storageFile string, interfaces map[string]*stats.InterfaceStats) error {
	data, err := json.MarshalIndent(interfaces, "", "  ")
	if err != nil {
		return err
	}

	return os.WriteFile(storageFile, data, 0644)
}
