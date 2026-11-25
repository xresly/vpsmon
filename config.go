package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
)

// parseResetTime parses an "HH:MM" string into hour and minute.
func parseResetTime(s string) (int, int, bool) {
	if len(s) != 5 || s[2] != ':' {
		return 0, 0, false
	}
	h, err1 := strconv.Atoi(s[:2])
	m, err2 := strconv.Atoi(s[3:])
	if err1 != nil || err2 != nil || h < 0 || h > 23 || m < 0 || m > 59 {
		return 0, 0, false
	}
	return h, m, true
}

// v1.0 unified configuration. One file, managed by the program itself:
// the install script creates it (vpsmon init), the WebUI edits it, the
// daemon hot-applies changes. Sections: web (panel), server
// (aggregator), client (agent).

type WebConfig struct {
	Port     int    `json:"port"`
	Username string `json:"username"`
	Password string `json:"password"` // pbkdf2 hash, never plaintext
	APIToken string `json:"api_token"` // read-only token for /api/nodes
	TLSCert  string `json:"tls_cert"`
	TLSKey   string `json:"tls_key"`
}

type ServerConfig struct {
	Enabled     bool   `json:"enabled"`
	IngestToken string `json:"ingest_token"`
}

type ClientConfig struct {
	Enabled           bool    `json:"enabled"`
	ServerURL         string  `json:"server_url"`
	Token             string  `json:"token"`
	NodeName          string  `json:"node_name"`
	QuotaG            float64 `json:"quota_g"`
	ResetDay          int     `json:"reset_day"`
	ResetTime         string  `json:"reset_time"` // "HH:MM" local, minute the cycle rolls over
	Expiry            string  `json:"expiry"`
	InitialUsedG      float64 `json:"initial_used_g"`
	TrafficMode       string  `json:"traffic_mode"`
	Interface         string  `json:"interface"`
	SampleIntervalSec int     `json:"sample_interval_sec"`
	ReportIntervalSec int     `json:"report_interval_sec"`
}

type AppConfig struct {
	Version   int          `json:"version"`
	Web       WebConfig    `json:"web"`
	Server    ServerConfig `json:"server"`
	Client    ClientConfig `json:"client"`
	StateFile string       `json:"state_file"`
}

func (c *AppConfig) applyDefaults() {
	if c.Version == 0 {
		c.Version = 1
	}
	if c.Web.Port <= 0 || c.Web.Port > 65535 {
		c.Web.Port = 2053
	}
	if c.Web.Username == "" {
		c.Web.Username = "admin"
	}
	if c.Client.SampleIntervalSec <= 0 {
		c.Client.SampleIntervalSec = 30
	}
	if c.Client.ReportIntervalSec <= 0 {
		c.Client.ReportIntervalSec = 60
	}
	if c.Client.TrafficMode == "" {
		c.Client.TrafficMode = "both"
	}
	if c.Client.Interface == "" {
		c.Client.Interface = "auto"
	}
	if c.Client.ResetDay < 1 || c.Client.ResetDay > 28 {
		c.Client.ResetDay = 1
	}
	if _, _, ok := parseResetTime(c.Client.ResetTime); !ok {
		c.Client.ResetTime = "00:00"
	}
	if c.StateFile == "" {
		c.StateFile = "/var/lib/vpsmon/state.json"
	}
}

func loadAppConfig(path string) (*AppConfig, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var c AppConfig
	if err := json.Unmarshal(b, &c); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	c.applyDefaults()
	return &c, nil
}

func (c *AppConfig) save(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
