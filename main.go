package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/moby/moby/client"
	"github.com/shirou/gopsutil/v3/cpu"
	"github.com/shirou/gopsutil/v3/disk"
	"github.com/shirou/gopsutil/v3/mem"
	"github.com/shirou/gopsutil/v3/net"
)

// --- Types ---

type Anomaly struct {
	Type   string `json:"type"`
	Target string `json:"target"`
	Reason string `json:"reason"`
}

type Decision struct {
	Type   string `json:"type"`
	Target string `json:"target"`
	Reason string `json:"reason"`
	Time   int64  `json:"timestamp"`
}

type HealthReport struct {
	Score     int              `json:"score"`
	Mode      string           `json:"mode"`
	Verdict   string           `json:"verdict"`
	Issues    []string         `json:"issues"`
	Uptime    string           `json:"uptime"`
	Timestamp int64            `json:"timestamp"`
	Agents    map[string]Agent `json:"agent_health"`
}

type Agent struct {
	Status    string `json:"status"`
	Timestamp int64  `json:"timestamp"`
}

type M3talState struct {
	dirty      bool
	System     SystemMetrics
	Network    NetworkMetrics
	CPU        float64
	Timestamp  int64
	Storage    StorageStats
	GPU        GpuStats
	Temp       TempStats
	Anomalies  []Anomaly
	Decisions  []Decision
	Health     HealthReport
	Cooldowns  map[string]time.Time
	Containers []ContainerMetric
	MutedUntil int64
	NetworkL   []NetworkRoute

	mu sync.RWMutex
}

type NetworkRoute struct {
	Name      string `json:"name"`
	URL       string `json:"url"`
	Status    string `json:"status"`
	Image     string `json:"image"`
	Icon      string `json:"icon"`
	Container string `json:"container"`
}

type StorageStats struct {
	Disks     map[string]DiskInfo `json:"disks"`
	IO        *DiskIO             `json:"io"`
	Timestamp int64               `json:"timestamp"`
	Status    string              `json:"status"`
}

type DiskInfo struct {
	Free    string  `json:"free"`
	Temp    float64 `json:"temp"`
	Percent float64 `json:"percent"`
}

type DiskIO struct {
	ReadCount  uint64 `json:"read_count"`
	WriteCount uint64 `json:"write_count"`
	ReadBytes  uint64 `json:"read_bytes"`
	WriteBytes uint64 `json:"write_bytes"`
}

type GpuStats struct {
	Name     string  `json:"name"`
	Temp     float64 `json:"temp"`
	Load     int     `json:"load"`
	MemUsed  int     `json:"mem_used"`
	MemTotal int     `json:"mem_total"`
	Active   bool    `json:"active"`
}

type TempStats struct {
	CPUTemp   float64 `json:"cpu_temp"`
	GPUTemp   float64 `json:"gpu_temp"`
	Timestamp int64   `json:"timestamp"`
	Status    string  `json:"status"`
}

type SystemMetrics struct {
	CPU       float64 `json:"cpu"`
	Mem       float64 `json:"mem"`
	MemGB     float64 `json:"mem_gb"`
	MemTotal  float64 `json:"mem_total"`
	Timestamp int64   `json:"timestamp"`
}

type TelegramUpdate struct {
	UpdateID int `json:"update_id"`
	Message  *struct {
		Chat struct {
			ID int64 `json:"id"`
		} `json:"chat"`
		From struct {
			ID int64 `json:"id"`
		} `json:"from"`
		Text string `json:"text"`
	} `json:"message"`
}

type ContainerMetric struct {
	Name    string  `json:"name"`
	CPU     float64 `json:"cpu"`
	Mem     float64 `json:"mem"`
	Status  string  `json:"status"`
	State   string  `json:"state"`
	Managed bool    `json:"managed"`
}

type NetworkMetrics struct {
	Down string  `json:"down"`
	Up   string  `json:"up"`
	Load float64 `json:"load"`
}

// --- Persistence ---

