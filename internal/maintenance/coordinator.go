package maintenance

import (
	"context"
	"sync"
	"time"
)

type Trigger string

const (
	TriggerStartup  Trigger = "startup"
	TriggerSchedule Trigger = "schedule"
	TriggerPressure Trigger = "pressure"
	TriggerOperator Trigger = "operator"
)

type Result struct {
	RemovedObjects int64 `json:"removed_objects"`
	RemovedBytes   int64 `json:"removed_bytes"`
	SkippedObjects int64 `json:"skipped_objects"`
	Errors         int64 `json:"errors"`
}

type Run struct {
	ID         uint64     `json:"id"`
	Trigger    Trigger    `json:"trigger"`
	StartedAt  time.Time  `json:"started_at"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
	Result     Result     `json:"result"`
	Error      string     `json:"error,omitempty"`
}

type Snapshot struct {
	State   string `json:"state"`
	Current *Run   `json:"current,omitempty"`
	Last    *Run   `json:"last,omitempty"`
}

type Runner func(context.Context) (Result, error)

type Coordinator struct {
	mu      sync.Mutex
	now     func() time.Time
	runner  Runner
	nextID  uint64
	current *Run
	last    *Run
}

func New(runner Runner) *Coordinator {
	return &Coordinator{runner: runner, now: time.Now}
}

// Run executes at most one collection at a time. accepted is false when a
// caller should reuse the returned current run instead of starting another.
func (c *Coordinator) Run(ctx context.Context, trigger Trigger) (run Run, accepted bool) {
	c.mu.Lock()
	if c.current != nil {
		run = *c.current
		c.mu.Unlock()
		return run, false
	}
	c.nextID++
	run = Run{ID: c.nextID, Trigger: trigger, StartedAt: c.now().UTC()}
	c.current = &run
	c.mu.Unlock()

	result, err := c.runner(ctx)
	finished := c.now().UTC()
	run.Result = result
	run.FinishedAt = &finished
	if err != nil {
		run.Error = err.Error()
		run.Result.Errors++
	}

	c.mu.Lock()
	c.current = nil
	c.last = &run
	c.mu.Unlock()
	return run, true
}

func (c *Coordinator) Snapshot() Snapshot {
	c.mu.Lock()
	defer c.mu.Unlock()
	snapshot := Snapshot{State: "idle"}
	if c.current != nil {
		current := *c.current
		snapshot.Current = &current
		snapshot.State = "running"
	}
	if c.last != nil {
		last := *c.last
		snapshot.Last = &last
	}
	return snapshot
}
