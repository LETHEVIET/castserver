package main

import (
	"embed"
	"encoding/json"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os/exec"
	"time"

	"castserver/internal/cast"
	"castserver/internal/control"
	"castserver/internal/webclient"
)

//go:embed static
var staticFS embed.FS

func main() {
	listenAddr := flag.String("listen", ":1108", "HTTP listen address")
	flag.Parse()

	if _, err := exec.LookPath("ffmpeg"); err != nil {
		log.Fatalf("ffmpeg not found in PATH: %v", err)
	}

	log.Printf("castserver starting...")
	log.Printf("  HTTP: %s", *listenAddr)

	ctrl := control.NewHandler()

	mux := http.NewServeMux()

	staticSub, err := fs.Sub(staticFS, "static")
	if err != nil {
		log.Fatalf("failed to open embedded static fs: %v", err)
	}
	mux.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.FS(staticSub))))
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" && r.URL.Path != "/web" {
			http.NotFound(w, r)
			return
		}
		http.ServeFileFS(w, r, staticFS, "static/index.html")
	})

	mux.HandleFunc("/presets", ctrl.HandlePresets)
	mux.HandleFunc("/stats", ctrl.HandleStats)
	mux.HandleFunc("/ws/cast", cast.Handler(ctrl))
	mux.HandleFunc("/ws/web", webclient.Handler(ctrl))

	mux.HandleFunc("/interfaces", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var ips []string
		ifaces, err := net.Interfaces()
		if err == nil {
			for _, i := range ifaces {
				addrs, err := i.Addrs()
				if err != nil {
					continue
				}
				for _, a := range addrs {
					if ipnet, ok := a.(*net.IPNet); ok {
						ip := ipnet.IP
						if ip.IsLoopback() || ip.To4() == nil {
							continue
						}
						ips = append(ips, ip.String())
					}
				}
			}
		}
		if ips == nil {
			ips = []string{}
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(ips)
	})

	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	})

	mux.HandleFunc("/stats/latency", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(ctrl.Latency().Stats())
	})

	mux.HandleFunc("/stats/telemetry/toggle", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var req struct {
			Enabled bool `json:"enabled"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		ctrl.Latency().SetEnabled(req.Enabled)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{"status": "ok", "enabled": ctrl.Latency().IsEnabled()})
	})

	fmt.Printf("Ready. Admin UI: http://localhost%s/  |  Viewer: /web\n", *listenAddr)

	srv := &http.Server{
		Addr:              *listenAddr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	log.Fatal(srv.ListenAndServe())
}
