package main

import (
	"encoding/json"
	"log"
	"net/http"
	"os"
	"path/filepath"

	"github.com/jakej985-rgb/m3tal-core/pkg/containers"
	"github.com/jakej985-rgb/m3tal-core/pkg/health"
	"github.com/jakej985-rgb/m3tal-core/pkg/system"
)

func main() {
	stateDir := os.Getenv("STATE_DIR")
	if stateDir == "" {
		stateDir = filepath.Join("..", "state")
	}

	mgr, err := containers.NewManager()
	if err != nil {
		log.Fatalf("❌ Failed to initialize container manager: %v", err)
	}

	log.Println("🚀 M3TAL API Interface starting on :5050...")

	// GET /api/containers - List all containers
	http.HandleFunc("/api/containers", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		list, err := mgr.ListContainers()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(list)
	})

	// POST /api/containers/start
	http.HandleFunc("/api/containers/start", func(w http.ResponseWriter, r *http.Request) {
		handleContainerAction(w, r, mgr.StartContainer)
	})

	// POST /api/containers/stop
	http.HandleFunc("/api/containers/stop", func(w http.ResponseWriter, r *http.Request) {
		handleContainerAction(w, r, mgr.StopContainer)
	})

	// POST /api/containers/restart
	http.HandleFunc("/api/containers/restart", func(w http.ResponseWriter, r *http.Request) {
		handleContainerAction(w, r, mgr.RestartContainer)
	})

	// GET /api/metrics - Real-time system metrics
	http.HandleFunc("/api/metrics", func(w http.ResponseWriter, r *http.Request) {
		stats, err := system.GetStats()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(stats)
	})

	// GET /api/storage - Disk metrics
	http.HandleFunc("/api/storage", func(w http.ResponseWriter, r *http.Request) {
		// Placeholder: reuse system stats or add specific disk logic
		stats, _ := system.GetStats()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]float64{"usage": stats.DiskUsage})
	})

	// GET /api/gpu - GPU metrics (if applicable)
	http.HandleFunc("/api/gpu", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "not_implemented"})
	})

	// GET /api/registry - Container registry
	http.HandleFunc("/api/registry", func(w http.ResponseWriter, r *http.Request) {
		list, _ := mgr.ListContainers()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(list)
	})

	// GET /api/health - Health metrics
	http.HandleFunc("/api/health", func(w http.ResponseWriter, r *http.Request) {
		// Example: checking the dashboard service
		h := health.CheckService("dashboard", "http://localhost:3000")
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(h)
	})

	// GET /api/logs - Get container logs
	http.HandleFunc("/api/logs", func(w http.ResponseWriter, r *http.Request) {
		name := r.URL.Query().Get("name")
		if name == "" {
			// Return list of log-capable containers
			list, _ := mgr.ListContainers()
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(list)
			return
		}
		tail := r.URL.Query().Get("tail")
		
		content, err := mgr.Logs(name, tail)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"logs": content})
	})

	if err := http.ListenAndServe(":5050", nil); err != nil {
		log.Fatalf("❌ API server failed: %v", err)
	}
}

func handleContainerAction(w http.ResponseWriter, r *http.Request, action func(string) error) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var req struct { Name string `json:"name"` }
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	if err := action(req.Name); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]bool{"ok": true})
}
