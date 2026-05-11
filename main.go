package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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

	// Generic JSON reader helper
	serveJSON := func(w http.ResponseWriter, filename string) {
		data, err := os.ReadFile(filepath.Join(stateDir, filename))
		if err != nil {
			w.WriteHeader(http.StatusNotFound)
			json.NewEncoder(w).Encode(map[string]string{"error": fmt.Sprintf("%s not found", filename)})
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

	// GET /api/logs - List and tail logs
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
				content, _ := exec.Command("tail", "-n", "100", filepath.Join(logsDir, file.Name())).Output()
				logData[file.Name()] = string(content)
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
		var err error

		switch action {
		case "restart":
			output, err = exec.Command("docker", "restart", req.Name).CombinedOutput()
		case "stop":
			output, err = exec.Command("docker", "stop", req.Name).CombinedOutput()
		case "start":
			output, err = exec.Command("docker", "start", req.Name).CombinedOutput()
		case "logs":
			tail := req.Tail
			if tail == "" { tail = "50" }
			output, err = exec.Command("docker", "logs", "--tail", tail, req.Name).CombinedOutput()
		default:
			w.WriteHeader(http.StatusNotFound)
			return
		}

		if err != nil {
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