func (s *M3talState) Save(stateDir string) {
	s.mu.Lock()
	if !s.dirty {
		s.mu.Unlock()
		return
	}
	s.dirty = false
	s.mu.Unlock()

	s.mu.RLock()
	defer s.mu.RUnlock()

	saveAtomic(filepath.Join(stateDir, "metrics.json"), s.System)
	saveAtomic(filepath.Join(stateDir, "storage.json"), s.Storage)
	saveAtomic(filepath.Join(stateDir, "gpu.json"), s.GPU)
	saveAtomic(filepath.Join(stateDir, "temp.json"), s.Temp)
	saveAtomic(filepath.Join(stateDir, "network.json"), map[string]interface{}{"metrics": s.Network, "links": s.NetworkL})
	saveAtomic(filepath.Join(stateDir, "anomalies.json"), map[string]interface{}{"issues": s.Anomalies})
	saveAtomic(filepath.Join(stateDir, "decisions.json"), map[string]interface{}{"actions": s.Decisions})
	saveAtomic(filepath.Join(stateDir, "health.json"), map[string]interface{}{
		"status":     "online",
		"containers": s.Containers,
		"timestamp":  s.Timestamp,
	})
	saveAtomic(filepath.Join(stateDir, "health_report.json"), s.Health)
}

func saveAtomic(path string, data interface{}) {
	tmp := path + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return
	}
	defer f.Close()

	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	if err := enc.Encode(data); err != nil {
		return
	}
	f.Close()
	os.Rename(tmp, path)
}

// --- Main ---

func main() {
	state := &M3talState{
		Cooldowns:  make(map[string]time.Time),
		Containers: []ContainerMetric{},
	}

	stateDir := os.Getenv("STATE_DIR")
	if stateDir == "" {
		stateDir = filepath.Join("..", "state")
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	fmt.Println("🚀 M3TAL Go Backend (Linux-Ready) starting...")

	// Launch Agents
	go metricsAgent(state)
	go dockerAgent(ctx, state)
	go hardwareAgent(state)
	go storageAgent(state)
	go scoutAgent(ctx, state)
	go listenerAgent(ctx, state)
	go orchestratorAgent(ctx, state)
	go logObserverAgent(ctx)
	go healerAgent(ctx, state)
	go notifyAgent(state)
	go saveAgent(ctx, state, stateDir)

	select {}
}

// --- Agents ---

func saveAgent(ctx context.Context, s *M3talState, stateDir string) {
	ticker := time.NewTicker(2 * time.Second)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.Save(stateDir)
		}
	}
}

func metricsAgent(s *M3talState) {
	ticker := time.NewTicker(2 * time.Second)
	var lastRecv, lastSent uint64
	var lastTime time.Time

	for range ticker.C {
		now := time.Now()
		cpuPerc, _ := cpu.Percent(0, false)
		vm, _ := mem.VirtualMemory()
		netIO, _ := net.IOCounters(false)

		cpuVal := 0.0
		if len(cpuPerc) > 0 {
			cpuVal = cpuPerc[0]
		}

		var totalRecv, totalSent uint64
		for _, io := range netIO {
			totalRecv += io.BytesRecv
			totalSent += io.BytesSent
		}

		var down, up, load float64
		if !lastTime.IsZero() {
			dt := now.Sub(lastTime).Seconds()
			if dt > 0 {
				down = float64(totalRecv-lastRecv) / (1024 * 1024) / dt
				up = float64(totalSent-lastSent) / (1024 * 1024) / dt
				capacity := 125.0
				load = ((down + up) / capacity) * 100
				if load > 100 {
					load = 100
				}
			}
		}
		lastRecv = totalRecv
		lastSent = totalSent
		lastTime = now

		s.mu.Lock()
		s.System = SystemMetrics{
			CPU:       cpuVal,
			Mem:       vm.UsedPercent,
			MemGB:     float64(vm.Used) / (1024 * 1024 * 1024),
			MemTotal:  float64(vm.Total) / (1024 * 1024 * 1024),
			Timestamp: now.Unix(),
		}
		s.Network = NetworkMetrics{
			Down: formatSpeed(down * 1024 * 1024),
			Up:   formatSpeed(up * 1024 * 1024),
			Load: load,
		}
		s.CPU = cpuVal
		s.Timestamp = now.Unix()
		s.dirty = true
		s.mu.Unlock()
	}
}

