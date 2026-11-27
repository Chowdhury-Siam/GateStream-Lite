package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"math/rand/v2"
	"runtime/metrics"
	"sync"
	"time"
)

const mib = 1024 * 1024

type workerState string

const (
	workerQueued     workerState = "queued"
	workerMemoryWait workerState = "waiting for memory"
	workerConnecting workerState = "connecting"
	workerReady      workerState = "connected"
	workerRetrying   workerState = "waiting to retry"
	workerStopped    workerState = "stopped"
)

type workerSlot struct {
	state  workerState
	worker *Worker
}

type workerProgress struct {
	Configured int `json:"configured"`
	Connected  int `json:"connected"`
	Queued     int `json:"queued"`
	MemoryWait int `json:"waiting_for_memory"`
	Connecting int `json:"connecting"`
	Retrying   int `json:"retrying"`
	Stopped    int `json:"stopped"`
}

type auxWorkers struct {
	mu       sync.RWMutex
	slots    []workerSlot
	starting int
	limit    int
	budget   uint64
	reserve  uint64
	memory   func() uint64
	wg       sync.WaitGroup
}

func newAuxWorkers(c Config) *auxWorkers {
	budget := uint64(c.MemoryMB) * mib
	if c.MemoryRestartMB > 0 {
		// Reserve space below the configured automatic restart threshold.
		budget = min(budget, uint64(max(c.MemoryRestartMB-24, 1))*mib)
	}
	m := &auxWorkers{
		slots: make([]workerSlot, len(c.Workers)), limit: c.WorkerStartConcurrency,
		budget: budget, reserve: min(40*mib, budget/4), memory: goMemoryInUse,
	}
	for i := range m.slots {
		m.slots[i].state = workerQueued
	}
	return m
}

func goMemoryInUse() uint64 {
	samples := [...]metrics.Sample{{Name: "/memory/classes/total:bytes"}, {Name: "/memory/classes/heap/released:bytes"}}
	metrics.Read(samples[:])
	return samples[0].Value.Uint64() - samples[1].Value.Uint64()
}

func (m *auxWorkers) progressLocked() workerProgress {
	p := workerProgress{Configured: len(m.slots)}
	for _, slot := range m.slots {
		switch stateOf(slot) {
		case workerReady:
			p.Connected++
		case workerConnecting:
			p.Connecting++
		case workerMemoryWait:
			p.MemoryWait++
		case workerRetrying:
			p.Retrying++
		case workerStopped:
			p.Stopped++
		default:
			p.Queued++
		}
	}
	return p
}

func stateOf(slot workerSlot) workerState {
	if slot.state == workerReady && !slot.worker.connected() {
		return workerRetrying
	}
	return slot.state
}

func (m *auxWorkers) progress() workerProgress {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.progressLocked()
}

func (m *auxWorkers) snapshot() []workerSlot {
	if m == nil {
		return nil
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	slots := append([]workerSlot(nil), m.slots...)
	for i := range slots {
		slots[i].state = stateOf(slots[i])
		if slots[i].state != workerReady {
			slots[i].worker = nil
		}
	}
	return slots
}

func (m *auxWorkers) setLocked(i int, state workerState, w *Worker, delay time.Duration) {
	if m.slots[i].state == state {
		return
	}
	m.slots[i] = workerSlot{state: state, worker: w}
	p := m.progressLocked()
	if delay > 0 {
		log.Printf("Worker %d %s in %s · auxiliary connected %d/%d", i+1, state, delay.Round(time.Second), p.Connected, p.Configured)
		return
	}
	log.Printf("Worker %d %s · auxiliary connected %d/%d", i+1, state, p.Connected, p.Configured)
}

func (m *auxWorkers) admit(i int) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.starting >= m.limit {
		m.setLocked(i, workerQueued, nil, 0)
		return false
	}
	// Reserve memory for every handshake because its allocations may not be
	// visible in the runtime estimate yet. Keep all configured slots queued.
	if m.memory()+m.reserve*uint64(m.starting+1) > m.budget {
		m.setLocked(i, workerMemoryWait, nil, 0)
		return false
	}
	m.starting++
	m.setLocked(i, workerConnecting, nil, 0)
	return true
}

func (m *auxWorkers) start(ctx context.Context, tokens []string, connect func(context.Context, int, string) (*Worker, error)) {
	for i, token := range tokens {
		m.wg.Go(func() { m.supervise(ctx, i, token, connect) })
	}
}

func (m *auxWorkers) supervise(ctx context.Context, i int, token string, connect func(context.Context, int, string) (*Worker, error)) {
	defer func() {
		m.mu.Lock()
		m.setLocked(i, workerStopped, nil, 0)
		m.mu.Unlock()
	}()
	backoff := 2 * time.Second
	for ctx.Err() == nil {
		if !m.admit(i) {
			if !waitForWorker(ctx, time.Second) {
				return
			}
			continue
		}
		w, err := connect(ctx, i+1, token)
		connectedAt := time.Now()
		m.mu.Lock()
		m.starting--
		if err == nil && ctx.Err() == nil {
			m.setLocked(i, workerReady, w, 0)
		}
		m.mu.Unlock()
		if w != nil {
			select {
			case <-w.done:
			case <-ctx.Done():
				w.stop()
				<-w.done
				return
			}
			if time.Since(connectedAt) >= time.Minute {
				backoff = 2 * time.Second
			}
		}
		if ctx.Err() != nil {
			return
		}
		delay := backoff + time.Duration(rand.Int64N(int64(backoff/4)))
		var telegramErr *APIError
		if errors.As(err, &telegramErr) && telegramErr.Wait > 0 {
			delay = max(delay, time.Duration(telegramErr.Wait)*time.Second)
		}
		m.mu.Lock()
		m.setLocked(i, workerRetrying, nil, delay)
		m.mu.Unlock()
		if !waitForWorker(ctx, delay) {
			return
		}
		backoff = min(backoff*2, 2*time.Minute)
	}
}

func waitForWorker(ctx context.Context, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func (p workerProgress) String() string {
	return fmt.Sprintf("Auxiliary workers %d/%d connected · %d queued · %d waiting for memory · %d connecting · %d retrying", p.Connected, p.Configured, p.Queued, p.MemoryWait, p.Connecting, p.Retrying)
}

func (a *App) workerProgress() workerProgress {
	if a.aux == nil {
		return workerProgress{}
	}
	return a.aux.progress()
}

func (a *App) connectedWorkers() []*Worker {
	workers := make([]*Worker, 0, len(a.workers)+len(a.config.Workers))
	for _, w := range a.workers {
		if w.connected() {
			workers = append(workers, w)
		}
	}
	if a.aux != nil {
		a.aux.mu.RLock()
		defer a.aux.mu.RUnlock()
		for _, slot := range a.aux.slots {
			if slot.worker.connected() {
				workers = append(workers, slot.worker)
			}
		}
	}
	return workers
}
