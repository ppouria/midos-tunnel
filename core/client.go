package main

// The Iran side: the user-facing listeners, and the pool of carriers that their
// connections ride on.

import (
	"errors"
	"fmt"
	"io"
	"log"
	"math/rand"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/xtaci/smux"
)

// session is one multiplexed carrier.
type session struct {
	*smux.Session
	c *carrier
}

// carrierPool keeps a few long-lived carriers open and hands out streams.
//
// One carrier per user connection would be both slow (a handshake each time)
// and conspicuous (a burst of identical short-lived connections). A small pool
// of long-lived carriers carrying many multiplexed streams looks far more like
// a browser talking to a website.
//
// When this side dials, the pool owns a fixed number of slots and redials a
// slot whose carrier died. In reverse mode the server opens the carriers, and
// the pool simply uses whichever of them are alive.
type carrierPool struct {
	cfg  *Config
	next atomic.Uint64

	mu       sync.Mutex
	slots    []*session   // dialing: one per configured carrier
	dialing  []sync.Mutex // dialing: at most one dial per slot at a time
	accepted []*session   // reverse: carriers the server opened towards us
	ready    chan struct{}
}

// reverseWait is how long a user connection waits for the server to open a
// carrier in reverse mode before it is refused.
const reverseWait = 10 * time.Second

func newCarrierPool(cfg *Config) *carrierPool {
	p := &carrierPool{cfg: cfg, ready: make(chan struct{}, 1)}
	if cfg.dials() {
		p.slots = make([]*session, cfg.Carriers)
		p.dialing = make([]sync.Mutex, cfg.Carriers)
	}
	return p
}

const muxV2RecvBuffer = 1 << 30

func smuxConfig(cfg *Config) *smux.Config {
	t := cfg.tune()
	c := smux.DefaultConfig()
	c.KeepAliveInterval = 20 * time.Second
	c.KeepAliveTimeout = 60 * time.Second
	c.MaxReceiveBuffer = t.recvBuf
	c.MaxStreamBuffer = t.streamBuf
	if cfg.MuxVersion == 2 {
		c.Version = 2
		c.MaxReceiveBuffer = muxV2RecvBuffer
	}
	return c
}

// stream returns a multiplexed stream on a live carrier.
func (p *carrierPool) stream() (*smux.Stream, error) {
	if !p.cfg.dials() {
		return p.acceptedStream()
	}
	idx := int(p.next.Add(1) % uint64(len(p.slots)))
	if st := p.tryOpen(idx); st != nil {
		return st, nil
	}
	// Only one caller redials a dead slot; the rest wait for its result. With
	// a path that swallows handshakes, every user connection dialing on its
	// own would pile up sockets faster than the timeouts could clear them.
	p.dialing[idx].Lock()
	defer p.dialing[idx].Unlock()
	if st := p.tryOpen(idx); st != nil {
		return st, nil
	}
	c, err := dialAuthenticated(p.cfg, &stats.good)
	if err != nil {
		stats.setError(err)
		return nil, err
	}
	s, err := newSession(p.cfg, c)
	if err != nil {
		return nil, err
	}
	p.mu.Lock()
	if old := p.slots[idx]; old != nil {
		_ = old.Close()
	}
	p.slots[idx] = s
	p.mu.Unlock()
	return s.OpenStream()
}

// tryOpen opens a stream on the slot's carrier if it is alive.
func (p *carrierPool) tryOpen(idx int) *smux.Stream {
	p.mu.Lock()
	s := p.slots[idx]
	p.mu.Unlock()
	if s == nil || s.IsClosed() {
		return nil
	}
	st, err := s.OpenStream()
	if err != nil {
		// the session answered but refused a stream: treat it as dead
		_ = s.Close()
		return nil
	}
	return st
}

// newSession puts the multiplexer on an authenticated carrier this side uses
// to open streams.
func newSession(cfg *Config, c *carrier) (*session, error) {
	ns, err := smux.Client(c, smuxConfig(cfg))
	if err != nil {
		_ = c.Close()
		return nil, err
	}
	stats.addCarrier(c)
	go func() {
		<-ns.CloseChan()
		stats.removeCarrier(c)
	}()
	return &session{Session: ns, c: c}, nil
}

func (p *carrierPool) addAccepted(s *session) {
	p.mu.Lock()
	p.accepted = append(p.accepted, s)
	p.mu.Unlock()
	select {
	case p.ready <- struct{}{}:
	default:
	}
}

