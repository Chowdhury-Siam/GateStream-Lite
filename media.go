package main

import (
	"context"
	"errors"
	"io"
	"sync"
	"time"

	"github.com/gotd/td/tgerr"
)

const (
	mediaChunkSize  = 1 << 20
	maxMediaBuffers = 24
	mediaRefTTL     = 10 * time.Minute
	maxMediaRefs    = 16
)

func mediaBufferLimit(c Config) int {
	// Keep a bounded payload window across all responses. Leave room for the
	// Telegram codec, connected clients, and the rest of the Go runtime.
	return int(min(maxMediaBuffers, max(2, c.MemoryMB/16)))
}

type chunkResult struct {
	data []byte
	err  error
}

type mediaPrefetch struct {
	ctx     context.Context
	cancel  context.CancelFunc
	app     *App
	fetch   func(context.Context, int64) ([]byte, error)
	pending map[int64]chan chunkResult
	wg      sync.WaitGroup

	head, scheduled, last int64
	window, limit         int
	wait                  time.Duration
}

func (a *App) prefetch(ctx context.Context, br byteRange, fetch func(context.Context, int64) ([]byte, error)) *mediaPrefetch {
	ctx, cancel := context.WithCancel(ctx)
	head := br.Start / mediaChunkSize
	limit := max(1, min(cap(a.readSlots), maxMediaBuffers))
	if a.bufferSlots != nil {
		limit = min(limit, cap(a.bufferSlots))
	}
	return &mediaPrefetch{
		ctx: ctx, cancel: cancel, app: a, fetch: fetch, pending: make(map[int64]chan chunkResult, limit),
		head: head, scheduled: head, last: br.End / mediaChunkSize, window: min(3, limit), limit: limit,
	}
}

func (p *mediaPrefetch) launch(required bool) bool {
	if p.ctx.Err() != nil {
		return false
	}
	if slots := p.app.bufferSlots; slots != nil {
		if required {
			select {
			case slots <- struct{}{}:
			case <-p.ctx.Done():
				return false
			}
		} else {
			select {
			case slots <- struct{}{}:
			default:
				return false
			}
		}
	}
	index := p.scheduled
	p.scheduled++
	result := make(chan chunkResult, 1)
	p.pending[index] = result
	p.wg.Go(func() {
		data, err := p.fetch(p.ctx, index*mediaChunkSize)
		result <- chunkResult{data: data, err: err}
	})
	return true
}

func (p *mediaPrefetch) next() (int64, chunkResult) {
	offset := p.head * mediaChunkSize
	if len(p.pending) == 0 && !p.launch(true) {
		return offset, chunkResult{err: p.ctx.Err()}
	}
	window := p.window
	if p.app.config.MemoryMB > 0 {
		free := int64(p.app.config.MemoryMB*mib) - int64(goMemoryInUse())
		window = int(max(1, min(int64(window), free/(2*mediaChunkSize))))
	}
	for len(p.pending) < window && p.scheduled <= p.last && p.launch(false) {
	}
	start := time.Now()
	select {
	case result := <-p.pending[p.head]:
		p.wait = time.Since(start)
		return offset, result
	case <-p.ctx.Done():
		return offset, chunkResult{err: p.ctx.Err()}
	}
}

func (p *mediaPrefetch) release() {
	if p.app.bufferSlots != nil {
		<-p.app.bufferSlots
	}
}

func (p *mediaPrefetch) consume(writeTime time.Duration) {
	delete(p.pending, p.head)
	p.release()
	p.head++
	switch {
	case p.wait > 4*time.Millisecond && p.wait > writeTime:
		p.window = min(p.limit, p.window+2)
	case writeTime > 20*time.Millisecond && writeTime > 2*p.wait:
		p.window = max(min(2, p.limit), p.window-1)
	}
}

func (p *mediaPrefetch) close() {
	p.cancel()
	p.wg.Wait()
	for index := range p.pending {
		delete(p.pending, index)
		p.release()
	}
}

type mediaMessage struct {
	channel int64
	id      int
}

type mediaReference struct {
	ready    chan struct{}
	source   Source
	err      error
	expires  time.Time
	lastUsed time.Time
}

