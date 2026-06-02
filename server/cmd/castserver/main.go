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
	"time"

	"castserver/internal/control"
	"castserver/internal/sfu"
)

//go:embed static
var staticFS embed.FS

func main() {
	listenAddr := flag.String("listen", ":1108", "HTTP listen address")
	flag.Parse()

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

	mgr := ctrl.SFU()
	mux.HandleFunc("/webrtc/publish", mgr.HandlePublish)
	mux.HandleFunc("/webrtc/subscribe", mgr.HandleSubscribe)
	mux.HandleFunc("/webrtc/stop", sfu.HandleStop(mgr))

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
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	})

	fmt.Printf("Ready. Admin UI: http://localhost%s/  |  Viewer: /web\n", *listenAddr)

	srv := &http.Server{
		Addr:              *listenAddr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	log.Fatal(srv.ListenAndServe())
}
