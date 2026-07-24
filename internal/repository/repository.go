package repository

import "net/http"

type Handler interface {
	http.Handler
	Snapshot() Snapshot
	SetupSnippet() string
}

type Snapshot struct {
	Name          string  `json:"name"`
	Type          string  `json:"type"`
	Path          string  `json:"path"`
	Upstream      string  `json:"upstream"`
	Requests      uint64  `json:"requests"`
	CacheHits     uint64  `json:"cache_hits"`
	CacheMisses   uint64  `json:"cache_misses"`
	Errors        uint64  `json:"errors"`
	RangeRequests uint64  `json:"range_requests"`
	HitRatio      float64 `json:"hit_ratio"`
	StorageBytes  int64   `json:"storage_bytes"`
	CacheObjects  int64   `json:"cache_objects"`
}
