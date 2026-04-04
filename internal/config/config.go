package config

type EventsConfig struct {
	Enabled    bool   `yaml:"enabled"`
	Subject    string `yaml:"subject"`
	Threshold  uint64 `yaml:"threshold"`
	IntervalMS int    `yaml:"interval_ms"`
}

type Config struct {
	NATSURL              string       `yaml:"nats_url"`
	ExporterID           string       `yaml:"exporter_id"`
	StorageFile          string       `yaml:"storage_file"`
	HooksFile            string       `yaml:"hooks_file"`
	CollectionIntervalMS int          `yaml:"collection_interval_ms"`
	InitFromSystem       bool         `yaml:"init_from_system"`
	Events               EventsConfig `yaml:"events"`
}