func formatSpeed(bytesPerSec float64) string {
	if bytesPerSec < 1024 {
		return fmt.Sprintf("%.0f B/s", bytesPerSec)
	} else if bytesPerSec < 1024*1024 {
		return fmt.Sprintf("%.1f KB/s", bytesPerSec/1024)
	} else if bytesPerSec < 1024*1024*1024 {
		return fmt.Sprintf("%.1f MB/s", bytesPerSec/(1024*1024))
	}
	return fmt.Sprintf("%.1f GB/s", bytesPerSec/(1024*1024*1024))
}

func dockerAgent(ctx context.Context, s *M3talState) {
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return
	}
	ticker := time.NewTicker(5 * time.Second)
	for range ticker.C {
		res, err := cli.ContainerList(ctx, client.ContainerListOptions{All: true})
		if err != nil {
			continue
		}
		newStats := []ContainerMetric{}
		for _, c := range res.Items {
			name := "unknown"
			if len(c.Names) > 0 {
				name = c.Names[0][1:]
			}
			managed := false
			if _, ok := c.Labels["m3tal.managed"]; ok {
				managed = true
			} else if _, ok := c.Labels["m3tal.stack"]; ok {
				managed = true
			}
			newStats = append(newStats, ContainerMetric{
				Name:    name,
				CPU:     0.0,
				Mem:     0.0,
				Status:  c.Status,
				State:   string(c.State),
				Managed: managed,
			})
		}
		s.mu.Lock()
		s.Containers = newStats
		s.dirty = true
		s.mu.Unlock()
	}
}

func hardwareAgent(s *M3talState) {
	ticker := time.NewTicker(10 * time.Second)
	gpuRe := regexp.MustCompile(`gpu\s+([\d\.]+)%`)
	vramRe := regexp.MustCompile(`vram\s+[\d\.]+% ([\d\.]+)mb`)

	for range ticker.C {
		var cpuT, gpuT float64
		var gpuStats GpuStats

		for i := 0; i < 8; i++ {
			path := fmt.Sprintf("/sys/class/thermal/thermal_zone%d/temp", i)
			if data, err := os.ReadFile(path); err == nil {
				if val, err := strconv.Atoi(strings.TrimSpace(string(data))); err == nil {
					cpuT = float64(val) / 1000.0
					if cpuT > 20 && cpuT < 110 {
						break
					}
				}
			}
			hwPath := fmt.Sprintf("/sys/class/hwmon/hwmon%d/temp1_input", i)
			if data, err := os.ReadFile(hwPath); err == nil {
				if val, err := strconv.Atoi(strings.TrimSpace(string(data))); err == nil {
					cpuT = float64(val) / 1000.0
					if cpuT > 20 && cpuT < 110 {
						break
					}
				}
			}
		}

		paths := []string{
			"/sys/class/drm/card0/device/hwmon/hwmon0/temp1_input",
			"/sys/class/drm/card0/device/hwmon/hwmon1/temp1_input",
			"/sys/class/drm/card0/device/hwmon/hwmon2/temp1_input",
		}
		for _, p := range paths {
			if data, err := os.ReadFile(p); err == nil {
				if val, err := strconv.Atoi(strings.TrimSpace(string(data))); err == nil {
					gpuT = float64(val) / 1000.0
					break
				}
			}
		}

		gpuStats.Name = "AMD Radeon HD 5770"
		gpuStats.Temp = gpuT
		gpuStats.MemTotal = 1024

		radeontopFound := false
		if _, err := exec.LookPath("radeontop"); err == nil {
			cmd := exec.Command("radeontop", "-d", "-", "-l", "1")
			if output, err := cmd.CombinedOutput(); err == nil {
				line := string(output)
				radeontopFound = true
				gpuStats.Active = true
				if m := gpuRe.FindStringSubmatch(line); len(m) > 1 {
					if f, err := strconv.ParseFloat(m[1], 64); err == nil {
						gpuStats.Load = int(f)
					}
				}
				if m := vramRe.FindStringSubmatch(line); len(m) > 1 {
					if f, err := strconv.ParseFloat(m[1], 64); err == nil {
						gpuStats.MemUsed = int(f)
					}
				}
			}
		}

		if !radeontopFound {
			if data, err := os.ReadFile("/sys/class/drm/card0/device/gpu_busy_percent"); err == nil {
				if val, err := strconv.Atoi(strings.TrimSpace(string(data))); err == nil {
					gpuStats.Load = val
					gpuStats.Active = true
				}
			}
			if data, err := os.ReadFile("/sys/class/drm/card0/device/mem_info_vram_used"); err == nil {
				if val, err := strconv.ParseUint(strings.TrimSpace(string(data)), 10, 64); err == nil {
					gpuStats.MemUsed = int(val / (1024 * 1024))
					gpuStats.Active = true
				}
			}
			if data, err := os.ReadFile("/sys/class/drm/card0/device/mem_info_vram_total"); err == nil {
				if val, err := strconv.ParseUint(strings.TrimSpace(string(data)), 10, 64); err == nil {
					gpuStats.MemTotal = int(val / (1024 * 1024))
				}
			}
		}

		status := "healthy"
		if cpuT > 85 || gpuT > 85 {
			status = "critical"
		} else if cpuT > 75 || gpuT > 75 {
			status = "warning"
		}

		s.mu.Lock()
		s.Temp = TempStats{
			CPUTemp:   cpuT,
			GPUTemp:   gpuT,
			Timestamp: time.Now().Unix(),
			Status:    status,
		}
		s.GPU = gpuStats
		s.dirty = true
		s.mu.Unlock()
	}
}

