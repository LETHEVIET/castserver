//go:build ignore

package main

import (
    "context"
    "fmt"
    "log"
    "os"
    "os/signal"
    "path/filepath"

    "3dsstreaming/internal/ingest"
    "3dsstreaming/internal/transport"
)

func main() {
    if len(os.Args) < 3 {
        fmt.Fprintf(os.Stderr, "Usage: %s <video-file> <client-addr>\n", os.Args[0])
        fmt.Fprintf(os.Stderr, "Example: %s sample.mp4 127.0.0.1:9001\n", os.Args[0])
        os.Exit(1)
    }

    sourcePath, _ := filepath.Abs(os.Args[1])
    sourceURL := fmt.Sprintf("file:%s", sourcePath)
    clientAddr := os.Args[2]

    log.Printf("Source: %s", sourceURL)
    log.Printf("Client: %s", clientAddr)

    ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
    defer cancel()

    sender, err := transport.NewSender(clientAddr, ":0")
    if err != nil {
        log.Fatalf("sender: %v", err)
    }
    defer sender.Close()

    nalCh := make(chan []byte, 32)
    frameCount := 0

    go func() {
        if err := ingest.Run(ctx, sourceURL, nalCh); err != nil {
            log.Printf("ingest error: %v", err)
        }
        close(nalCh)
    }()

    for nal := range nalCh {
        if err := sender.SendNAL(nal); err != nil {
            log.Printf("send error: %v", err)
            break
        }
        frameCount++
        if frameCount%100 == 0 {
            log.Printf("Sent %d frames", frameCount)
        }
    }

    log.Printf("Done. Sent %d frames", frameCount)
}
