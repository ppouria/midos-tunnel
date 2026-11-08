package main

// Live statistics, served as a small JSON document on a loopback address for
// the web panel. Nothing here is sampled or estimated: carrier RTT and
// retransmissions come from the transport itself (the kernel's TCP_INFO, KCP's
// or QUIC's own estimator), and traffic is counted as it is copied.

import (
	"encoding/json"
	"io"
	"log"
	"net/http"
	"sort"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

type counters struct {
	up, down      atomic.Uint64 // bytes towards the service / towards the user
	total, active atomic.Int64  // streams (TCP connections or UDP flows)
}

type tunnelStats struct {
	start time.Time
	good  atomic.Int32 // dialing side: index of the endpoint in use

	mu        sync.Mutex
	carriers  map[*carrier]struct{}
	ports     map[string]*counters
	lastErr   string
	lastErrAt time.Time
}

var stats = &tunnelStats{
	start:    time.Now(),
	carriers: map[*carrier]struct{}{},
	ports:    map[string]*counters{},
}

func (s *tunnelStats) addCarrier(c *carrier) {
	s.mu.Lock()
	s.carriers[c] = struct{}{}
	s.mu.Unlock()
}

func (s *tunnelStats) removeCarrier(c *carrier) {
	s.mu.Lock()
	delete(s.carriers, c)
	s.mu.Unlock()
}

func (s *tunnelStats) port(proto string, port uint16) *counters {
	key := proto + "/" + strconv.Itoa(int(port))
	s.mu.Lock()
	defer s.mu.Unlock()
	c := s.ports[key]
	if c == nil {
		c = &counters{}
		s.ports[key] = c
	}
	return c
}

func (s *tunnelStats) setError(err error) {
	s.mu.Lock()
	s.lastErr, s.lastErrAt = err.Error(), time.Now()
	s.mu.Unlock()
}

type carrierDoc struct {
	Transport string `json:"transport"`
	Peer      string `json:"peer"`
	UpSince   int64  `json:"up_since"`
	linkInfo
	Measured bool `json:"measured"`
}

type portDoc struct {
	Proto   string `json:"proto"`
	Port    int    `json:"port"`
	Up      uint64 `json:"bytes_up"`
	Down    uint64 `json:"bytes_down"`
	Streams int64  `json:"streams_total"`
	Active  int64  `json:"streams_active"`
}

type statsDoc struct {
	Name        string       `json:"name"`
	Version     string       `json:"version"`
	Mode        string       `json:"mode"`
	Reverse     bool         `json:"reverse"`
	UptimeSec   int64        `json:"uptime_s"`
	Endpoint    *Endpoint    `json:"endpoint,omitempty"`
	Endpoints   []Endpoint   `json:"endpoints,omitempty"`
	Carriers    []carrierDoc `json:"carriers"`
	Ports       []portDoc    `json:"ports"`
	LastError   string       `json:"last_error,omitempty"`
	LastErrorAt int64        `json:"last_error_at,omitempty"`
}

func (s *tunnelStats) snapshot(cfg *Config) statsDoc {
	d := statsDoc{
		Name: cfg.Name, Version: version, Mode: cfg.Mode, Reverse: cfg.Reverse,
		UptimeSec: int64(time.Since(s.start).Seconds()),
		Carriers:  []carrierDoc{}, Ports: []portDoc{},
	}
	if cfg.dials() && len(cfg.Endpoints) > 0 {
		ep := cfg.Endpoints[int(s.good.Load())%len(cfg.Endpoints)]
		d.Endpoint, d.Endpoints = &ep, cfg.Endpoints
	}

	s.mu.Lock()
	cs := make([]*carrier, 0, len(s.carriers))
	for c := range s.carriers {
		cs = append(cs, c)
	}
	for key, c := range s.ports {
		proto, num := key[:3], key[4:]
		n, _ := strconv.Atoi(num)
		d.Ports = append(d.Ports, portDoc{Proto: proto, Port: n,
			Up: c.up.Load(), Down: c.down.Load(), Streams: c.total.Load(), Active: c.active.Load()})
	}
	if s.lastErr != "" {
		d.LastError, d.LastErrorAt = s.lastErr, s.lastErrAt.Unix()
	}
	s.mu.Unlock()

	// probing reads socket state, so it runs outside the lock
	for _, c := range cs {
		li := c.link()
		d.Carriers = append(d.Carriers, carrierDoc{Transport: c.ep.Transport, Peer: c.ep.Server,
			UpSince: c.since.Unix(), linkInfo: li, Measured: li.known})
	}
	sort.Slice(d.Carriers, func(i, j int) bool { return d.Carriers[i].UpSince < d.Carriers[j].UpSince })
	sort.Slice(d.Ports, func(i, j int) bool {
		if d.Ports[i].Port != d.Ports[j].Port {
			return d.Ports[i].Port < d.Ports[j].Port
		}
		return d.Ports[i].Proto < d.Ports[j].Proto
	})
	return d
}

// serveStats runs the status endpoint if one is configured. A failure here is
// logged and otherwise ignored: losing the statistics must never stop relaying.
func serveStats(cfg *Config) {
	if cfg.StatsListen == "" {
		return
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/stats", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(stats.snapshot(cfg))
	})
	srv := &http.Server{Addr: cfg.StatsListen, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	if err := srv.ListenAndServe(); err != nil {
		log.Printf("stats endpoint: %v", err)
	}
}

// countWriter adds every byte written through it to n.
type countWriter struct {
	w io.Writer
	n *atomic.Uint64
}

func (c countWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.n.Add(uint64(n))
	return n, err
}

// pipeCounted is pipe with traffic accounting: user is the side facing the
// user, svc the side facing the service.
func pipeCounted(user, svc io.ReadWriteCloser, c *counters, stall time.Duration) {
	c.total.Add(1)
	c.active.Add(1)
	defer c.active.Add(-1)
	done := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(stallGuard(countWriter{svc, &c.up}, svc, stall), user); done <- struct{}{} }()
	go func() { _, _ = io.Copy(stallGuard(countWriter{user, &c.down}, user, stall), svc); done <- struct{}{} }()
	<-done
	_ = user.Close()
	_ = svc.Close()
}