func storageAgent(s *M3talState) {
	ticker := time.NewTicker(30 * time.Second)
	smartRe := regexp.MustCompile(`(?i)(?:Temperature_Celsius|Airflow_Temperature_Cel|Composite\s+Temperature|Current\s+Drive\s+Temperature:).*?(\d+)`)

	for range ticker.C {
		disks := make(map[string]DiskInfo)
		highestUsage := 0.0

		parts, _ := disk.Partitions(false)
		for _, p := range parts {
			if strings.HasPrefix(p.Mountpoint, "/proc") || strings.HasPrefix(p.Mountpoint, "/dev") || strings.HasPrefix(p.Mountpoint, "/sys") {
				continue
			}
			usage, err := disk.Usage(p.Mountpoint)
			if err != nil {
				continue
			}
			label := p.Mountpoint
			if label == "/" {
				label = "System"
			} else {
				label = filepath.Base(label)
			}

			var driveT float64
			dev := p.Device
			if strings.HasPrefix(dev, "/dev/") {
				if _, err := exec.LookPath("smartctl"); err == nil {
					phys := dev
					if len(dev) > 8 && (dev[7] >= '0' && dev[7] <= '9') {
						phys = dev[:7]
					}
					cmd := exec.Command("smartctl", "-a", phys)
					if output, err := cmd.CombinedOutput(); err == nil {
						if m := smartRe.FindStringSubmatch(string(output)); len(m) > 1 {
							if f, err := strconv.ParseFloat(m[1], 64); err == nil {
								driveT = f
							}
						}
					}
				}
			}

			disks[label] = DiskInfo{
				Free:    fmt.Sprintf("%.1f", float64(usage.Free)/(1024*1024*1024)),
				Temp:    driveT,
				Percent: usage.UsedPercent,
			}
			if usage.UsedPercent > highestUsage {
				highestUsage = usage.UsedPercent
			}
		}

		var ioStats *DiskIO
		if io, err := disk.IOCounters(); err == nil {
			var rC, wC, rB, wB uint64
			for _, stats := range io {
				rC += stats.ReadCount
				wC += stats.WriteCount
				rB += stats.ReadBytes
				wB += stats.WriteBytes
			}
			ioStats = &DiskIO{
				ReadCount:  rC,
				WriteCount: wC,
				ReadBytes:  rB,
				WriteBytes: wB,
			}
		}

		status := "healthy"
		if highestUsage > 95 {
			status = "critical"
		} else if highestUsage > 85 {
			status = "warning"
		}

		s.mu.Lock()
		s.Storage = StorageStats{
			Disks:     disks,
			IO:        ioStats,
			Timestamp: time.Now().Unix(),
			Status:    status,
		}
		s.dirty = true
		s.mu.Unlock()
	}
}