// The reference belongs to this specific authorization. Waiters share an
// in-flight lookup, and expiry or a rejected reference only clears that entry.
func (w *Worker) mediaReference(ctx context.Context, key mediaMessage) (*mediaReference, error) {
	for {
		now := time.Now()
		w.refMu.Lock()
		ref := w.refs[key]
		if ref != nil && ref.ready != nil {
			done := ref.ready
			w.refMu.Unlock()
			select {
			case <-done:
				continue
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		if ref != nil && now.Before(ref.expires) {
			ref.lastUsed = now
			w.refMu.Unlock()
			return ref, ref.err
		}
		for k, candidate := range w.refs {
			if candidate.ready == nil && !now.Before(candidate.expires) {
				delete(w.refs, k)
			}
		}
		if len(w.refs) >= maxMediaRefs {
			var oldestKey mediaMessage
			var oldest *mediaReference
			for k, candidate := range w.refs {
				if candidate.ready == nil && (oldest == nil || candidate.lastUsed.Before(oldest.lastUsed)) {
					oldestKey, oldest = k, candidate
				}
			}
			if oldest != nil {
				delete(w.refs, oldestKey)
			} else {
				// Don't evict an active lookup. The process-wide RPC cap bounds
				// these temporary lookups even when all cache entries are busy.
				w.refMu.Unlock()
				source, err := w.source(ctx, key.channel, key.id)
				return &mediaReference{source: source}, err
			}
		}
		ref = &mediaReference{ready: make(chan struct{})}
		if w.refs == nil {
			w.refs = make(map[mediaMessage]*mediaReference)
		}
		w.refs[key] = ref
		w.refMu.Unlock()

		source, err := w.source(ctx, key.channel, key.id)
		w.refMu.Lock()
		ref.source, ref.err, ref.lastUsed = source, err, time.Now()
		ttl := mediaRefTTL
		if err != nil {
			ttl = 5 * time.Second
		}
		ref.expires = ref.lastUsed.Add(ttl)
		if ctx.Err() != nil {
			delete(w.refs, key)
		}
		close(ref.ready)
		ref.ready = nil
		w.refMu.Unlock()
		return ref, err
	}
}

func (w *Worker) invalidateReference(key mediaMessage, old *mediaReference) {
	w.refMu.Lock()
	defer w.refMu.Unlock()
	if w.refs[key] == old {
		delete(w.refs, key)
	}
}

func (w *Worker) referenceHint(key mediaMessage, now time.Time) (warm, unavailable bool) {
	w.refMu.Lock()
	defer w.refMu.Unlock()
	if ref := w.refs[key]; ref != nil && ref.ready == nil && now.Before(ref.expires) {
		return ref.err == nil, ref.err != nil
	}
	return false, false
}

func (w *Worker) recordRead(duration time.Duration) {
	value := max(duration.Microseconds(), 1)
	for {
		old := w.readMicros.Load()
		next := value
		if old != 0 {
			next = (old*3 + value) / 4
		}
		if w.readMicros.CompareAndSwap(old, next) {
			return
		}
	}
}

func (a *App) chooseMediaWorker(workers []*Worker, key mediaMessage, tried map[*Worker]bool) *Worker {
	a.workerPickMu.Lock()
	defer a.workerPickMu.Unlock()
	now := time.Now()
	var chosen *Worker
	var best int64
	for _, w := range workers {
		if tried[w] || !w.connected() || w.until.Load() > now.Unix() {
			continue
		}
		warm, unavailable := w.referenceHint(key, now)
		if unavailable {
			continue
		}
		latency := w.readMicros.Load()
		if latency == 0 {
			latency = 150_000
		}
		score := max(25_000, latency) * (1 + w.active.Load())
		if !warm {
			score += 12_000
		}
		if chosen == nil || score < best {
			chosen, best = w, score
		}
	}
	if chosen != nil {
		chosen.active.Add(1)
	}
	return chosen
}

func (a *App) readChunk(ctx context.Context, key mediaMessage, size, offset int64) ([]byte, error) {
	select {
	case a.readSlots <- struct{}{}:
		defer func() { <-a.readSlots }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	workers := a.connectedWorkers()
	tried := make(map[*Worker]bool)
	var last error
	for attempt := 0; attempt < max(3, min(16, len(workers)*2)); attempt++ {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		w := a.chooseMediaWorker(workers, key, tried)
		if w == nil {
			break
		}
		data, err := w.readMediaChunk(ctx, key, size, offset)
		w.active.Add(-1)
		if err == nil {
			return data, nil
		}
		last = err
		tried[w] = true
	}
	if last == nil {
		last = errors.New("no Telegram worker is available")
	}
	return nil, last
}

func (w *Worker) readMediaChunk(ctx context.Context, key mediaMessage, size, offset int64) ([]byte, error) {
	for retry := 0; retry < 2; retry++ {
		lookup, cancel := context.WithTimeout(ctx, 10*time.Second)
		ref, err := w.mediaReference(lookup, key)
		cancel()
		if err != nil {
			w.applyFloodWait(err)
			return nil, err
		}
		if ref.source.Size != size {
			return nil, errors.New("Telegram media size changed")
		}
		start := time.Now()
		data, err := w.chunk(ctx, ref.source, offset)
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if err == nil && int64(len(data)) == min(mediaChunkSize, size-offset) {
			w.recordRead(time.Since(start))
			return data, nil
		}
		if err == nil {
			err = io.ErrUnexpectedEOF
		}
		stale := tgerr.Is(err, "FILE_REFERENCE_EXPIRED", "FILE_REFERENCE_INVALID", "FILE_REFERENCE_EMPTY") || errors.Is(err, io.ErrUnexpectedEOF)
		if stale {
			w.invalidateReference(key, ref)
			if retry == 0 {
				continue
			}
		} else {
			// Briefly rotate away from a failed transport. This cannot
			// shorten a Telegram FloodWait already recorded by chunk.
			w.coolDown(3 * time.Second)
			w.recordRead(max(time.Since(start), time.Second))
		}
		return nil, err
	}
	return nil, io.ErrUnexpectedEOF
}
