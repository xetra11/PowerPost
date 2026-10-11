// Package jobs runs background work (transcodes, uploads) persisted in SQLite,
// so queued work survives restarts.
package jobs

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"powerposter/internal/store"
)

type Handler func(ctx context.Context, payload json.RawMessage) error

type worker struct {
	handler     Handler
	concurrency int
	wake        chan struct{}
}

type Queue struct {
	store   *store.Store
	log     *slog.Logger
	workers map[string]*worker
	wg      sync.WaitGroup
}

func New(s *store.Store, log *slog.Logger) *Queue {
	return &Queue{store: s, log: log, workers: map[string]*worker{}}
}

// Register must be called before Run.
func (q *Queue) Register(typ string, concurrency int, h Handler) {
	q.workers[typ] = &worker{handler: h, concurrency: concurrency, wake: make(chan struct{}, 1)}
}

func (q *Queue) Enqueue(ctx context.Context, typ string, payload any) error {
	w, ok := q.workers[typ]
	if !ok {
		return fmt.Errorf("unknown job type %q", typ)
	}
	if _, err := q.store.EnqueueJob(ctx, typ, payload); err != nil {
		return err
	}
	select {
	case w.wake <- struct{}{}:
	default:
	}
	return nil
}

// Run dispatches jobs until ctx is cancelled, then waits for running jobs.
func (q *Queue) Run(ctx context.Context) {
	var dispatchers sync.WaitGroup
	for typ, w := range q.workers {
		dispatchers.Add(1)
		go func() {
			defer dispatchers.Done()
			q.dispatch(ctx, typ, w)
		}()
	}
	dispatchers.Wait()
	q.wg.Wait()
}

func (q *Queue) dispatch(ctx context.Context, typ string, w *worker) {
	slots := make(chan struct{}, w.concurrency)
	tick := time.NewTicker(2 * time.Second)
	defer tick.Stop()
	for {
		// Wait for a free slot.
		select {
		case slots <- struct{}{}:
		case <-ctx.Done():
			return
		}
		job, ok, err := q.store.ClaimJob(context.WithoutCancel(ctx), typ)
		if err != nil {
			q.log.Error("claim job", "type", typ, "err", err)
		}
		if !ok {
			<-slots
			select {
			case <-w.wake:
			case <-tick.C:
			case <-ctx.Done():
				return
			}
			continue
		}
		q.wg.Add(1)
		go func() {
			defer q.wg.Done()
			defer func() { <-slots }()
			q.run(ctx, job, w)
		}()
	}
}

func (q *Queue) run(ctx context.Context, job store.Job, w *worker) {
	start := time.Now()
	err := func() (err error) {
		defer func() {
			if r := recover(); r != nil {
				err = fmt.Errorf("panic: %v", r)
			}
		}()
		return w.handler(ctx, job.Payload)
	}()
	if ctx.Err() != nil && err != nil {
		// Interrupted by shutdown: leave it "running" so RecoverJobs handles it on next start.
		q.log.Info("job interrupted by shutdown", "id", job.ID, "type", job.Type)
		return
	}
	if ferr := q.store.FinishJob(context.WithoutCancel(ctx), job.ID, err); ferr != nil {
		q.log.Error("finish job", "id", job.ID, "err", ferr)
	}
	if err != nil {
		q.log.Warn("job failed", "id", job.ID, "type", job.Type, "err", err, "took", time.Since(start).Round(time.Millisecond))
	} else {
		q.log.Info("job done", "id", job.ID, "type", job.Type, "took", time.Since(start).Round(time.Millisecond))
	}
}
