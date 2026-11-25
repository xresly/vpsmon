package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// ---------- report payload (shared with the server side) ----------

type TrafficReport struct {
	QuotaG          float64 `json:"quota_g"`
	CycleUsedG      float64 `json:"cycle_used_g"` // seed + counted this cycle, combined per traffic_mode
	CycleRXG        float64 `json:"cycle_rx_g"`   // counted only (no seed)
	CycleTXG        float64 `json:"cycle_tx_g"`
	RemainingG      float64 `json:"remaining_g"`
	Last5mG         float64 `json:"last5m_g"`
	Last5mRXG       float64 `json:"last5m_rx_g"`
	Last5mTXG       float64 `json:"last5m_tx_g"`
	Last5mWindowSec int     `json:"last5m_window_sec"`
	TotalG          float64 `json:"total_g"` // lifetime counted, combined
	ResetDay        int     `json:"reset_day"`
	NextReset       string  `json:"next_reset"`
	Expiry          string  `json:"expiry"`
	DaysLeft        *int    `json:"days_left,omitempty"`
}

type Report struct {
	Node       string        `json:"node"`
	Hostname   string        `json:"hostname"`
	Version    string        `json:"version"`
	Time       string        `json:"time"`
	UptimeSec  float64       `json:"uptime_sec"`
	Kernel     string        `json:"kernel"`
	OS         string        `json:"os"`
	CPUPct     float64       `json:"cpu_pct"`
	CPUPct5m   float64       `json:"cpu_pct_5m"`
	Load1      float64       `json:"load1"`
	MemTotalG  float64       `json:"mem_total_g"`
	MemUsedG   float64       `json:"mem_used_g"`
	MemPct     float64       `json:"mem_pct"`
	DiskTotalG float64       `json:"disk_total_g"`
	DiskUsedG  float64       `json:"disk_used_g"`
	DiskPct    float64       `json:"disk_pct"`
	Traffic    TrafficReport `json:"traffic"`
}

// ---------- persistent accounting state ----------

type minuteSample struct {
	T      time.Time `json:"t"`
	LifeRX uint64    `json:"life_rx"`
	LifeTX uint64    `json:"life_tx"`
}

type agentState struct {
	BootID       string         `json:"boot_id"`
	Ifaces       []string       `json:"ifaces"`
	LastRawRX    uint64         `json:"last_raw_rx"`
	LastRawTX    uint64         `json:"last_raw_tx"`
	LifeRX       uint64         `json:"life_rx"`
	LifeTX       uint64         `json:"life_tx"`
	SeedBytes    float64        `json:"seed_bytes"` // initial used for the current cycle
	CycleKey     string         `json:"cycle_key"`  // cycle start date, local time
	CycleStartRX uint64         `json:"cycle_start_rx"`
	CycleStartTX uint64         `json:"cycle_start_tx"`
	Samples      []minuteSample `json:"samples"`
	LastCPUTotal uint64         `json:"last_cpu_total"`
	LastCPUIdle  uint64         `json:"last_cpu_idle"`
	CPUPcts      []float64      `json:"cpu_pcts"`
}

func combined(rx, tx uint64, mode string) uint64 {
	switch mode {
	case "out":
		return tx
	case "in":
		return rx
	case "max":
		if rx > tx {
			return rx
		}
		return tx
	default: // both
		return rx + tx
	}
}

// cycleStartFor returns 00:00 local on the most recent reset day <= now.
func cycleStartFor(now time.Time, resetDay int) time.Time {
	if resetDay < 1 {
		resetDay = 1
	}
	if resetDay > 28 {
		resetDay = 28
	}
	loc := now.Location()
	cand := time.Date(now.Year(), now.Month(), resetDay, 0, 0, 0, 0, loc)
	if cand.After(now) {
		cand = cand.AddDate(0, -1, 0)
	}
	return cand
}

