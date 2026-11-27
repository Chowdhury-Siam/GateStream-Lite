package main

import (
	"fmt"
	"net"
	"net/http"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/gotd/td/telegram"
	"github.com/gotd/td/tg"
	"go.mongodb.org/mongo-driver/v2/bson"
)

// TestMediaBenchmarkOrigin serves a simulated Telegram source over real HTTP.
// scripts/bench-download.py arms it in a separate process; normal tests skip it.
func TestMediaBenchmarkOrigin(t *testing.T) {
	address := os.Getenv("GATESTREAM_BENCH_ADDRESS")
	if address == "" {
		t.Skip("run scripts/bench-download.py to start the HTTP benchmark fixture")
	}
	delay, err := time.ParseDuration(os.Getenv("GATESTREAM_BENCH_DELAY"))
	if err != nil {
		t.Fatal(err)
	}
	size, err := strconv.Atoi(os.Getenv("GATESTREAM_BENCH_SIZE"))
	if err != nil || size <= 0 {
		t.Fatal("invalid fixture size")
	}
	config := Config{MemoryMB: 350, Concurrency: 16}
	backend := &testMediaBackend{data: make([]byte, size), delay: delay}
	for offset := 0; offset < size; offset += mediaChunkSize {
		chunk := backend.data[offset:min(size, offset+mediaChunkSize)]
		for i := range chunk {
			chunk[i] = byte(1 + (offset/mediaChunkSize)%251)
		}
	}
	a := &App{config: config, readSlots: make(chan struct{}, config.Concurrency),
		bufferSlots: make(chan struct{}, mediaBufferLimit(config)),
		streamSlots: make(chan struct{}, 8), cache: &Cache{}, activity: newActivity(),
		files: fakeFiles{bson.M{"channel_id": int64(-1000000000001), "message_id": 1, "file_name": "fixture.mkv",
			"mime_type": "video/x-matroska", "file_size": int64(size)}}}
	for i := byte(1); i <= 28; i++ {
		rpc := &testMediaRPC{backend: backend, bot: i, lane: make(chan struct{}, 1)}
		a.workers = append(a.workers, &Worker{rpc: tg.NewClient(rpc), peers: map[int64]Peer{-1000000000001: {ID: 1, Hash: 7}},
			pools: map[int]telegram.CloseInvoker{2: rpc}})
	}
	a.ready.Store(true)
	mux := http.NewServeMux()
	mux.Handle("/", a.routes())
	mux.HandleFunc("GET /benchmark-peak", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, backend.readPeak.Load())
	})
	listener, err := net.Listen("tcp", address)
	if err != nil {
		t.Fatal(err)
	}
	t.Fatal((&http.Server{Handler: mux, ReadHeaderTimeout: 3 * time.Second}).Serve(listener))
}
