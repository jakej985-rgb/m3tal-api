package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// --- M3TAL API Interface (m3tal-api) ---
// Responsibility: Expose system control + state.
// Role: Read-Only (Eyes/Ears). NOT the brain.

func main() {
	stateDir := os.Getenv("STATE_DIR")
	if stateDir == "" {
		stateDir = filepath.Join("..", "state")
	}

	log.Println("🚀 M3TAL API Interface starting on :5050...")

	// Generic JSON reader with retry logic for race conditions
	serveJSON := func(w http.ResponseWriter, filename string) {
		fullPath := filepath.Join(stateDir, filename)
		var data []byte
		var err error

		// Retry up to 3 times for partial writes/locks
		for i := 0; i < 3; i++ {
			data, err = os.ReadFile(fullPath)
			if err == nil && len(data) > 0 {
				break
			}
			time.Sleep(100 * time.Millisecond)
		}

		if err != nil {
			w.WriteHeader(http.StatusNotFound)
			json.NewEncoder(w).Encode(map[string]string{"error": fmt.Sprintf("%s not found or unreadable", filename)})
			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.Write(data)
	}

	// GET /api/status - Core system state
	http.HandleFunc("/api/status", func(w http.ResponseWriter, r *http.Request) {
		serveJSON(w, "system.json")
	})

	// GET /api/health - Health metrics
	http.HandleFunc("/api/health", func(w http.ResponseWriter, r *http.Request) {
		serveJSON(w, "health.json")
	})

	// GET /api/metrics - Real-time metrics
	http.HandleFunc("/api/metrics", func(w http.ResponseWriter, r *http.Request) {
		serveJSON(w, "metrics.json")
	})

	// GET /api/storage - Disk metrics
	http.HandleFunc("/api/storage", func(w http.ResponseWriter, r *http.Request) {
		serveJSON(w, "storage.json")
	})

	// GET /api/gpu - GPU metrics
	http.HandleFunc("/api/gpu", func(w http.ResponseWriter, r *http.Request) {
		serveJSON(w, "gpu.json")
	})

	// GET /api/registry - Container registry
	http.HandleFunc("/api/registry", func(w http.ResponseWriter, r *http.Request) {
		serveJSON(w, "registry.json")
	})

	// GET /api/logs - List and tail logs (NATIVE IMPLEMENTATION)
	http.HandleFunc("/api/logs", func(w http.ResponseWriter, r *http.Request) {
		logsDir := filepath.Join(stateDir, "logs")
		files, err := os.ReadDir(logsDir)
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}

		logData := make(map[string]string)
		for _, file := range files {
			if !file.IsDir() && strings.HasSuffix(file.Name(), ".log") {
				path := filepath.Join(logsDir, file.Name())
				content := nativeTail(path, 100)
				logData[file.Name()] = content
			}
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(logData)
	})

	// POST /api/containers/{action}
	http.HandleFunc("/api/containers/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}

		action := strings.TrimPrefix(r.URL.Path, "/api/containers/")
		var req struct { Name string `json:"name"`; Tail string `json:"tail"` }
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		log.Printf("📥 Action: %s on %s", action, req.Name)

		var output []byte
		var cmdErr error

		switch action {
		case "restart":
			output, cmdErr = exec.Command("docker", "restart", req.Name).CombinedOutput()
		case "stop":
			output, cmdErr = exec.Command("docker", "stop", req.Name).CombinedOutput()
		case "start":
			output, cmdErr = exec.Command("docker", "start", req.Name).CombinedOutput()
		case "logs":
			tail := req.Tail
			if tail == "" { tail = "50" }
			output, cmdErr = exec.Command("docker", "logs", "--tail", tail, req.Name).CombinedOutput()
		default:
			w.WriteHeader(http.StatusNotFound)
			return
		}

		if cmdErr != nil {
			w.WriteHeader(http.StatusInternalServerError)
			json.NewEncoder(w).Encode(map[string]string{"error": string(output)})
			return
		}

		w.Header().Set("Content-Type", "application/json")
		if action == "logs" {
			json.NewEncoder(w).Encode(map[string]string{"logs": string(output)})
		} else {
			json.NewEncoder(w).Encode(map[string]bool{"ok": true})
		}
	})

	if err := http.ListenAndServe(":5050", nil); err != nil {
		log.Fatalf("❌ API server failed: %v", err)
	}
}

// nativeTail reads the last n lines of a file without external dependencies.
func nativeTail(filename string, lines int) string {
	f, err := os.Open(filename)
	if err != nil {
		return fmt.Sprintf("Error opening log: %v", err)
	}
	defer f.Close()

	// Simple tail: Seek to end and read a reasonable chunk (e.g., 32KB)
	// For a more robust tail, we'd scan backwards for line endings.
	stat, _ := f.Stat()
	size := stat.Size()
	limit := int64(32768) // 32KB
	if size < limit {
		limit = size
	}

	buf := make([]byte, limit)
	_, err = f.ReadAt(buf, size-limit)
	if err != nil && err != io.EOF {
		return fmt.Sprintf("Error reading log: %v", err)
	}

	return string(buf)
}