func scoutAgent(ctx context.Context, s *M3talState) {
	cli, _ := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	ticker := time.NewTicker(60 * time.Second)
	hostRe := regexp.MustCompile(`Host\(` + "`" + `([^` + "`" + `]+)` + "`" + `\)`)

	hc := &http.Client{
		Timeout: 3 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		},
	}

	for range ticker.C {
		res, err := cli.ContainerList(ctx, client.ContainerListOptions{})
		if err != nil {
			continue
		}

		links := []NetworkRoute{}
		seen := make(map[string]bool)
		blacklist := []string{"dashboard", "api", "traefik", "m3tal"}

		for _, c := range res.Items {
			labels := ""
			for k, v := range c.Labels {
				labels += k + "=" + v + ","
			}

			if m := hostRe.FindStringSubmatch(labels); len(m) > 1 {
				host := m[1]
				if seen[host] {
					continue
				}

				serviceKey := strings.ToLower(strings.Split(host, ".")[0])
				readableName := strings.ReplaceAll(serviceKey, "-", " ")

				skip := false
				for _, b := range blacklist {
					if strings.Contains(strings.ToLower(readableName), b) {
						skip = true
						break
					}
				}
				if skip {
					continue
				}

				targetURL := "https://" + host
				status := "enabled"
				if resp, err := hc.Head(targetURL); err == nil {
					if resp.StatusCode >= 500 {
						status = "disabled"
					}
					resp.Body.Close()
				} else {
					status = "disabled"
				}

				links = append(links, NetworkRoute{
					Name:      readableName,
					URL:       targetURL,
					Status:    status,
					Image:     c.Image,
					Icon:      fmt.Sprintf("https://cdn.jsdelivr.net/gh/walkxcode/dashboard-icons/png/%s.png", serviceKey),
					Container: c.Names[0][1:],
				})
				seen[host] = true
			}
		}

		s.mu.Lock()
		s.NetworkL = links
		s.dirty = true
		s.mu.Unlock()
	}
}

func logObserverAgent(ctx context.Context) {
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return
	}

	secrets := []string{"TOKEN", "SECRET", "KEY", "PASSWORD"}

	activeLogs := make(map[string]context.CancelFunc)
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			for _, cancel := range activeLogs {
				cancel()
			}
			return
		case <-ticker.C:
			res, err := cli.ContainerList(ctx, client.ContainerListOptions{})
			if err != nil {
				continue
			}

			currentContainers := make(map[string]bool)
			for _, c := range res.Items {
				currentContainers[c.ID] = true
				if _, exists := activeLogs[c.ID]; !exists {
					logCtx, cancel := context.WithCancel(ctx)
					activeLogs[c.ID] = cancel

					go func(cCtx context.Context, containerID string, name string) {
						options := client.ContainerLogsOptions{ShowStdout: true, ShowStderr: true, Follow: true, Tail: "0"}
						out, err := cli.ContainerLogs(cCtx, containerID, options)
						if err != nil {
							return
						}
						defer out.Close()

						lines := make(chan string)
						go func() {
							scanner := bufio.NewScanner(out)
							for scanner.Scan() {
								lines <- scanner.Text()
							}
							close(lines)
						}()

						for {
							select {
							case <-cCtx.Done():
								return
							case line, ok := <-lines:
								if !ok {
									return
								}
								lLine := strings.ToLower(line)
								if strings.Contains(lLine, "error") || strings.Contains(lLine, "panic") || strings.Contains(lLine, "fatal") || strings.Contains(lLine, "fail") {
									for _, s := range secrets {
										if strings.Contains(strings.ToUpper(line), s) {
											line = "[REDACTED LOG ENTRY]"
											break
										}
									}

									token := os.Getenv("TELEGRAM_BOT_TOKEN")
									chat := os.Getenv("TG_ALERT_CHAT_ID")
									if token != "" && chat != "" {
										msg := fmt.Sprintf("⚠️ <b>Log Alert [%s]</b>\n<code>%s</code>", name, line)
										sendTelegram(token, chat, msg)
									}
								}
							}
						}
					}(logCtx, c.ID, c.Names[0][1:])
				}
			}

			for id, cancel := range activeLogs {
				if !currentContainers[id] {
					cancel()
					delete(activeLogs, id)
				}
			}
		}
	}
}