func (a *agentState) save(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	b, err := json.Marshal(a)
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func loadState(path string) (*agentState, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var s agentState
	if err := json.Unmarshal(b, &s); err != nil {
		return nil, err
	}
	return &s, nil
}

// sample takes one measurement: updates lifetime counters, cycle
// rollover, CPU history and the per-minute sample ring.
func (a *agentState) sample(cfg *ClientConfig, now time.Time) {
	dev := readNetDev()
	ifaces := pickInterfaces(cfg, dev)
	raw := sumCounters(dev, ifaces)

	// interface set changed (e.g. default route moved): re-baseline raw
	// counters so the delta is not garbage.
	if !sameStrings(ifaces, a.Ifaces) {
		a.Ifaces = ifaces
		a.LastRawRX, a.LastRawTX = raw.RX, raw.TX
	}

	boot := readBootID()
	if boot != "" && a.BootID != "" && boot != a.BootID {
		// rebooted: counters restarted at zero, the whole current
		// counter value is the delta since boot.
		a.LifeRX += raw.RX
		a.LifeTX += raw.TX
	} else {
		if raw.RX >= a.LastRawRX {
			a.LifeRX += raw.RX - a.LastRawRX
		} else {
			a.LifeRX += raw.RX // counter reset without reboot
		}
		if raw.TX >= a.LastRawTX {
			a.LifeTX += raw.TX - a.LastRawTX
		} else {
			a.LifeTX += raw.TX
		}
	}
	if boot != "" {
		a.BootID = boot
	}
	a.LastRawRX, a.LastRawTX = raw.RX, raw.TX

	// cycle rollover
	key := cycleStartFor(now, cfg.ResetDay).Format("2006-01-02")
	if a.CycleKey != key {
		a.CycleKey = key
		a.SeedBytes = 0
		a.CycleStartRX = a.LifeRX
		a.CycleStartTX = a.LifeTX
	}

	// CPU percentage since the previous sample
	if total, idle, ok := readCPUTimes(); ok {
		if a.LastCPUTotal > 0 && total > a.LastCPUTotal {
			dt := float64(total - a.LastCPUTotal)
			di := float64(idle - a.LastCPUIdle)
			pct := (1 - di/dt) * 100
			if pct < 0 {
				pct = 0
			}
			if pct > 100 {
				pct = 100
			}
			a.CPUPcts = append(a.CPUPcts, pct)
			if len(a.CPUPcts) > 5 {
				a.CPUPcts = a.CPUPcts[len(a.CPUPcts)-5:]
			}
		}
		a.LastCPUTotal, a.LastCPUIdle = total, idle
	}

	a.Samples = append(a.Samples, minuteSample{T: now, LifeRX: a.LifeRX, LifeTX: a.LifeTX})
	if len(a.Samples) > 15 {
		a.Samples = a.Samples[len(a.Samples)-15:]
	}
}

func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	set := map[string]bool{}
	for _, s := range a {
		set[s] = true
	}
	for _, s := range b {
		if !set[s] {
			return false
		}
	}
	return true
}

func (a *agentState) buildReport(cfg *ClientConfig, now time.Time) Report {
	mode := cfg.TrafficMode
	cycleRX := a.LifeRX - a.CycleStartRX
	cycleTX := a.LifeTX - a.CycleStartTX
	cycleUsedB := a.SeedBytes + float64(combined(cycleRX, cycleTX, mode))

	// traffic in the last ~5 minutes, from the sample ring
	var dRX, dTX uint64
	windowSec := 0
	if len(a.Samples) > 0 {
		target := now.Add(-5 * time.Minute)
		base := a.Samples[0]
		for _, s := range a.Samples {
			if s.T.Before(target) {
				base = s
			} else {
				break
			}
		}
		dRX = a.LifeRX - base.LifeRX
		dTX = a.LifeTX - base.LifeTX
		windowSec = int(now.Sub(base.T).Seconds())
	}

	rep := Report{
		Node:      cfg.NodeName,
		Hostname:  readHostname(),
		Version:   version,
		Time:      now.Format(time.RFC3339),
		UptimeSec: readUptimeSec(),
		Kernel:    readKernel(),
		OS:        readOSName(),
		Load1:     readLoad1(),
	}
	if n := len(a.CPUPcts); n > 0 {
		rep.CPUPct = a.CPUPcts[n-1]
		var sum float64
		for _, p := range a.CPUPcts {
			sum += p
		}
		rep.CPUPct5m = sum / float64(n)
	}
	if tot, avail, ok := readMem(); ok {
		rep.MemTotalG = float64(tot) / bytesPerG
		rep.MemUsedG = float64(tot-avail) / bytesPerG
		if tot > 0 {
			rep.MemPct = float64(tot-avail) / float64(tot) * 100
		}
	}
	if tot, used, ok := diskUsage("/"); ok {
		rep.DiskTotalG = float64(tot) / bytesPerG
		rep.DiskUsedG = float64(used) / bytesPerG
		if tot > 0 {
			rep.DiskPct = float64(used) / float64(tot) * 100
		}
	}
	cycleStart := cycleStartFor(now, cfg.ResetDay)
	tr := TrafficReport{
		QuotaG:          cfg.QuotaG,
		CycleUsedG:      cycleUsedB / bytesPerG,
		CycleRXG:        float64(cycleRX) / bytesPerG,
		CycleTXG:        float64(cycleTX) / bytesPerG,
		Last5mG:         float64(combined(dRX, dTX, mode)) / bytesPerG,
		Last5mRXG:       float64(dRX) / bytesPerG,
		Last5mTXG:       float64(dTX) / bytesPerG,
		Last5mWindowSec: windowSec,
		TotalG:          float64(combined(a.LifeRX, a.LifeTX, mode)) / bytesPerG,
		ResetDay:        cfg.ResetDay,
		NextReset:       cycleStart.AddDate(0, 1, 0).Format("2006-01-02"),
		Expiry:          cfg.Expiry,
	}
	tr.RemainingG = tr.QuotaG - tr.CycleUsedG
	if tr.RemainingG < 0 {
		tr.RemainingG = 0
	}
	if cfg.Expiry != "" {
		if exp, err := time.ParseInLocation("2006-01-02", cfg.Expiry, now.Location()); err == nil {
			today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
			d := int(exp.Sub(today).Hours() / 24)
			tr.DaysLeft = &d
		}
	}
	rep.Traffic = tr
	return rep
}

