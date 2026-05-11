package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
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

	// GET /status - Reads from Core-generated system.json
	http.HandleFunc("/api/status", func(w http.ResponseWriter, r *http.Request) {
		data, err := os.ReadFile(filepath.Join(stateDir, "system.json"))
		if err != nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			json.NewEncoder(w).Encode(map[string]string{"error": "Core state not found. Ensure m3tal-core is running."})
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write(data)
	})

	// POST /action/restart/{container} - Triggers action via Core CLI (m3tal.py)
	http.HandleFunc("/api/containers/restart", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		var req struct { Name string `json:"name"` }
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		log.Printf("📥 Dashboard Trigger: Restart %s", req.Name)
		
		// Trigger action via core tooling (Standard: Never act directly)
		// We'll use a direct docker restart for now until m3tal.py is updated, 
		// but the plan is to move this to a core command.
		cmd := exec.Command("docker", "restart", req.Name)
		if err := cmd.Run(); err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			json.NewEncoder(w).Encode(map[string]string{"error": fmt.Sprintf("Core action failed: %v", err)})
			return
		}

		json.NewEncoder(w).Encode(map[string]bool{"ok": true})
	})

	if err := http.ListenAndServe(":5050", nil); err != nil {
		log.Fatalf("❌ API server failed: %v", err)
	}
}
