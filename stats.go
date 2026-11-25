package main

import (
	"bufio"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"syscall"
)

// ---------- /proc readers (Linux) ----------

func readBootID() string {
	b, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

func readHostname() string {
	h, err := os.Hostname()
	if err != nil {
		return ""
	}
	return h
}

func readKernel() string {
	b, err := os.ReadFile("/proc/sys/kernel/osrelease")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

func readOSName() string {
	f, err := os.Open("/etc/os-release")
	if err != nil {
		return ""
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, "PRETTY_NAME=") {
			return strings.Trim(strings.TrimPrefix(line, "PRETTY_NAME="), "\"")
		}
	}
	return ""
}

func readUptimeSec() float64 {
	b, err := os.ReadFile("/proc/uptime")
	if err != nil {
		return 0
	}
	f := strings.Fields(string(b))
	if len(f) == 0 {
		return 0
	}
	v, _ := strconv.ParseFloat(f[0], 64)
	return v
}

func readLoad1() float64 {
	b, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return 0
	}
	f := strings.Fields(string(b))
	if len(f) == 0 {
		return 0
	}
	v, _ := strconv.ParseFloat(f[0], 64)
	return v
}

// cpuTimes returns total and idle jiffies from the aggregate cpu line.
func readCPUTimes() (total, idle uint64, ok bool) {
	f, err := os.Open("/proc/stat")
	if err != nil {
		return 0, 0, false
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "cpu ") {
			continue
		}
		fields := strings.Fields(line)[1:]
		var vals []uint64
		for _, s := range fields {
			v, err := strconv.ParseUint(s, 10, 64)
			if err != nil {
				return 0, 0, false
			}
			vals = append(vals, v)
		}
		if len(vals) < 5 {
			return 0, 0, false
		}
		for _, v := range vals {
			total += v
		}
		idle = vals[3] + vals[4] // idle + iowait
		return total, idle, true
	}
	return 0, 0, false
}

// memInfo returns MemTotal and MemAvailable in bytes.
func readMem() (totalB, availB uint64, ok bool) {
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return 0, 0, false
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	var found int
	for sc.Scan() {
		line := sc.Text()
		for _, key := range []string{"MemTotal:", "MemAvailable:"} {
			if strings.HasPrefix(line, key) {
				fields := strings.Fields(line)
				if len(fields) >= 2 {
					v, err := strconv.ParseUint(fields[1], 10, 64) // kB
					if err == nil {
						if key == "MemTotal:" {
							totalB = v * 1024
						} else {
							availB = v * 1024
						}
						found++
					}
				}
			}
		}
	}
	return totalB, availB, found == 2
}

func diskUsage(path string) (totalB, usedB uint64, ok bool) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, 0, false
	}
	totalB = st.Blocks * uint64(st.Bsize)
	freeB := st.Bavail * uint64(st.Bsize)
	if totalB < freeB {
		return 0, 0, false
	}
	return totalB, totalB - freeB, true
}

// ---------- network interfaces ----------

type netCounters struct {
	RX uint64
	TX uint64
}

// readNetDev parses /proc/net/dev into per-interface byte counters.
func readNetDev() map[string]netCounters {
	out := map[string]netCounters{}
	f, err := os.Open("/proc/net/dev")
	if err != nil {
		return out
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		i := strings.Index(line, ":")
		if i < 0 {
			continue
		}
		name := strings.TrimSpace(line[:i])
		fields := strings.Fields(line[i+1:])
		if len(fields) < 16 {
			continue
		}
		rx, err1 := strconv.ParseUint(fields[0], 10, 64)
		tx, err2 := strconv.ParseUint(fields[8], 10, 64)
		if err1 != nil || err2 != nil {
			continue
		}
		out[name] = netCounters{RX: rx, TX: tx}
	}
	return out
}

// defaultInterface finds the interface holding the IPv4 or IPv6 default route.
func defaultInterface() string {
	for _, path := range []string{"/proc/net/route", "/proc/net/ipv6_route"} {
		f, err := os.Open(path)
		if err != nil {
			continue
		}
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			fields := strings.Fields(sc.Text())
			if path == "/proc/net/route" {
				// Iface Destination Gateway ... ; default = 00000000
				if len(fields) >= 2 && fields[1] == "00000000" {
					f.Close()
					return fields[0]
				}
			} else {
				// ipv6_route: dest(32 hex) plen ... iface is last field; default = all zero, plen 00
				if len(fields) >= 10 && fields[0] == "00000000000000000000000000000000" && fields[1] == "00" {
					f.Close()
					return fields[len(fields)-1]
				}
			}
		}
		f.Close()
	}
	return ""
}

var virtualPrefixes = []string{"lo", "docker", "veth", "br-", "virbr", "tun", "tap", "wg", "vnet"}

func isVirtualIface(name string) bool {
	for _, p := range virtualPrefixes {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	return false
}

// ifaceInfo describes one network interface for the client settings page.
type ifaceInfo struct {
	Name    string
	Label   string
	Default bool
	Virtual bool
}

// listInterfaces enumerates the machine's interfaces with their cumulative
// counters (default-route interface first) for the settings dropdown.
func listInterfaces() []ifaceInfo {
	dev := readNetDev()
	def := defaultInterface()
	names := make([]string, 0, len(dev))
	for n := range dev {
		names = append(names, n)
	}
	sort.Slice(names, func(i, j int) bool {
		if names[i] == def {
			return true
		}
		if names[j] == def {
			return false
		}
		return names[i] < names[j]
	})
	out := make([]ifaceInfo, 0, len(names))
	for _, n := range names {
		c := dev[n]
		label := fmt.Sprintf("%s（累计 ↓ %.2f G / ↑ %.2f G", n,
			float64(c.RX)/float64(bytesPerG), float64(c.TX)/float64(bytesPerG))
		if n == def {
			label += " · 默认路由"
		}
		if isVirtualIface(n) {
			label += " · 虚拟网卡"
		}
		label += "）"
		out = append(out, ifaceInfo{Name: n, Label: label, Default: n == def, Virtual: isVirtualIface(n)})
	}
	return out
}

// pickInterfaces resolves the configured interface selection to a list of
// real interfaces to sum. "auto" = the default-route interface; if that
// cannot be determined, all non-virtual interfaces.
func pickInterfaces(cfg *ClientConfig, dev map[string]netCounters) []string {
	if cfg.Interface != "auto" && cfg.Interface != "" {
		if _, ok := dev[cfg.Interface]; ok {
			return []string{cfg.Interface}
		}
		return nil
	}
	if d := defaultInterface(); d != "" {
		if _, ok := dev[d]; ok {
			return []string{d}
		}
	}
	var names []string
	for name := range dev {
		skip := false
		for _, p := range virtualPrefixes {
			if strings.HasPrefix(name, p) {
				skip = true
				break
			}
		}
		if !skip {
			names = append(names, name)
		}
	}
	return names
}

func sumCounters(dev map[string]netCounters, ifaces []string) netCounters {
	var s netCounters
	for _, n := range ifaces {
		if c, ok := dev[n]; ok {
			s.RX += c.RX
			s.TX += c.TX
		}
	}
	return s
}