func sendReport(cfg *ClientConfig, rep Report) error {
	b, err := json.Marshal(rep)
	if err != nil {
		return err
	}
	url := cfg.ServerURL + "/api/report"
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+cfg.Token)
	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("server returned %s", resp.Status)
	}
	return nil
}

// ---------- agent lifecycle (start/stop under the daemon) ----------

type agentStatus struct {
	mu           sync.RWMutex
	Running      bool
	LastReportAt time.Time
	LastError    string
	LastReport   *Report
}

func (s *agentStatus) setReport(rep Report, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := rep
	s.LastReport = &cp
	s.LastReportAt = time.Now()
	if err != nil {
		s.LastError = err.Error()
	} else {
		s.LastError = ""
	}
}

func (s *agentStatus) snapshot() (running bool, lastAt time.Time, lastErr string, last *Report) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.Running, s.LastReportAt, s.LastError, s.LastReport
}

func (s *agentStatus) setRunning(v bool) {
	s.mu.Lock()
	s.Running = v
	s.mu.Unlock()
}

// runAgent runs the client agent until ctx is cancelled. selfSink, when
// non-nil, receives every built report so the local WebUI can show this
// node even when it reports to a remote server.
func runAgent(ctx context.Context, cfg ClientConfig, stateFile string, status *agentStatus, selfSink func(Report)) {
	status.setRunning(true)
	defer status.setRunning(false)

	now := time.Now()
	state, err := loadState(stateFile)
	if err != nil {
		// first run: seed the current cycle with the configured
		// already-used figure; it is stored in the state file so
		// later config edits never apply it twice.
		state = &agentState{
			BootID:    readBootID(),
			SeedBytes: cfg.InitialUsedG * bytesPerG,
			CycleKey:  cycleStartFor(now, cfg.ResetDay).Format("2006-01-02"),
		}
		dev := readNetDev()
		ifaces := pickInterfaces(&cfg, dev)
		raw := sumCounters(dev, ifaces)
		state.Ifaces = ifaces
		state.LastRawRX, state.LastRawTX = raw.RX, raw.TX
		if total, idle, ok := readCPUTimes(); ok {
			state.LastCPUTotal, state.LastCPUIdle = total, idle
		}
		log.Printf("vpsmon: agent first run for node %q, seeding cycle with %.2f G already used", cfg.NodeName, cfg.InitialUsedG)
	}
	state.sample(&cfg, now)
	if err := state.save(stateFile); err != nil {
		log.Printf("vpsmon: saving state: %v", err)
	}
	report := func(t time.Time) {
		rep := state.buildReport(&cfg, t)
		err := sendReport(&cfg, rep)
		status.setReport(rep, err)
		if selfSink != nil {
			selfSink(rep)
		}
		if err != nil {
			log.Printf("vpsmon: report failed: %v", err)
		}
	}
	report(time.Now())

	sampleTicker := time.NewTicker(time.Duration(cfg.SampleIntervalSec) * time.Second)
	reportTicker := time.NewTicker(time.Duration(cfg.ReportIntervalSec) * time.Second)
	defer sampleTicker.Stop()
	defer reportTicker.Stop()
	for {
		select {
		case <-ctx.Done():
			// one last sample so the state file is current
			state.sample(&cfg, time.Now())
			_ = state.save(stateFile)
			return
		case t := <-sampleTicker.C:
			state.sample(&cfg, t)
			if err := state.save(stateFile); err != nil {
				log.Printf("vpsmon: saving state: %v", err)
			}
		case t := <-reportTicker.C:
			report(t)
		}
	}
}
