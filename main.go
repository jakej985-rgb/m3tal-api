package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
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
	Name     string  `json:"name"`
	CPU      float64 `json:"cpu"`
	Mem      float64 `json:"mem"`
	MemUsage uint64  `json:"mem_usage"`
	MemLimit uint64  `json:"mem_limit"`
	Status   string  `json:"status"`
	State    string  `json:"state"`
	Managed  bool    `json:"managed"`
	NetRx    uint64  `json:"net_rx"`
	NetTx    uint64  `json:"net_tx"`
}

type NetworkMetrics struct {
	Down    string  `json:"down"`
	Up      string  `json:"up"`
	DownRaw float64 `json:"down_raw"`
	UpRaw   float64 `json:"up_raw"`
	Load    float64 `json:"load"`
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

func historyAgent(s *M3talState, stateDir string) {
	logger := getAgentLogger("history")
	logger.Println("Agent started")
	ticker := time.NewTicker(60 * time.Second)
	historyPath := filepath.Join(stateDir, "history.json")

	type HistoryPoint struct {
		CPU       float64 `json:"cpu"`
		Mem       float64 `json:"mem"`
		NetDown   float64 `json:"net_down"`
		NetUp     float64 `json:"net_up"`
		Timestamp int64   `json:"timestamp"`
	}

	record := func() {
		s.mu.RLock()
		newPoint := HistoryPoint{
			CPU:       s.System.CPU,
			Mem:       s.System.Mem,
			NetDown:   s.Network.DownRaw,
			NetUp:     s.Network.UpRaw,
			Timestamp: time.Now().Unix(),
		}
		s.mu.RUnlock()

		// Skip if state isn't ready yet (e.g. just started)
		if newPoint.CPU == 0 && newPoint.Mem == 0 { return }

		var history []HistoryPoint
		if data, err := os.ReadFile(historyPath); err == nil {
			if err := json.Unmarshal(data, &history); err != nil {
				logger.Printf(" Failed to parse existing history: %v", err)
			}
		}

		history = append(history, newPoint)
		if len(history) > 1440 {
			history = history[len(history)-1440:]
		}

		saveAtomic(historyPath, history)
	}

	// Initial record on start
	go func() {
		time.Sleep(5 * time.Second) // Wait for agents to gather first metrics
		record()
	}()

	for range ticker.C {
		record()
	}
}


func getAgentLogger(name string) *log.Logger {
	stateDir := os.Getenv("STATE_DIR")
	if stateDir == "" {
		stateDir = filepath.Join("..", "state")
	}
	logsDir := filepath.Join(stateDir, "logs")
	os.MkdirAll(logsDir, 0755)
	logFile, err := os.OpenFile(filepath.Join(logsDir, name+".log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return log.New(os.Stdout, "["+name+"] ", log.LstdFlags)
	}
	return log.New(io.MultiWriter(os.Stdout, logFile), "["+name+"] ", log.LstdFlags)
}

func main() {
	state := &M3talState{
		Cooldowns:  make(map[string]time.Time),
		Containers: []ContainerMetric{},
	}

	stateDir := os.Getenv("STATE_DIR")
	if stateDir == "" {
		stateDir = filepath.Join("..", "state")
	}

	// Setup logging to file for dashboard visibility
	logsDir := filepath.Join(stateDir, "logs")
	os.MkdirAll(logsDir, 0755)
	if logFile, err := os.OpenFile(filepath.Join(logsDir, "m3tal-runtime.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644); err == nil {
		log.SetOutput(io.MultiWriter(os.Stdout, logFile))
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	log.Println("🚀 M3TAL Go Backend (6-Pillar) starting...")

	// Launch Core Pillars
	go registryAgent(ctx, state, stateDir)
	go monitorAgent(ctx, state)
	go metricsAgent(state)
	go anomalyAgent(ctx, state)
	go decisionAgent(ctx, state)
	go reconcileAgent(ctx, state)

	// Launch Supporting Services
	go historyAgent(state, stateDir)
	go apiAgent(ctx, state)
	go notifyAgent(state)
	go listenerAgent(ctx, state)

	select {}
}

// --- Agents ---

func apiAgent(ctx context.Context, s *M3talState) {
	logger := getAgentLogger("api")
	logger.Println("Agent started")
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		logger.Printf("❌ Failed to init Docker client: %v", err)
		return
	}

	http.HandleFunc("/api/containers/start", func(w http.ResponseWriter, r *http.Request) {
		handleContainerAction(ctx, cli, s, w, r, "start")
	})
	http.HandleFunc("/api/containers/stop", func(w http.ResponseWriter, r *http.Request) {
		handleContainerAction(ctx, cli, s, w, r, "stop")
	})
	http.HandleFunc("/api/containers/restart", func(w http.ResponseWriter, r *http.Request) {
		handleContainerAction(ctx, cli, s, w, r, "restart")
	})
	http.HandleFunc("/api/containers/logs", func(w http.ResponseWriter, r *http.Request) {
		handleContainerLogs(ctx, cli, w, r)
	})

	logger.Printf("🚀 Control Plane API listening on :5050")
	if err := http.ListenAndServe(":5050", nil); err != nil {
		logger.Printf("❌ Server failed: %v", err)
	}
}

func handleContainerAction(ctx context.Context, cli *client.Client, s *M3talState, w http.ResponseWriter, r *http.Request, action string) {
	if r.Method != http.MethodPost {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusMethodNotAllowed)
		json.NewEncoder(w).Encode(map[string]interface{}{"ok": false, "error": "Method not allowed"})
		return
	}

	var req struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]interface{}{"ok": false, "error": "Invalid request body"})
		return
	}

	var err error
	switch action {
	case "start":
		_, err = cli.ContainerStart(ctx, req.Name, client.ContainerStartOptions{})
	case "stop":
		_, err = cli.ContainerStop(ctx, req.Name, client.ContainerStopOptions{})
	case "restart":
		_, err = cli.ContainerRestart(ctx, req.Name, client.ContainerRestartOptions{})
	}

	w.Header().Set("Content-Type", "application/json")
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]interface{}{"ok": false, "error": err.Error()})
		return
	}

	// Record the successful action as a decision
	s.mu.Lock()
	s.Decisions = append(s.Decisions, Decision{
		Type:   action,
		Target: req.Name,
		Reason: "User initiated via Dashboard",
		Time:   time.Now().Unix(),
	})
	if len(s.Decisions) > 50 {
		s.Decisions = s.Decisions[1:]
	}
	s.dirty = true
	s.mu.Unlock()

	json.NewEncoder(w).Encode(map[string]interface{}{"ok": true, "message": fmt.Sprintf("Container %s %sed", req.Name, action)})
}

