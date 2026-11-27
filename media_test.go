package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/gotd/td/bin"
	"github.com/gotd/td/telegram"
	"github.com/gotd/td/tg"
	"go.mongodb.org/mongo-driver/v2/bson"
)

type testMediaBackend struct {
	data          []byte
	delay         time.Duration
	readPeak      atomic.Int64
	readsInFlight atomic.Int64
}

type testMediaRPC struct {
	backend *testMediaBackend
	bot     byte
	lane    chan struct{}
	reads   atomic.Int64
	refs    atomic.Int64
}

func (*testMediaRPC) Close() error { return nil }

func (r *testMediaRPC) Invoke(ctx context.Context, input bin.Encoder, output bin.Decoder) error {
	switch req := input.(type) {
	case *tg.ChannelsGetMessagesRequest:
		r.refs.Add(1)
		output.(*tg.MessagesMessagesBox).Messages = &tg.MessagesChannelMessages{Messages: []tg.MessageClass{
			&tg.Message{ID: 1, Media: &tg.MessageMediaDocument{Document: &tg.Document{
				ID: 99, AccessHash: 777, FileReference: []byte{r.bot}, DCID: 2, Size: int64(len(r.backend.data))}}},
		}}
		return nil
	case *tg.UploadGetFileRequest:
		loc, ok := req.Location.(*tg.InputDocumentFileLocation)
		if !ok || !bytes.Equal(loc.FileReference, []byte{r.bot}) {
			return errors.New("media reference crossed bot authorizations")
		}
		select {
		case r.lane <- struct{}{}:
		case <-ctx.Done():
			return ctx.Err()
		}
		defer func() { <-r.lane }()
		r.reads.Add(1)
		active := r.backend.readsInFlight.Add(1)
		defer r.backend.readsInFlight.Add(-1)
		for last := r.backend.readPeak.Load(); active > last; last = r.backend.readPeak.Load() {
			if r.backend.readPeak.CompareAndSwap(last, active) {
				break
			}
		}
		delay := r.backend.delay
		if req.Offset%(4*mediaChunkSize) == 0 {
			delay *= 2 // Later chunks finish before the first one.
		}
		if !waitForWorker(ctx, delay) {
			return ctx.Err()
		}
		last := min(req.Offset+int64(req.Limit), int64(len(r.backend.data)))
		if req.Offset < 0 || req.Offset >= last || req.Offset%mediaChunkSize != 0 || req.Limit != mediaChunkSize {
			return errors.New("unexpected MTProto chunk boundaries")
		}
		output.(*tg.UploadFileBox).File = &tg.UploadFile{Bytes: bytes.Clone(r.backend.data[req.Offset:last])}
		return nil
	default:
		return errors.New("unexpected Telegram RPC")
	}
}

func TestParallelRangeDeliveryAndWorkerReuse(t *testing.T) {
	backend := &testMediaBackend{data: make([]byte, 8*mediaChunkSize+19), delay: 6 * time.Millisecond}
	for i := range backend.data {
		backend.data[i] = byte((i/mediaChunkSize)*10 + i%11)
	}
	a := &App{config: Config{MemoryMB: 350}, readSlots: make(chan struct{}, 4), bufferSlots: make(chan struct{}, 6),
		cache: &Cache{}, activity: newActivity(), files: fakeFiles{bson.M{
			"channel_id": int64(-1000000000001), "message_id": 1, "file_name": "video.mkv", "file_size": int64(len(backend.data)),
		}}}
	var bots []*testMediaRPC
	for i := byte(1); i <= 4; i++ {
		rpc := &testMediaRPC{backend: backend, bot: i, lane: make(chan struct{}, 1)}
		bots = append(bots, rpc)
		a.workers = append(a.workers, &Worker{rpc: tg.NewClient(rpc), peers: map[int64]Peer{-1000000000001: {ID: 1, Hash: 7}},
			pools: map[int]telegram.CloseInvoker{2: rpc}})
	}
	server := httptest.NewServer(a.routes())
	defer server.Close()
	for _, suffix := range []string{"", "?inline=1", "?stream=1"} {
		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, server.URL+"/dl/video"+suffix, nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Range", "bytes=12345-8388612")
		res, err := server.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		payload, err := io.ReadAll(res.Body)
		res.Body.Close()
		if suffix != "" && !strings.HasPrefix(res.Header.Get("Content-Disposition"), "inline;") {
			t.Fatal("playback link stopped returning inline media")
		}
		if err != nil || res.StatusCode != 206 || res.Header.Get("Content-Range") != "bytes 12345-8388612/8388627" ||
			!bytes.Equal(payload, backend.data[12345:8388613]) {
			t.Fatalf("out-of-order or incorrect range: %s %s %v", res.Status, res.Header.Get("Content-Range"), err)
		}
	}
	if peak := backend.readPeak.Load(); peak < 2 || peak > 4 {
		t.Fatalf("expected parallel reads within global limit of 4, got %d", peak)
	}
	used, reads := 0, int64(0)
	for _, bot := range bots {
		if bot.reads.Load() > 0 {
			used++
		}
		reads += bot.reads.Load()
		if bot.refs.Load() > 1 {
			t.Fatal("worker looked up the same media again for a seek")
		}
	}
	if used < 2 || reads != 27 || len(a.bufferSlots) != 0 {
		t.Fatalf("worker selection or buffer cleanup: workers=%d reads=%d buffers=%d", used, reads, len(a.bufferSlots))
	}
}

