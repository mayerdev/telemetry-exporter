package config

type Config struct {
	NATSURL              string `yaml:"nats_url"`
	ExporterID           string `yaml:"exporter_id"`
	StorageFile          string `yaml:"storage_file"`
	CollectionIntervalMS int    `yaml:"collection_interval_ms"`
	InitFromSystem       bool   `yaml:"init_from_system"`
}
