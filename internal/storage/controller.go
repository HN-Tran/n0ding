package storage

import "sync"

// Controller coordinates a single cache budget across all repositories.
// Callers reserve known response sizes before creating cache temporary files.
type Controller struct {
	mu             sync.Mutex
	maxBytes       int64
	highWatermark  float64
	lowWatermark   float64
	minFreeBytes   int64
	committedBytes int64
	reservedBytes  int64
	bypassObjects  int64
	bypassBytes    int64
}

type Snapshot struct {
	MaxBytes       int64 `json:"max_bytes"`
	HighBytes      int64 `json:"high_bytes"`
	LowBytes       int64 `json:"low_bytes"`
	MinFreeBytes   int64 `json:"min_free_bytes"`
	CommittedBytes int64 `json:"committed_bytes"`
	ReservedBytes  int64 `json:"reserved_bytes"`
	BypassObjects  int64 `json:"bypass_objects"`
	BypassBytes    int64 `json:"bypass_bytes"`
	Pressure       bool  `json:"pressure"`
}

type Reservation struct {
	controller *Controller
	bytes      int64
	once       sync.Once
}

func NewController(maxBytes int64, highWatermark, lowWatermark float64, minFreeBytes, committedBytes int64) *Controller {
	return &Controller{
		maxBytes:       maxBytes,
		highWatermark:  highWatermark,
		lowWatermark:   lowWatermark,
		minFreeBytes:   minFreeBytes,
		committedBytes: committedBytes,
	}
}

// Reserve returns nil when an object should be proxied without caching.
// A non-positive size is intentionally bypassed because it cannot be safely
// accounted for before a download starts.
func (c *Controller) Reserve(bytes, filesystemFreeBytes int64) *Reservation {
	c.mu.Lock()
	defer c.mu.Unlock()

	if bytes <= 0 || (c.maxBytes > 0 && c.committedBytes+c.reservedBytes+bytes > c.maxBytes) ||
		(c.minFreeBytes > 0 && filesystemFreeBytes-bytes < c.minFreeBytes) {
		c.bypassObjects++
		if bytes > 0 {
			c.bypassBytes += bytes
		}
		return nil
	}
	c.reservedBytes += bytes
	return &Reservation{controller: c, bytes: bytes}
}

// Commit converts the reservation into committed usage. It is safe to call
// Commit or Release more than once; only the first call has an effect.
func (r *Reservation) Commit(actualBytes int64) {
	if r == nil {
		return
	}
	r.once.Do(func() {
		r.controller.mu.Lock()
		defer r.controller.mu.Unlock()
		r.controller.reservedBytes -= r.bytes
		if actualBytes > 0 {
			r.controller.committedBytes += actualBytes
		}
	})
}

func (r *Reservation) Release() {
	if r == nil {
		return
	}
	r.once.Do(func() {
		r.controller.mu.Lock()
		r.controller.reservedBytes -= r.bytes
		r.controller.mu.Unlock()
	})
}

func (c *Controller) Remove(bytes int64) {
	if bytes <= 0 {
		return
	}
	c.mu.Lock()
	c.committedBytes -= bytes
	if c.committedBytes < 0 {
		c.committedBytes = 0
	}
	c.mu.Unlock()
}

func (c *Controller) Snapshot() Snapshot {
	c.mu.Lock()
	defer c.mu.Unlock()
	highBytes := int64(float64(c.maxBytes) * c.highWatermark)
	lowBytes := int64(float64(c.maxBytes) * c.lowWatermark)
	return Snapshot{
		MaxBytes:       c.maxBytes,
		HighBytes:      highBytes,
		LowBytes:       lowBytes,
		MinFreeBytes:   c.minFreeBytes,
		CommittedBytes: c.committedBytes,
		ReservedBytes:  c.reservedBytes,
		BypassObjects:  c.bypassObjects,
		BypassBytes:    c.bypassBytes,
		Pressure:       c.maxBytes > 0 && c.committedBytes+c.reservedBytes >= highBytes,
	}
}