func TestPrefetchSharesBudgetAndCancelsBlockedReads(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		a := &App{readSlots: make(chan struct{}, 8), bufferSlots: make(chan struct{}, 4)}
		ctx, stop := context.WithCancel(t.Context())
		defer stop()
		var started atomic.Int64
		var stopped atomic.Int64
		fetch := func(ctx context.Context, _ int64) ([]byte, error) {
			started.Add(1)
			<-ctx.Done()
			stopped.Add(1)
			return nil, ctx.Err()
		}
		var readers sync.WaitGroup
		for i := 0; i < 3; i++ {
			readers.Go(func() {
				p := a.prefetch(ctx, byteRange{End: 32 * mediaChunkSize}, fetch)
				defer p.close()
				_, result := p.next()
				if !errors.Is(result.err, context.Canceled) {
					t.Error(result.err)
				}
			})
		}
		synctest.Wait()
		if started.Load() != 4 || len(a.bufferSlots) != 4 {
			t.Fatal("parallel requests exceeded the shared payload budget")
		}
		stop()
		readers.Wait()
		if stopped.Load() != started.Load() || len(a.bufferSlots) != 0 {
			t.Fatal("cancellation did not join reads and release all payload slots")
		}
	})
}

type testPoolFactory struct {
	pool       telegram.CloseInvoker
	started    chan struct{}
	continueDC chan struct{}
	calls      atomic.Int64
}

func (*testPoolFactory) Config() tg.Config                           { return tg.Config{ThisDC: 2} }
func (p *testPoolFactory) Pool(int64) (telegram.CloseInvoker, error) { return p.pool, nil }
func (p *testPoolFactory) DC(ctx context.Context, _ int, _ int64) (telegram.CloseInvoker, error) {
	if p.calls.Add(1) == 1 {
		close(p.started)
	}
	select {
	case <-p.continueDC:
		return p.pool, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func TestSinglePoolCreationPerDCDoesNotBlockWarmDC(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		factory := &testPoolFactory{pool: &testMediaRPC{}, started: make(chan struct{}), continueDC: make(chan struct{})}
		w := &Worker{media: factory}
		warm, err := w.mediaClient(t.Context(), 2)
		if err != nil {
			t.Fatal(err)
		}
		clients := make(chan *tg.Client, 2)
		var wg sync.WaitGroup
		for i := 0; i < cap(clients); i++ {
			wg.Go(func() {
				client, err := w.mediaClient(t.Context(), 4)
				if err != nil {
					t.Error(err)
				}
				clients <- client
			})
		}
		<-factory.started
		synctest.Wait()
		reused, err := w.mediaClient(t.Context(), 2)
		if err != nil || reused != warm {
			t.Error("warm media client was not reused", err)
		}
		close(factory.continueDC)
		wg.Wait()
		if first, second := <-clients, <-clients; first == nil || first != second || factory.calls.Load() != 1 {
			t.Fatal("concurrent reads created multiple pools for the same DC")
		}
	})
}