func handleContainerLogs(ctx context.Context, cli *client.Client, w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusMethodNotAllowed)
		json.NewEncoder(w).Encode(map[string]interface{}{"ok": false, "error": "Method not allowed"})
		return
	}

	var req struct {
		Name string `json:"name"`
		Tail string `json:"tail"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]interface{}{"ok": false, "error": "Invalid request body"})
		return
	}

	tail := req.Tail
	if tail == "" {
		tail = "80"
	}

	logReader, err := cli.ContainerLogs(ctx, req.Name, client.ContainerLogsOptions{
		ShowStdout: true,
		ShowStderr: true,
		Tail:       tail,
	})
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]interface{}{"ok": false, "error": err.Error()})
		return
	}
	defer logReader.Close()

	raw, _ := io.ReadAll(logReader)

	// Docker multiplexed stream has 8-byte headers per frame; strip them
	var clean bytes.Buffer
	for len(raw) > 8 {
		frameSize := int(raw[4])<<24 | int(raw[5])<<16 | int(raw[6])<<8 | int(raw[7])
		if 8+frameSize > len(raw) {
			break
		}
		clean.Write(raw[8 : 8+frameSize])
		raw = raw[8+frameSize:]
	}
	// If stripping produced nothing, the stream may not be multiplexed (TTY mode)
	output := clean.String()
	if output == "" {
		output = string(raw)
	}

	// Cap output size
	if len(output) > 8000 {
		output = output[len(output)-8000:]
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{"ok": true, "logs": output})
}

func registryAgent(ctx context.Context, s *M3talState, stateDir string) {
	logger := getAgentLogger("registry")
	logger.Println("Agent started")
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
	// Group host, gpu, and storage into the Metrics pillar
	go hostMetricsLoop(s)
	go gpuMetricsLoop(s)
	go storageMetricsLoop(s)
}

func hostMetricsLoop(s *M3talState) {
	logger := getAgentLogger("metrics")
	logger.Println("Host metrics loop started")
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
			Down:    formatSpeed(down * 1024 * 1024),
			Up:      formatSpeed(up * 1024 * 1024),
			DownRaw: down,
			UpRaw:   up,
			Load:    load,
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

func monitorAgent(ctx context.Context, s *M3talState) {
	logger := getAgentLogger("monitor")
	logger.Println("Agent started")
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return
	}
	ticker := time.NewTicker(10 * time.Second)
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
			
			cpuPerc := 0.0
			memPerc := 0.0
			var memUsage uint64
			var memLimit uint64
			var netRx uint64
			var netTx uint64

			// Only fetch stats for running containers to save resources
			if strings.ToLower(string(c.State)) == "running" {
				stats, err := cli.ContainerStats(ctx, c.ID, client.ContainerStatsOptions{Stream: false})
				if err == nil {
					var v struct {
						CPUStats struct {
							CPUUsage struct {
								TotalUsage uint64 `json:"total_usage"`
							} `json:"cpu_usage"`
							SystemCPUUsage uint64 `json:"system_cpu_usage"`
							OnlineCPUs     uint64 `json:"online_cpus"`
						} `json:"cpu_stats"`
						PreCPUStats struct {
							CPUUsage struct {
								TotalUsage uint64 `json:"total_usage"`
							} `json:"cpu_usage"`
							SystemCPUUsage uint64 `json:"system_cpu_usage"`
						} `json:"precpu_stats"`
						MemoryStats struct {
							Usage uint64            `json:"usage"`
							Limit uint64            `json:"limit"`
							Stats map[string]uint64 `json:"stats"`
						} `json:"memory_stats"`
						Networks map[string]struct {
							RxBytes uint64 `json:"rx_bytes"`
							TxBytes uint64 `json:"tx_bytes"`
						} `json:"networks"`
					}
					if err := json.NewDecoder(stats.Body).Decode(&v); err == nil {
						// CPU Calculation
						cpuDelta := float64(v.CPUStats.CPUUsage.TotalUsage) - float64(v.PreCPUStats.CPUUsage.TotalUsage)
						sysDelta := float64(v.CPUStats.SystemCPUUsage) - float64(v.PreCPUStats.SystemCPUUsage)
						cpus := float64(v.CPUStats.OnlineCPUs)
						if cpus == 0 { cpus = 1 }
						if sysDelta > 0 && cpuDelta > 0 {
							cpuPerc = (cpuDelta / sysDelta) * cpus * 100.0
						}

						// Memory Calculation (Usage - Cache)
						if v.MemoryStats.Limit > 0 {
							cache := v.MemoryStats.Stats["inactive_file"]
							if cache == 0 { cache = v.MemoryStats.Stats["cache"] }
							memUsage = v.MemoryStats.Usage - cache
							memLimit = v.MemoryStats.Limit
							memPerc = (float64(memUsage) / float64(memLimit)) * 100.0
						}

						// Network Aggregation
						for _, nw := range v.Networks {
							netRx += nw.RxBytes
							netTx += nw.TxBytes
						}
					}
					stats.Body.Close()
				}
			}

			managed := false
			if _, ok := c.Labels["m3tal.managed"]; ok {
				managed = true
			} else if _, ok := c.Labels["m3tal.stack"]; ok {
				managed = true
			}
			newStats = append(newStats, ContainerMetric{
				Name:     name,
				CPU:      cpuPerc,
				Mem:      memPerc,
				MemUsage: memUsage,
				MemLimit: memLimit,
				Status:   c.Status,
				State:    string(c.State),
				Managed:  managed,
				NetRx:    netRx,
				NetTx:    netTx,
			})
		}
		s.mu.Lock()
		s.Containers = newStats
		s.dirty = true
		s.mu.Unlock()
	}
}

func gpuMetricsLoop(s *M3talState) {
	logger := getAgentLogger("metrics")
	logger.Println("GPU metrics loop started")
	ticker := time.NewTicker(10 * time.Second)
	gpuRe := regexp.MustCompile(`gpu\s+([\d\.]+)%`)
	vramRe := regexp.MustCompile(`vram\s+[\d\.]+% ([\d\.]+)mb`)

	for range ticker.C {
		var cpuT, gpuT float64
		var gpuStats GpuStats

		// --- Resilient Temp Discovery (hwmon walking) ---
		if entries, err := os.ReadDir("/sys/class/hwmon"); err == nil {
			for _, entry := range entries {
				namePath := filepath.Join("/sys/class/hwmon", entry.Name(), "name")
				tempPath := filepath.Join("/sys/class/hwmon", entry.Name(), "temp1_input")
				if name, err := os.ReadFile(namePath); err == nil {
					n := strings.TrimSpace(string(name))
					if data, err := os.ReadFile(tempPath); err == nil {
						if val, err := strconv.Atoi(strings.TrimSpace(string(data))); err == nil {
							t := float64(val) / 1000.0
							switch n {
							case "amdgpu", "radeon":
								gpuT = t
							case "coretemp", "cpu_thermal", "k10temp", "zenpower", "acpitz", "it87":
								// Prioritize the highest found temperature for CPU
								if t > cpuT {
									cpuT = t
								}
							default:
								// Catch-all for other potentially relevant sensors
								if strings.Contains(strings.ToLower(n), "temp") && t > cpuT {
									cpuT = t
								}
							}
						}
					}
				}
			}
		}

		// Fallback for CPU if hwmon failed
		if cpuT == 0 {
			for i := 0; i < 5; i++ {
				path := fmt.Sprintf("/sys/class/thermal/thermal_zone%d/temp", i)
				if data, err := os.ReadFile(path); err == nil {
					if val, err := strconv.Atoi(strings.TrimSpace(string(data))); err == nil {
						cpuT = float64(val) / 1000.0
						break
					}
				}
			}
		}

		gpuStats.Name = "AMD Radeon HD 5770"
		gpuStats.Temp = gpuT
		gpuStats.MemTotal = 1024

		// --- GPU Usage Discovery ---
		radeontopFound := false
		radeontopPath := "/usr/bin/radeontop"
		
		// Attempt 1: Direct execution
		cmd := exec.Command(radeontopPath, "-d", "-", "-l", "1")
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
			logger.Printf(" radeontop (direct) success: Load=%d%%", gpuStats.Load)
		} else {
			// Attempt 2: chroot /host (uses host libraries)
			logger.Printf(" radeontop (direct) failed, trying chroot /host...")
			cmd = exec.Command("chroot", "/host", "radeontop", "-d", "-", "-l", "1")
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
				logger.Printf(" radeontop (chroot) success: Load=%d%%", gpuStats.Load)
			} else {
				logger.Printf(" radeontop (chroot) failed: %v, output: %s", err, string(output))
			}
		}

		if !radeontopFound {
			// Scan all DRM cards
			if cards, err := os.ReadDir("/sys/class/drm"); err == nil {
				for _, card := range cards {
					if !strings.HasPrefix(card.Name(), "card") || strings.Contains(card.Name(), "-") {
						continue
					}
					base := filepath.Join("/sys/class/drm", card.Name(), "device")
					logger.Printf(" Scanning sysfs for %s...", card.Name())
					
					// Load
					if data, err := os.ReadFile(filepath.Join(base, "gpu_busy_percent")); err == nil {
						if val, err := strconv.Atoi(strings.TrimSpace(string(data))); err == nil {
							gpuStats.Load = val
							gpuStats.Active = true
							logger.Printf(" Found load via sysfs: %d%%", val)
						}
					}
					
					// VRAM
					if data, err := os.ReadFile(filepath.Join(base, "mem_info_vram_used")); err == nil {
						if val, err := strconv.ParseUint(strings.TrimSpace(string(data)), 10, 64); err == nil {
							gpuStats.MemUsed = int(val / (1024 * 1024))
							gpuStats.Active = true
							logger.Printf(" Found VRAM used via sysfs: %dMB", gpuStats.MemUsed)
						}
					}
					if data, err := os.ReadFile(filepath.Join(base, "mem_info_vram_total")); err == nil {
						if val, err := strconv.ParseUint(strings.TrimSpace(string(data)), 10, 64); err == nil {
							gpuStats.MemTotal = int(val / (1024 * 1024))
						}
					}
					
					if gpuStats.Active { break }
				}
			} else {
				logger.Printf(" Failed to read /sys/class/drm: %v", err)
			}
		}

		if !gpuStats.Active {
			logger.Printf(" WARN: No active GPU discovered after all scans.")
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

func storageMetricsLoop(s *M3talState) {
	logger := getAgentLogger("metrics")
	logger.Println("Storage metrics loop started")
	ticker := time.NewTicker(30 * time.Second)
	// Identifiers for temperature lines across SATA, SAS, and NVMe
	tempKeywords := []string{"Temperature_Celsius", "Airflow_Temperature_Cel", "Composite Temperature", "Current Drive Temperature:"}

	// Detect host root mount (bind-mounted at /host)
	hostRoot := "/host"
	if _, err := os.Stat(hostRoot); err != nil {
		hostRoot = ""
	}

	for range ticker.C {
		disks := make(map[string]DiskInfo)
		highestUsage := 0.0

		// Strategy: Read /proc/mounts from host to discover real mount points
		var mountEntries []struct {
			Device     string
			Mountpoint string
		}

		if hostRoot != "" {
			// Parse /proc/mounts for real host filesystems
			if data, err := os.ReadFile("/proc/mounts"); err == nil {
				for _, line := range strings.Split(string(data), "\n") {
					fields := strings.Fields(line)
					if len(fields) < 3 {
						continue
					}
					dev, mnt, fstype := fields[0], fields[1], fields[2]
					// Only real block devices with real filesystems
					if !strings.HasPrefix(dev, "/dev/") {
						continue
					}
					if fstype == "squashfs" || fstype == "tmpfs" || fstype == "devtmpfs" {
						continue
					}
					// Skip container-internal mounts, only use /host/* mounts
					if !strings.HasPrefix(mnt, "/host") && mnt != "/" {
						continue
					}
					// Skip Docker overlays and internal paths
					if strings.Contains(mnt, "docker") || strings.Contains(mnt, "overlay") {
						continue
					}
					mountEntries = append(mountEntries, struct {
						Device     string
						Mountpoint string
					}{Device: dev, Mountpoint: mnt})
				}
			}
		}

		// Fallback: use gopsutil if no host mounts found
		if len(mountEntries) == 0 {
			parts, _ := disk.Partitions(false)
			for _, p := range parts {
				if strings.HasPrefix(p.Mountpoint, "/proc") || strings.HasPrefix(p.Mountpoint, "/dev") || strings.HasPrefix(p.Mountpoint, "/sys") {
					continue
				}
				mountEntries = append(mountEntries, struct {
					Device     string
					Mountpoint string
				}{Device: p.Device, Mountpoint: p.Mountpoint})
			}
		}

		seen := make(map[string]bool)
		for _, entry := range mountEntries {
			// Deduplicate by device
			if seen[entry.Device] {
				continue
			}
			seen[entry.Device] = true

			usage, err := disk.Usage(entry.Mountpoint)
			if err != nil || usage.Total == 0 {
				continue
			}

			// Derive a human-readable label
			label := entry.Mountpoint
			if label == "/" || label == "/host" {
				label = "System"
			} else if strings.HasPrefix(label, "/host/") {
				label = strings.TrimPrefix(label, "/host")
				label = filepath.Base(label)
			} else {
				label = filepath.Base(label)
			}

			// Skip tiny/virtual partitions (< 1GB)
			if usage.Total < 1024*1024*1024 {
				continue
			}

			var driveT float64
			dev := entry.Device
			if strings.HasPrefix(dev, "/dev/") {
				phys := dev
				// Strip partition number: /dev/sda1 -> /dev/sda
				if len(dev) > 8 && (dev[len(dev)-1] >= '0' && dev[len(dev)-1] <= '9') {
					for i := len(dev) - 1; i > 4; i-- {
						if dev[i] < '0' || dev[i] > '9' {
							phys = dev[:i+1]
							break
						}
					}
				}

				// Strategy: Use chroot /host to run the host's smartctl
				var output []byte
				var err error

				// Try chroot first
				cmd := exec.Command("chroot", "/host", "/usr/sbin/smartctl", "-a", phys)
				output, err = cmd.CombinedOutput()
				if err != nil && len(output) == 0 {
					cmd = exec.Command("chroot", "/host", "smartctl", "-a", phys)
					output, err = cmd.CombinedOutput()
				}
				
				// Fallback to direct container execution if chroot yielded nothing
				if len(output) == 0 {
					cmd = exec.Command("/usr/sbin/smartctl", "-a", phys)
					output, _ = cmd.CombinedOutput()
				}

				// Even if err != nil, smartctl often returns data with warning bits set
				if len(output) > 0 {
					lines := strings.Split(string(output), "\n")
					for _, line := range lines {
						matched := false
						for _, kw := range tempKeywords {
							if strings.Contains(strings.ToLower(line), strings.ToLower(kw)) {
								matched = true
								break
							}
						}

						if matched {
							fields := strings.Fields(line)
							if len(fields) > 0 {
								// SATA Table detection: starts with a numeric ID and has many columns
								isSata := len(fields) >= 9 && regexp.MustCompile(`^\d+$`).MatchString(fields[0])
								
								if isSata {
									// In SATA tables, RAW_VALUE is ALWAYS the last field.
									f := fields[len(fields)-1]
									f = regexp.MustCompile(`[^\d].*`).ReplaceAllString(f, "")
									if val, err := strconv.ParseFloat(f, 64); err == nil {
										driveT = val
									}
								} else {
									// For SAS/NVMe/Other: Look for the first numeric value from the right
									for i := len(fields) - 1; i >= 0; i-- {
										f := fields[i]
										f = regexp.MustCompile(`[^\d].*`).ReplaceAllString(f, "")
										if val, err := strconv.ParseFloat(f, 64); err == nil && val > 0 && val < 150 {
											driveT = val
											break
										}
									}
								}
							}
						}
						if driveT > 0 { break }
					}
				} else if err != nil {
					logger.Printf(" smartctl failed for %s: %v", phys, err)
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

func anomalyAgent(ctx context.Context, s *M3talState) {
	// Group scout and log observer into the Anomaly pillar
	go scoutLoop(ctx, s)
	go logObserverLoop(ctx)
}

func scoutLoop(ctx context.Context, s *M3talState) {
	logger := getAgentLogger("anomaly")
	logger.Println("Scout loop started")
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

func logObserverLoop(ctx context.Context) {
	logger := getAgentLogger("anomaly")
	logger.Println("Log observer loop started")
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

func decisionAgent(ctx context.Context, s *M3talState) {
	logger := getAgentLogger("decision")
	logger.Println("Agent started")
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

func reconcileAgent(ctx context.Context, s *M3talState) {
	logger := getAgentLogger("reconcile")
	logger.Println("Agent started")
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

				_, err = cli.ContainerRestart(ctx, c.Name, client.ContainerRestartOptions{})
				if err != nil {
					logger.Printf("❌ Failed to restart %s: %v", c.Name, err)
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
	logger := getAgentLogger("notify")
	logger.Println("Agent started")
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
	logger := getAgentLogger("listener")
	logger.Println("Agent started")
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
					_, err = cli.ContainerRestart(ctx, target, client.ContainerRestartOptions{})
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