func (p *carrierPool) acceptedStream() (*smux.Stream, error) {
	deadline := time.Now().Add(reverseWait)
	for {
		p.mu.Lock()
		live := p.accepted[:0]
		for _, s := range p.accepted {
			if !s.IsClosed() {
				live = append(live, s)
			}
		}
		p.accepted = live
		var s *session
		if len(live) > 0 {
			s = live[int(p.next.Add(1)%uint64(len(live)))]
		}
		p.mu.Unlock()

		if s != nil {
			if st, err := s.OpenStream(); err == nil {
				return st, nil
			}
			_ = s.Close()
			continue
		}
		wait := time.Until(deadline)
		if wait <= 0 {
			return nil, errors.New("no carrier from the server")
		}
		select {
		case <-p.ready:
		case <-time.After(wait):
		}
	}
}

// rotateLoop recycles each dialing carrier on a jittered interval, so no single
// connection stays open long enough to stand out as a persistent tunnel. A new
// carrier is dialed and swapped in first; the old one is closed after a grace
// period, so streams already running on it are not cut.
func (p *carrierPool) rotateLoop(cfg *Config) {
	base := time.Duration(cfg.RotateSec) * time.Second
	for i := range p.slots {
		go func(idx int) {
			for {
				time.Sleep(time.Duration(float64(base) * (0.7 + 0.6*rand.Float64())))
				p.rotateSlot(cfg, idx)
			}
		}(i)
	}
}

func (p *carrierPool) rotateSlot(cfg *Config, idx int) {
	c, err := dialAuthenticated(cfg, &stats.good)
	if err != nil {
		return // keep the current carrier; try again next interval
	}
	s, err := newSession(cfg, c)
	if err != nil {
		return
	}
	p.mu.Lock()
	old := p.slots[idx]
	p.slots[idx] = s
	p.mu.Unlock()
	if old != nil {
		time.AfterFunc(30*time.Second, func() { _ = old.Close() })
	}
}

func (p *carrierPool) closeAll() {
	p.mu.Lock()
	defer p.mu.Unlock()
	for i, s := range p.slots {
		if s != nil {
			_ = s.Close()
			p.slots[i] = nil
		}
	}
	for _, s := range p.accepted {
		_ = s.Close()
	}
	p.accepted = nil
}

func runClient(cfg *Config) error {
	if err := cfg.normalize(); err != nil {
		return err
	}
	maps, err := cfg.parsePorts()
	if err != nil {
		return err
	}
	udpMaps, err := cfg.parseUDPPorts()
	if err != nil {
		return err
	}
	pool := newCarrierPool(cfg)
	defer pool.closeAll()
	if cfg.dials() && cfg.RotateSec > 0 {
		go pool.rotateLoop(cfg)
	}
	go serveStats(cfg)

	errCh := make(chan error, len(maps)+len(udpMaps)+1)
	if !cfg.dials() {
		log.Printf("reverse mode: waiting for the server to open carriers")
		go func() {
			errCh <- listenCarriers(cfg, func(c *carrier) {
				if !auth.ok(cfg, c) {
					return
				}
				s, err := newSession(cfg, c)
				if err != nil {
					return
				}
				log.Printf("carrier from %s (%s)", c.ep.Server, c.ep.Transport)
				pool.addAccepted(s)
			})
		}()
	}

	for _, m := range udpMaps {
		go func(m portMap) { errCh <- serveUDPPort(pool, m) }(m)
	}
	for _, m := range maps {
		ln, err := net.Listen("tcp", fmt.Sprintf(":%d", m.local))
		if err != nil {
			return fmt.Errorf("listen %d: %w", m.local, err)
		}
		log.Printf("relaying :%d -> remote %d", m.local, m.remote)
		go func(ln net.Listener, m portMap) {
			defer ln.Close()
			for {
				c, err := ln.Accept()
				if err != nil {
					errCh <- err
					return
				}
				go serveLocal(cfg, pool, c, m.remote)
			}
		}(ln, m)
	}
	return <-errCh
}

func serveLocal(cfg *Config, pool *carrierPool, local net.Conn, remote uint16) {
	defer local.Close()
	if t, ok := local.(*net.TCPConn); ok {
		_ = t.SetNoDelay(true)
	}
	st, err := pool.stream()
	if err != nil {
		log.Printf("no carrier for :%d: %v", remote, err)
		return
	}
	defer st.Close()
	var user net.Addr
	if cfg.ProxyProtocol {
		user = local.RemoteAddr()
	}
	if err := writeTCPOpen(st, remote, user); err != nil {
		return
	}
	pipeCounted(local, st, stats.port("tcp", remote), cfg.stallLimit())
}

// pipe copies in both directions and returns when either side is done.
func pipe(a, b io.ReadWriteCloser) {
	done := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(a, b); done <- struct{}{} }()
	go func() { _, _ = io.Copy(b, a); done <- struct{}{} }()
	<-done
	_ = a.Close()
	_ = b.Close()
}
