package main

import (
	"context"
	"embed"
	"encoding/json"
	"flag"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/pion/webrtc/v4"

	"castserver/internal/control"
	"castserver/internal/sfu"
)

//go:embed static
var staticFS embed.FS

func main() {
	listenAddr := flag.String("listen", ":1108", "HTTP listen address")
	turnURL := flag.String("turn-url", "", "TURN server URL (repeatable comma-separated, e.g. turn:1.2.3.4:3478)")
	turnUser := flag.String("turn-user", "", "TURN username")
	turnPass := flag.String("turn-pass", "", "TURN credential")
	flag.Parse()

	slog.Info("castserver starting", "listen", *listenAddr)

	var iceServers []webrtc.ICEServer
	if *turnURL != "" {
		iceServers = []webrtc.ICEServer{{
			URLs:       []string{*turnURL},
			Username:   *turnUser,
			Credential: *turnPass,
		}}
		slog.Info("TURN configured", "url", *turnURL)
	}

	ctrl := control.NewHandler()
	mgr := ctrl.SFU()
	if len(iceServers) > 0 {
		mgr.SetICEServers(iceServers)
	}

	mux := http.NewServeMux()

	staticSub, err := fs.Sub(staticFS, "static")
	if err != nil {
		slog.Error("failed to open embedded static fs", "error", err)
		os.Exit(1)
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
	mux.HandleFunc("/stats/stream", ctrl.HandleStatsStream)

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

	fmt.Printf("Ready. Admin UI: http://localhost%s/  |  Viewer: /web\n", *listenAddr)

	srv := &http.Server{
		Addr:              *listenAddr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-quit
		slog.Info("shutting down...")
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		mgr.Stop()
		if err := srv.Shutdown(ctx); err != nil {
			slog.Error("http shutdown", "error", err)
		}
	}()

	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		slog.Error("server error", "error", err)
		os.Exit(1)
	}
}
