package storage

import (
	"math"
	"sync"
)

// Controller coordinates a single cache budget across all repositories.
// Callers reserve known response sizes before creating cache temporary files.
type Controller struct {
	mu              sync.Mutex
	maxBytes        int64
	highBasisPoints int64
	lowBasisPoints  int64
	minFreeBytes    int64
	committedBytes  int64
	reservedBytes   int64
	bypassObjects   int64
	bypassBytes     int64
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
		maxBytes:        maxBytes,
		highBasisPoints: int64(math.Round(highWatermark * 10_000)),
		lowBasisPoints:  int64(math.Round(lowWatermark * 10_000)),
		minFreeBytes:    minFreeBytes,
		committedBytes:  committedBytes,
	}
}

// Reserve returns nil when an object should be proxied without caching.
// A non-positive size is intentionally bypassed because it cannot be safely
// accounted for before a download starts.
func (c *Controller) Reserve(bytes, filesystemFreeBytes int64) *Reservation {
	c.mu.Lock()
	defer c.mu.Unlock()

	budgetUsed := saturatingAdd(c.committedBytes, c.reservedBytes)
	availableFilesystem := filesystemFreeBytes - min(c.reservedBytes, filesystemFreeBytes)
	if bytes <= 0 || (c.maxBytes > 0 && exceedsAvailable(c.maxBytes, budgetUsed, bytes)) ||
		(c.minFreeBytes > 0 && exceedsAvailable(availableFilesystem, c.minFreeBytes, bytes)) {
		c.bypassObjects = saturatingAdd(c.bypassObjects, 1)
		if bytes > 0 {
			c.bypassBytes = saturatingAdd(c.bypassBytes, bytes)
		}
		return nil
	}
	c.reservedBytes = saturatingAdd(c.reservedBytes, bytes)
	return &Reservation{controller: c, bytes: bytes}
}

func (c *Controller) RecordBypass(bytes int64) {
	c.mu.Lock()
	c.bypassObjects = saturatingAdd(c.bypassObjects, 1)
	if bytes > 0 {
		c.bypassBytes = saturatingAdd(c.bypassBytes, bytes)
	}
	c.mu.Unlock()
}

// Commit converts the reservation into committed usage. It is safe to call
// Commit or Release more than once; only the first call has an effect.
func (r *Reservation) Commit(actualBytes, replacedBytes int64) bool {
	if r == nil {
		return false
	}
	committed := false
	r.once.Do(func() {
		r.controller.mu.Lock()
		defer r.controller.mu.Unlock()
		r.controller.reservedBytes -= r.bytes
		if actualBytes >= 0 && actualBytes <= r.bytes && replacedBytes >= 0 && replacedBytes <= r.controller.committedBytes {
			r.controller.committedBytes -= replacedBytes
			r.controller.committedBytes = saturatingAdd(r.controller.committedBytes, actualBytes)
			committed = true
		}
	})
	return committed
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
	highBytes := scaleBasisPoints(c.maxBytes, c.highBasisPoints)
	lowBytes := scaleBasisPoints(c.maxBytes, c.lowBasisPoints)
	return Snapshot{
		MaxBytes:       c.maxBytes,
		HighBytes:      highBytes,
		LowBytes:       lowBytes,
		MinFreeBytes:   c.minFreeBytes,
		CommittedBytes: c.committedBytes,
		ReservedBytes:  c.reservedBytes,
		BypassObjects:  c.bypassObjects,
		BypassBytes:    c.bypassBytes,
		Pressure:       c.maxBytes > 0 && saturatingAdd(c.committedBytes, c.reservedBytes) >= highBytes,
	}
}

func exceedsAvailable(limit, used, requested int64) bool {
	return limit < 0 || used > limit || requested > limit-used
}

func saturatingAdd(left, right int64) int64 {
	if right > 0 && left > math.MaxInt64-right {
		return math.MaxInt64
	}
	return left + right
}

func scaleBasisPoints(value, basisPoints int64) int64 {
	return (value/10_000)*basisPoints + (value%10_000)*basisPoints/10_000
}
