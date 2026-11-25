package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// dtLayout is the canonical storage format for datetimes in the
// config and reports: minute precision, local time.
const dtLayout = "2006-01-02 15:04"

// parseDateTime parses a stored datetime ("YYYY-MM-DD HH:MM"); a
// legacy date-only value ("YYYY-MM-DD") is accepted as midnight.
func parseDateTime(s string) (time.Time, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, false
	}
	if t, err := time.ParseInLocation(dtLayout, s, time.Local); err == nil {
		return t, true
	}
	if t, err := time.ParseInLocation("2006-01-02", s, time.Local); err == nil {
		return t, true
	}
	return time.Time{}, false
}

// v1.0 unified configuration. One file, managed by the program itself:
// the install script creates it (vpsmon init), the WebUI edits it, the
// daemon hot-applies changes. Sections: web (panel), server
// (aggregator), client (agent).

type WebConfig struct {
	Port     int    `json:"port"`
	Username string `json:"username"`
	Password string `json:"password"`  // pbkdf2 hash, never plaintext
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
	NextReset         string  `json:"next_reset"` // "YYYY-MM-DD HH:MM" local; "" = no auto reset
	Expiry            string  `json:"expiry"`     // "YYYY-MM-DD HH:MM" local; "" = none
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
	// Normalize the two datetimes to the canonical minute-precision
	// format; an unparseable non-empty value is dropped.
	if c.Client.NextReset != "" {
		if t, ok := parseDateTime(c.Client.NextReset); ok {
			c.Client.NextReset = t.Format(dtLayout)
		} else {
			c.Client.NextReset = ""
		}
	}
	if c.Client.Expiry != "" {
		if t, ok := parseDateTime(c.Client.Expiry); ok {
			c.Client.Expiry = t.Format(dtLayout)
		} else {
			c.Client.Expiry = ""
		}
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
