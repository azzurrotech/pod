// Command pod runs the pod HTML-form database as a standalone server.
//
// Records are stored as XML files in siloed directories under -db, matching
// the destination URL of every form submission. The indexer and normalizer
// background services keep an in-memory index current and backfill schema
// defaults into older records.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"azzurrotech/pod"
)

func main() {
	var (
		port              = flag.Int("port", 8080, "HTTP listen port")
		db                = flag.String("db", "./data", "database base directory")
		mount             = flag.String("mount", "/", "URL mount prefix (use / for standalone)")
		ui                = flag.Bool("ui", true, "serve the HTML form interface")
		indexInterval     = flag.Duration("index-interval", 5*time.Second, "indexer sync interval (0 disables)")
		normalizeInterval = flag.Duration("normalize-interval", 30*time.Second, "normalizer run interval (0 disables)")
		encrypt           = flag.Bool("encrypt", envBool("POD_ENCRYPT", false), "encrypt field values and uploaded blobs at rest (requires -key-file or POD_KEY)")
		keyFile           = flag.String("key-file", os.Getenv("POD_KEY_FILE"), "path to a 32-byte key file (raw, hex or base64) used with -encrypt")
		version           = flag.Bool("version", false, "print version and exit")
	)
	flag.Parse()

	if *version {
		fmt.Printf("pod %s\n", pod.Version)
		return
	}

	store, err := openStore(*db, *encrypt, *keyFile)
	if err != nil {
		log.Fatalf("pod: %v", err)
	}

	h := pod.NewHandler(pod.HandlerOptions{Store: store, Mount: *mount, UI: boolPtr(*ui)})

	// Warm the index once at boot so queries can use equality narrowing
	// immediately. A failure only degrades query speed, not correctness.
	if err := h.Indexer().Rebuild(); err != nil {
		log.Printf("pod: initial index rebuild incomplete: %v", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	h.RunBackground(ctx, *indexInterval, *normalizeInterval)

	srv := &http.Server{
		Addr:              fmt.Sprintf(":%d", *port),
		Handler:           h,
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		<-ctx.Done()
		shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutCtx); err != nil {
			log.Printf("pod: shutdown: %v", err)
		}
	}()

	log.Printf("pod %s listening on %s (base %s, mount %q)", pod.Version, srv.Addr, store.Base(), *mount)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("pod: server: %v", err)
	}
	log.Print("pod: stopped")
}

// boolPtr returns a pointer to b (for the tri-state UI option).
func boolPtr(b bool) *bool { return &b }

// envBool reads a boolean environment variable, falling back to def.
func envBool(key string, def bool) bool {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return def
	}
	return b
}

// openStore opens the store, enabling encryption at rest when -encrypt (or the
// POD_KEY environment variable) is set. The key comes from -key-file or POD_KEY
// and may be 32 raw bytes, hex, or base64.
func openStore(db string, encrypt bool, keyFile string) (*pod.Store, error) {
	raw := []byte(os.Getenv("POD_KEY"))
	if keyFile != "" {
		data, err := os.ReadFile(keyFile)
		if err != nil {
			return nil, fmt.Errorf("read key file %q: %w", keyFile, err)
		}
		raw = data
	}
	if !encrypt && len(raw) == 0 {
		return pod.Open(db)
	}
	if len(raw) == 0 {
		return nil, fmt.Errorf("-encrypt requires -key-file or POD_KEY")
	}
	key, err := pod.ParseEncryptionKey(raw)
	if err != nil {
		return nil, err
	}
	log.Printf("pod: encryption at rest enabled (AES-256-GCM)")
	return pod.OpenEncrypted(db, key)
}
