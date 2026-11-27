package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
)

func TestWorkerProgressWhilePrimaryServes(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		config := Config{Workers: []string{"one", "two", "three"}, WorkerStartConcurrency: 2, MemoryMB: 100}
		var memory atomic.Uint64
		memory.Store(60 * mib)
		manager := newAuxWorkers(config)
		manager.memory = memory.Load
		a := &App{config: config, aux: manager, workers: []*Worker{{}}}
		a.ready.Store(true)
		ctx, cancel := context.WithCancel(t.Context())
		defer func() { cancel(); manager.wg.Wait() }()
		manager.start(ctx, config.Workers, func(ctx context.Context, i int, token string) (*Worker, error) {
			if config.Workers[i-1] != token {
				t.Error("worker token order changed")
			}
			<-ctx.Done()
			return nil, ctx.Err()
		})
		synctest.Wait()
		routes := a.routes()
		health := httptest.NewRecorder()
		routes.ServeHTTP(health, httptest.NewRequest(http.MethodGet, "/health", nil))
		if health.Code != http.StatusOK || health.Body.String() != "OK" || len(a.connectedWorkers()) != 1 {
			t.Fatal("auxiliary startup blocked primary readiness")
		}
		progress := httptest.NewRecorder()
		routes.ServeHTTP(progress, httptest.NewRequest(http.MethodGet, "/api/workers", nil))
		var state struct {
			Primary bool           `json:"primary_connected"`
			Aux     workerProgress `json:"auxiliary"`
		}
		if err := json.Unmarshal(progress.Body.Bytes(), &state); err != nil || !state.Primary || state.Aux.Connecting != 1 || state.Aux.MemoryWait != 2 {
			t.Fatalf("unexpected live worker status: %s (%v)", progress.Body.String(), err)
		}
		memory.Store(0)
		time.Sleep(time.Second)
		synctest.Wait()
		if p := manager.progress(); p.Connecting != 2 || p.Queued != 1 {
			t.Fatalf("handshake limit was not respected after memory recovered: %+v", p)
		}
		cancel()
		manager.wg.Wait()
		if p := manager.progress(); p.Stopped != 3 || p.Connecting != 0 {
			t.Fatalf("shutdown left workers running: %+v", p)
		}
	})
}

func TestWorkerRetriesStartupAndReplacesDisconnectedClient(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		config := Config{Workers: []string{"aux"}, WorkerStartConcurrency: 1, MemoryMB: 100}
		manager := newAuxWorkers(config)
		manager.memory = func() uint64 { return 0 }
		a := &App{config: config, aux: manager, workers: []*Worker{{}}}
		ctx, cancel := context.WithCancel(t.Context())
		defer func() { cancel(); manager.wg.Wait() }()
		var attempts int
		manager.start(ctx, config.Workers, func(ctx context.Context, i int, token string) (*Worker, error) {
			attempts++
			if i != 1 || token != "aux" {
				t.Error("retry changed the configured bot or session slot")
			}
			if attempts == 1 {
				return nil, errors.New("connection unavailable")
			}
			client, stop := context.WithCancel(ctx)
			w := &Worker{done: make(chan struct{}), stop: stop}
			go func() { <-client.Done(); close(w.done) }()
			return w, nil
		})
		synctest.Wait()
		if p := manager.progress(); p.Retrying != 1 || len(a.connectedWorkers()) != 1 {
			t.Fatalf("failed startup was not queued for retry: %+v", p)
		}
		time.Sleep(3 * time.Second)
		synctest.Wait()
		workers := a.connectedWorkers()
		if len(workers) != 2 || attempts != 2 {
			t.Fatalf("worker did not join after retry (attempts=%d)", attempts)
		}
		first := workers[1]
		first.stop()
		synctest.Wait()
		if len(a.connectedWorkers()) != 1 {
			t.Fatal("disconnected worker remained eligible for downloads")
		}
		time.Sleep(5 * time.Second)
		synctest.Wait()
		workers = a.connectedWorkers()
		if len(workers) != 2 || workers[1] == first || attempts != 3 {
			t.Fatalf("worker was not replaced after disconnect (attempts=%d)", attempts)
		}
		cancel()
		manager.wg.Wait()
		if workers[1].connected() || manager.progress().Stopped != 1 {
			t.Fatal("shutdown did not join the replacement worker")
		}
	})
}
