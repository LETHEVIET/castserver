package main

import (
	"embed"
	"encoding/json"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"time"

	"3dsstreaming/internal/cast"
	"3dsstreaming/internal/control"
	"3dsstreaming/internal/ingest"
	"3dsstreaming/internal/transport"
	"3dsstreaming/internal/webclient"
)

//go:embed static
var staticFS embed.FS

func main() {
	var (
		listenAddr = flag.String("listen", ":1108", "HTTP REST listen address")
		udpAddr    = flag.String("udp", ":1108", "UDP sender bind address")
		sourceURL  = flag.String("source", "", "Default ingest source URL (ffmpeg -i compatible). When set, /play can omit the source field.")
	)

	flag.Parse()

	log.Printf("3DS Stream Server starting...")
	log.Printf("  HTTP control: %s", *listenAddr)
	log.Printf("  UDP sender: %s", *udpAddr)

	ctrl := control.NewHandler()
	if *sourceURL != "" {
		ctrl.SetSourceURL(*sourceURL)
		log.Printf("  Default source: %s", *sourceURL)
	}

	mux := http.NewServeMux()

	staticSub, err := fs.Sub(staticFS, "static")
	if err != nil {
		log.Fatalf("failed to open embedded static fs: %v", err)
	}
	mux.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.FS(staticSub))))
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		http.ServeFileFS(w, r, staticFS, "static/index.html")
	})

	mux.HandleFunc("/play", playHandler(ctrl, *udpAddr))
	mux.HandleFunc("/stop", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if !ctrl.StopSession() {
			http.Error(w, "not playing", http.StatusConflict)
			return
		}
		log.Printf("control: streaming stopped")
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(control.PlayResponse{Status: "stopped"})
	})

	mux.HandleFunc("/stats", ctrl.HandleStats)
	mux.HandleFunc("/extract", ctrl.HandleExtract)
	mux.HandleFunc("/source", sourceHandler(ctrl))
	mux.HandleFunc("/ws/cast", cast.Handler(ctrl, *udpAddr))
	mux.HandleFunc("/ws/web", webclient.Handler(ctrl))
	mux.HandleFunc("/web", func(w http.ResponseWriter, r *http.Request) {
		http.ServeFileFS(w, r, staticFS, "static/web.html")
	})

	mux.HandleFunc("/keyframe", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		log.Printf("control: keyframe requested")
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(control.PlayResponse{Status: "keyframe requested"})
	})

	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	})

	// Note: we deliberately leave ReadTimeout/WriteTimeout off so long-lived
	// WebSocket subscriptions on /ws/web and /ws/cast aren't killed mid-stream.
	srv := &http.Server{
		Addr:              *listenAddr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	log.Printf("Ready. UI: http://localhost%s/  |  Web client: /web  |  API: POST /play", *listenAddr)
	log.Fatal(srv.ListenAndServe())
}

func playHandler(ctrl *control.Handler, udpAddr string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		var req control.PlayRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, fmt.Sprintf("invalid JSON: %v", err), http.StatusBadRequest)
			return
		}

		// Resolve source.
		source := req.Source
		if source == "" {
			source = ctrl.GetSourceURL()
		}
		if source == "" {
			http.Error(w, "source is required (no server default configured)", http.StatusBadRequest)
			return
		}

		// Resolve target.
		target := req.Target
		if target == "" {
			target = control.TargetUDP
		}
		if target != control.TargetUDP && target != control.TargetWeb {
			http.Error(w, fmt.Sprintf("invalid target %q (want udp or web)", target), http.StatusBadRequest)
			return
		}

		// Defaults.
		sw, sh, sfps := req.Width, req.Height, req.FPS
		if sw <= 0 || sh <= 0 {
			if target == control.TargetWeb {
				sw, sh = 480, 320
			} else {
				sw, sh = 256, 192
			}
		}
		if sfps <= 0 {
			sfps = 15
		}
		ctrl.SetStreamConfig(sw, sh, sfps)

		// Per-target validation + dispatch.
		switch target {
		case control.TargetUDP:
			if req.ClientAddr == "" {
				http.Error(w, "client_addr is required for target=udp", http.StatusBadRequest)
				return
			}
			if err := startUDPSession(ctrl, source, req.ClientAddr, udpAddr, sw, sh, sfps); err != nil {
				if err == control.ErrSessionActive {
					http.Error(w, err.Error(), http.StatusConflict)
				} else {
					http.Error(w, err.Error(), http.StatusInternalServerError)
				}
				return
			}
			log.Printf("control: udp session started — source=%s client=%s %dx%d@%d", source, req.ClientAddr, sw, sh, sfps)

		case control.TargetWeb:
			if err := startWebSession(ctrl, source, sw, sh, sfps, req.Quality); err != nil {
				if err == control.ErrSessionActive {
					http.Error(w, err.Error(), http.StatusConflict)
				} else {
					http.Error(w, err.Error(), http.StatusInternalServerError)
				}
				return
			}
			log.Printf("control: web session started — source=%s %dx%d@%d q=%d", source, sw, sh, sfps, req.Quality)
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(control.PlayResponse{Status: "playing"})
	}
}

func startUDPSession(ctrl *control.Handler, source, clientAddr, udpAddr string, sw, sh, sfps int) error {
	sender, err := transport.NewSender(clientAddr, udpAddr)
	if err != nil {
		return fmt.Errorf("failed to create UDP sender: %w", err)
	}

	ctx, cancel, err := ctrl.AcquireSession(control.TargetUDP)
	if err != nil {
		sender.Close()
		return err
	}
	ctrl.SetClientAddr(clientAddr)

	nalCh := make(chan []byte, 32)
	go func() {
		defer close(nalCh)
		if err := ingest.Run(ctx, source, sw, sh, sfps, nalCh); err != nil && ctx.Err() == nil {
			log.Printf("ingest: %v", err)
		}
	}()
	go func() {
		defer func() {
			sender.Close()
			ctrl.SetClientAddr("")
			ctrl.ReleaseSession()
			log.Printf("udp session: stopped")
		}()
		for frame := range nalCh {
			if err := sender.SendFrame(frame, uint16(sw), uint16(sh)); err != nil {
				log.Printf("udp send: %v", err)
				cancel()
				break
			}
			ctrl.IncrementFrames(1)
			ctrl.IncrementNALs(1)
		}
	}()
	return nil
}

func startWebSession(ctrl *control.Handler, source string, sw, sh, sfps, quality int) error {
	ctx, _, err := ctrl.AcquireSession(control.TargetWeb)
	if err != nil {
		return err
	}
	go func() {
		defer func() {
			ctrl.ReleaseSession()
			log.Printf("web session: stopped")
		}()
		if err := ingest.RunMJPEG(ctx, source, sw, sh, sfps, quality, ctrl.Hub()); err != nil && ctx.Err() == nil {
			log.Printf("web ingest: %v", err)
		}
	}()
	return nil
}

func sourceHandler(ctrl *control.Handler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]string{"source": ctrl.GetSourceURL()})
		case http.MethodPost:
			var req struct {
				Source string `json:"source"`
			}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				http.Error(w, fmt.Sprintf("invalid JSON: %v", err), http.StatusBadRequest)
				return
			}
			ctrl.SetSourceURL(req.Source)
			log.Printf("control: default source set to %q", req.Source)
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]string{"source": req.Source})
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	}
}
