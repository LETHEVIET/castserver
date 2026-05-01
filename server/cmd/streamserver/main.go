package main

import (
    "context"
    "embed"
    "encoding/json"
    "flag"
    "fmt"
    "io/fs"
    "log"
    "net/http"
    "sync"
    "time"

    "3dsstreaming/internal/control"
    "3dsstreaming/internal/ingest"
    "3dsstreaming/internal/transport"
)

//go:embed static
var staticFS embed.FS

func main() {
    var (
        listenAddr = flag.String("listen", ":1108", "HTTP REST listen address")
        udpAddr    = flag.String("udp", ":1108", "UDP sender bind address")
    )

    flag.Parse()

    log.Printf("3DS Stream Server starting...")
    log.Printf("  HTTP control: %s", *listenAddr)
    log.Printf("  UDP sender: %s", *udpAddr)

    ctrl := &control.Handler{}

    // Mutable state for the currently running ingest.
    // Protected by mu because net/http runs each request in its own goroutine.
    var (
        mu           sync.Mutex
        ingestCancel context.CancelFunc
        isPlaying    bool
    )

    mux := http.NewServeMux()

    // Serve embedded static files (UI) from /static/ and the root page.
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

    mux.HandleFunc("/play", func(w http.ResponseWriter, r *http.Request) {
        if r.Method != http.MethodPost {
            http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
            return
        }

        mu.Lock()
        if isPlaying {
            mu.Unlock()
            http.Error(w, "already playing — POST /stop first", http.StatusConflict)
            return
        }

        var req control.PlayRequest
        if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
            mu.Unlock()
            http.Error(w, fmt.Sprintf("invalid JSON: %v", err), http.StatusBadRequest)
            return
        }

        if req.Source == "" {
            mu.Unlock()
            http.Error(w, "source is required", http.StatusBadRequest)
            return
        }
        if req.ClientAddr == "" {
            mu.Unlock()
            http.Error(w, "client_addr is required", http.StatusBadRequest)
            return
        }

        // Create the UDP sender
        sender, err := transport.NewSender(req.ClientAddr, *udpAddr)
        if err != nil {
            mu.Unlock()
            log.Printf("sender error: %v", err)
            http.Error(w, fmt.Sprintf("failed to create UDP sender: %v", err), http.StatusInternalServerError)
            return
        }

        // Create a cancellable context for the ingest pipeline
        ctx, cancel := context.WithCancel(context.Background())
        ingestCancel = cancel

        // Create the NAL channel
        nalCh := make(chan []byte, 32)

        // Start ingest goroutine
        go func() {
            defer close(nalCh)
            if err := ingest.Run(ctx, req.Source, nalCh); err != nil {
                log.Printf("ingest: %v", err)
            }
            log.Printf("ingest: pipeline ended")
        }()

        // Start sender goroutine (reads from nalCh, sends UDP)
        go func() {
            defer func() {
                sender.Close()
                mu.Lock()
                isPlaying = false
                ingestCancel = nil
                ctrl.SetClientAddr("")
                mu.Unlock()
                log.Printf("sender: stopped")
            }()
            for frame := range nalCh {
                if err := sender.SendFrame(frame); err != nil {
                    log.Printf("send: %v", err)
                    cancel() // stop ingest on send failure
                    break
                }
                ctrl.IncrementFrames(1)
                ctrl.IncrementNALs(1)
            }
        }()

        isPlaying = true
        ctrl.SetClientAddr(req.ClientAddr)
        mu.Unlock()

        log.Printf("control: streaming started — source=%s client=%s", req.Source, req.ClientAddr)

        w.Header().Set("Content-Type", "application/json")
        json.NewEncoder(w).Encode(control.PlayResponse{Status: "playing"})
    })

    mux.HandleFunc("/stop", func(w http.ResponseWriter, r *http.Request) {
        if r.Method != http.MethodPost {
            http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
            return
        }

        mu.Lock()
        if !isPlaying {
            mu.Unlock()
            http.Error(w, "not playing", http.StatusConflict)
            return
        }

        if ingestCancel != nil {
            ingestCancel()
            ingestCancel = nil
        }
        isPlaying = false
        ctrl.SetClientAddr("")
        mu.Unlock()

        log.Printf("control: streaming stopped")
        w.Header().Set("Content-Type", "application/json")
        json.NewEncoder(w).Encode(control.PlayResponse{Status: "stopped"})
    })

    mux.HandleFunc("/stats", ctrl.HandleStats)

    mux.HandleFunc("/keyframe", func(w http.ResponseWriter, r *http.Request) {
        if r.Method != http.MethodPost {
            http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
            return
        }
        // In v1, /keyframe is acknowledged but the actual keyframe forcing
        // requires writing to ffmpeg's stdin. This is a v1.1 feature.
        log.Printf("control: keyframe requested")
        w.Header().Set("Content-Type", "application/json")
        json.NewEncoder(w).Encode(control.PlayResponse{Status: "keyframe requested"})
    })

    mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
        w.Header().Set("Content-Type", "application/json")
        json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
    })

    srv := &http.Server{
        Addr:         *listenAddr,
        Handler:      mux,
        ReadTimeout:  5 * time.Second,
        WriteTimeout: 5 * time.Second,
    }

    log.Printf("Ready. UI: http://localhost%s/  |  API: POST /play", *listenAddr)
    log.Fatal(srv.ListenAndServe())
}