func orchestratorAgent(ctx context.Context, s *M3talState) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			analyzeSystem(s)
		}
	}
}

func analyzeSystem(s *M3talState) {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now()
	score := 100
	var issues []string
	var anomalies []Anomaly
	var decisions []Decision

	if s.Temp.CPUTemp > 85 {
		score -= 20
		msg := fmt.Sprintf("CPU Thermal Critical: %.1f°C", s.Temp.CPUTemp)
		issues = append(issues, msg)
		anomalies = append(anomalies, Anomaly{Type: "critical", Target: "host", Reason: msg})
	}
	if s.System.CPU > 95 {
		score -= 10
		msg := fmt.Sprintf("CPU Saturation: %.1f%%", s.System.CPU)
		issues = append(issues, msg)
		anomalies = append(anomalies, Anomaly{Type: "transient", Target: "host", Reason: msg})
	}

	for _, disk := range s.Storage.Disks {
		if disk.Percent > 95 {
			score -= 15
			msg := fmt.Sprintf("Disk Pressure: %.1f%%", disk.Percent)
			issues = append(issues, msg)
			anomalies = append(anomalies, Anomaly{Type: "critical", Target: "disk", Reason: msg})
		}
	}

	mode := "FULL"
	if score < 50 {
		mode = "CRITICAL"
	} else if score < 85 {
		mode = "DEGRADED"
	}

	s.Health = HealthReport{
		Score:     score,
		Mode:      mode,
		Verdict:   mode,
		Issues:    issues,
		Uptime:    getUptime(),
		Timestamp: now.Unix(),
		Agents: map[string]Agent{
			"orchestrator": {Status: "healthy", Timestamp: now.Unix()},
			"healer":       {Status: "healthy", Timestamp: now.Unix()},
			"metrics":      {Status: "healthy", Timestamp: now.Unix()},
			"hardware":     {Status: "healthy", Timestamp: now.Unix()},
		},
	}
	s.Anomalies = anomalies
	s.Decisions = decisions
	s.dirty = true
}

func getUptime() string {
	data, err := os.ReadFile("/proc/uptime")
	if err != nil {
		return "—"
	}
	parts := strings.Fields(string(data))
	if len(parts) == 0 {
		return "—"
	}
	sec, _ := strconv.ParseFloat(parts[0], 64)
	days := int(sec / 86400)
	hours := int(sec) / 3600 % 24
	if days > 0 {
		return fmt.Sprintf("%dd %dh", days, hours)
	}
	return fmt.Sprintf("%dh", hours)
}

func healerAgent(ctx context.Context, s *M3talState) {
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return
	}

	ticker := time.NewTicker(10 * time.Second)
	for range ticker.C {
		s.mu.RLock()
		containers := s.Containers
		s.mu.RUnlock()

		for _, c := range containers {
			if c.Managed && (c.State == "exited" || c.State == "dead") {
				log.Printf("🛡️ HEALER: Detected crashed container: %s. Restarting...", c.Name)

				_, err := cli.ContainerRestart(ctx, c.Name, client.ContainerRestartOptions{})
				if err != nil {
					log.Printf("❌ HEALER: Failed to restart %s: %v", c.Name, err)
					continue
				}

				s.mu.Lock()
				s.Decisions = append(s.Decisions, Decision{
					Type:   "restart",
					Target: c.Name,
					Reason: fmt.Sprintf("State was '%s'", c.State),
					Time:   time.Now().Unix(),
				})
				if len(s.Decisions) > 50 {
					s.Decisions = s.Decisions[1:]
				}
				s.dirty = true
				s.mu.Unlock()
			}
		}
	}
}

