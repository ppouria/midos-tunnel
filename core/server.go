package main

// The side abroad: it authenticates carriers, accepts the streams they carry,
// and connects each one to the service it names.

import (
	"bufio"
	"crypto/tls"
	"log"
	"net"
	"time"

	"github.com/xtaci/smux"
)

func runServer(cfg *Config) error {
	if err := cfg.normalize(); err != nil {
		return err
	}
	maps, err := cfg.parsePorts()
	if err != nil {
		return err
	}
	// Only ports the operator listed may be reached. Without this the peer
	// could ask for any port on the host, turning the relay into an open proxy
	// into the server's own private services.
	// remote port -> where to dial it. The host defaults to cfg.Targets, but a
	// "port=host:port" entry can send one port somewhere else.
	allowed := make(map[uint16]string, len(maps))
	for _, m := range maps {
		allowed[m.remote] = m.dest(cfg.Targets)
	}
	udpMaps, err := cfg.parseUDPPorts()
	if err != nil {
		return err
	}
	// UDP ports are allowed separately: a TCP port in the list does not open
	// the same number for UDP, and the other way round.
	allowedUDP := make(map[uint16]string, len(udpMaps))
	for _, m := range udpMaps {
		allowedUDP[m.remote] = m.dest(cfg.Targets)
	}
	go serveStats(cfg)
	log.Printf("tunnel server: %d tcp + %d udp port(s) allowed, targets at %s", len(allowed), len(allowedUDP), cfg.Targets)

	if cfg.dials() {
		return runReverseServer(cfg, allowed, allowedUDP)
	}
	return listenCarriers(cfg, func(c *carrier) {
		if auth.ok(cfg, c) {
			serveCarrier(cfg, c, allowed, allowedUDP)
		}
	})
}

// runReverseServer keeps cfg.Carriers carriers open towards the Iran side. The
// side abroad then needs no inbound port at all: every connection it has, it
// opened itself.
func runReverseServer(cfg *Config, allowed, allowedUDP map[uint16]string) error {
	log.Printf("reverse mode: opening %d carrier(s) towards the client", cfg.Carriers)
	for i := 0; i < cfg.Carriers; i++ {
		go func() {
			backoff := time.Second
			for {
				c, err := dialAuthenticated(cfg, &stats.good)
				if err != nil {
					stats.setError(err)
					log.Printf("carrier: %v (retrying in %s)", err, backoff)
					time.Sleep(backoff)
					backoff = min(backoff*2, 30*time.Second)
					continue
				}
				backoff = time.Second
				serveCarrier(cfg, c, allowed, allowedUDP)
			}
		}()
	}
	select {}
}

// rejectSlowly drops a peer that failed authentication. It does not say which
// part failed and does not answer quickly: an unauthenticated peer should
// learn as little as possible about what is listening here.
func rejectSlowly(c *carrier, err error) {
	peer := c.RemoteAddr().String()
	time.Sleep(time.Duration(200+len(peer)%300) * time.Millisecond)
	log.Printf("rejected %s: %v", peer, err)
	_ = c.Close()
}

// serveCarrier runs the multiplexer on an authenticated carrier and serves its
// streams until the carrier dies.
func serveCarrier(cfg *Config, c *carrier, allowed, allowedUDP map[uint16]string) {
	defer c.Close()
	sess, err := smux.Server(c, smuxConfig(cfg))
	if err != nil {
		log.Printf("mux for %s: %v", c.ep.Server, err)
		return
	}
	defer sess.Close()
	stats.addCarrier(c)
	defer stats.removeCarrier(c)

	for {
		st, err := sess.AcceptStream()
		if err != nil {
			return
		}
		go handleStream(cfg, st, allowed, allowedUDP)
	}
}

func newBufReader(c net.Conn) *bufio.Reader { return bufio.NewReaderSize(c, 64<<10) }

// bufConn is a net.Conn whose reads come from a bufio.Reader, so bytes already
// peeked during the sniff below are not lost to whoever reads next.
type bufConn struct {
	net.Conn
	r *bufio.Reader
}

func (b bufConn) Read(p []byte) (int, error) { return b.r.Read(p) }

// openTLSSession completes the TLS handshake and tells the two kinds of session
// apart. A plain tunnel client opens with our magic; a CDN forwarding a WebSocket
// opens with an HTTP request. With a WebSocket path configured we peek the
// first bytes to decide; without one, every carrier is a plain tunnel.
func openTLSSession(cfg *Config, raw net.Conn, tc *tls.Config) (net.Conn, error) {
	c := tls.Server(raw, tc)
	_ = c.SetDeadline(time.Now().Add(authTimeout))
	if err := c.Handshake(); err != nil {
		return nil, err
	}
	_ = c.SetDeadline(time.Time{})
	if cfg.WSPath == "" {
		return c, nil
	}
	br := newBufReader(c)
	_ = c.SetReadDeadline(time.Now().Add(wsSniffWait))
	head, err := br.Peek(len(protoMagic))
	_ = c.SetReadDeadline(time.Time{})
	if err != nil {
		// a silent or very short peer is not a WebSocket: let the tunnel
		// handshake (and the decoy) judge it, reading from the buffer
		return bufConn{c, br}, nil
	}
	if string(head) == protoMagic {
		return bufConn{c, br}, nil // plain tunnel; keep the magic for the handshake
	}
	return wsServerUpgrade(c, br, cfg.WSPath)
}

// wsSniffWait is how long a fresh TLS session may stay silent before it is
// taken for a plain tunnel client; it delays only that client's first session.
const wsSniffWait = 700 * time.Millisecond

func handleStream(cfg *Config, st *smux.Stream, allowed, allowedUDP map[uint16]string) {
	defer st.Close()

	_ = st.SetReadDeadline(time.Now().Add(10 * time.Second))
	kind, port, user, err := readStreamOpen(st)
	if err != nil {
		return
	}
	_ = st.SetReadDeadline(time.Time{})

	if kind == kindUDP {
		dst, ok := allowedUDP[port]
		if !ok {
			log.Printf("refused udp stream to port %d (not in the allowed list)", port)
			return
		}
		handleUDPStream(dst, st, port)
		return
	}
	target, ok := allowed[port]
	if !ok {
		log.Printf("refused stream to port %d (not in the allowed list)", port)
		return
	}

	out, err := net.DialTimeout("tcp", target, 10*time.Second)
	if err != nil {
		log.Printf("dial %s: %v", target, err)
		return
	}
	defer out.Close()
	if t, ok := out.(*net.TCPConn); ok {
		_ = t.SetNoDelay(true)
	}
	// The address is honoured only when this side is configured for it too: a
	// service that does not expect the header would read it as garbage.
	if cfg.ProxyProtocol && user != nil {
		if dst, ok := out.RemoteAddr().(*net.TCPAddr); ok {
			if err := writeProxyV2(out, user, dst); err != nil {
				return
			}
		}
	}
	pipeCounted(st, out, stats.port("tcp", port), cfg.stallLimit())
}