func notifyAgent(s *M3talState) {
	ticker := time.NewTicker(30 * time.Second)
	token := os.Getenv("TELEGRAM_BOT_TOKEN")
	chat := os.Getenv("TG_ALERT_CHAT_ID")
	if token == "" || chat == "" {
		return
	}

	lastScore := 100
	for range ticker.C {
		s.mu.RLock()
		currentScore := s.Health.Score
		issues := s.Health.Issues
		s.mu.RUnlock()

		if currentScore < lastScore && currentScore < 90 {
			msg := fmt.Sprintf("⚠️ <b>M3TAL Health Drop: %d%%</b>\n%s", currentScore, strings.Join(issues, "\n"))
			sendTelegram(token, chat, msg)
		}
		lastScore = currentScore
	}
}

func listenerAgent(ctx context.Context, s *M3talState) {
	token := os.Getenv("TELEGRAM_BOT_TOKEN")
	allowedUsers := os.Getenv("ALLOWED_USERS")
	if token == "" {
		return
	}

	cli, _ := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	offset := 0
	hc := &http.Client{Timeout: 35 * time.Second}

	for {
		select {
		case <-ctx.Done():
			return
		default:
			url := fmt.Sprintf("https://api.telegram.org/bot%s/getUpdates?offset=%d&timeout=30", token, offset)
			resp, err := hc.Get(url)
			if err != nil {
				time.Sleep(5 * time.Second)
				continue
			}

			var result struct {
				Ok     bool             `json:"ok"`
				Result []TelegramUpdate `json:"result"`
			}
			json.NewDecoder(resp.Body).Decode(&result)
			resp.Body.Close()

			for _, u := range result.Result {
				offset = u.UpdateID + 1
				if u.Message == nil {
					continue
				}

				uidStr := strconv.FormatInt(u.Message.From.ID, 10)
				allowed := false
				for _, uid := range strings.Split(allowedUsers, ",") {
					if strings.TrimSpace(uid) == uidStr {
						allowed = true
						break
					}
				}
				if !allowed {
					sendTelegram(token, strconv.FormatInt(u.Message.Chat.ID, 10), "⛔ <b>Unauthorized</b>")
					continue
				}

				parts := strings.Fields(u.Message.Text)
				if len(parts) == 0 {
					continue
				}
				cmd := strings.ToLower(parts[0])
				chatStr := strconv.FormatInt(u.Message.Chat.ID, 10)

				switch cmd {
				case "/status":
					s.mu.RLock()
					sys := s.System
					net := s.Network
					s.mu.RUnlock()
					msg := fmt.Sprintf("🏥 <b>M3TAL Status</b>\nCPU: <b>%.1f%%</b>\nRAM: <b>%.1f%%</b>\nNet: <b>%s</b>", sys.CPU, sys.Mem, net.Down)
					sendTelegram(token, chatStr, msg)

				case "/restart":
					if len(parts) < 2 {
						sendTelegram(token, chatStr, "❓ Usage: <code>/restart <name></code>")
						continue
					}
					target := parts[1]

					allowedRestarts := os.Getenv("ALLOWED_DOCKER_RESTARTS")
					if allowedRestarts != "" && allowedRestarts != "*" {
						isAllowed := false
						for _, r := range strings.Split(allowedRestarts, ",") {
							if strings.TrimSpace(r) == target {
								isAllowed = true
								break
							}
						}
						if !isAllowed {
							sendTelegram(token, chatStr, "⛔ <b>Restart not allowed</b>")
							continue
						}
					}

					sendTelegram(token, chatStr, fmt.Sprintf("⏳ Restarting <code>%s</code>...", target))
					_, err := cli.ContainerRestart(ctx, target, client.ContainerRestartOptions{})
					if err != nil {
						sendTelegram(token, chatStr, fmt.Sprintf("❌ Error: %v", err))
					} else {
						sendTelegram(token, chatStr, fmt.Sprintf("✅ <code>%s</code> restarted.", target))
					}
				}
			}
		}
	}
}

func sendTelegram(token, chatID, text string) {
	url := fmt.Sprintf("https://api.telegram.org/bot%s/sendMessage", token)
	payload, _ := json.Marshal(map[string]string{
		"chat_id":    chatID,
		"text":       text,
		"parse_mode": "HTML",
	})
	http.Post(url, "application/json", bytes.NewBuffer(payload))
}
