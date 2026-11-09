package main

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"testing"
	"time"
)

func startPair(t *testing.T, srv, cli *Config) {
	t.Helper()
	go func() {
		if err := runServer(srv); err != nil {
			t.Log("server stopped:", err)
		}
	}()
	time.Sleep(300 * time.Millisecond)
	go func() {
		if err := runClient(cli); err != nil {
			t.Log("client stopped:", err)
		}
	}()
	time.Sleep(500 * time.Millisecond)
}

// relayRoundTrip pushes random bytes through the relay and checks they come back.
func relayRoundTrip(t *testing.T, localPort, size int) {
	t.Helper()
	payload := make([]byte, size)
	_, _ = rand.Read(payload)
	c, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", localPort), 5*time.Second)
	if err != nil {
		t.Fatalf("dial relay: %v", err)
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(20 * time.Second))
	go func() { _, _ = c.Write(payload) }()
	got := make([]byte, size)
	if _, err := io.ReadFull(c, got); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if !bytes.Equal(payload, got) {
		t.Fatal("payload corrupted")
	}
}

const testPSK = "a-sufficiently-long-test-psk"

// Every transport, in both directions of dialing, must carry a stream intact.
func TestTransportsBothDirections(t *testing.T) {
	for _, tr := range []string{"tls", "ws", "tcp", "stealth", "kcp", "quic", "grpc", "dns"} {
		for _, reverse := range []bool{false, true} {
			name := tr
			if reverse {
				name += "/reverse"
			}
			t.Run(name, func(t *testing.T) {
				svc := freePort(t)
				local := freePort(t)
				echoServer(t, svc)

				var addr string
				if tr == "kcp" || tr == "quic" || tr == "dns" {
					addr = fmt.Sprintf("127.0.0.1:%d", freeUDPPort(t))
				} else {
					addr = fmt.Sprintf("127.0.0.1:%d", freePort(t))
				}
				// the listening side is the server normally, the client in reverse
				lst := &Config{WSPath: "/w"}
				switch tr {
				case "tls", "ws":
					lst.Listen = addr
				case "tcp":
					lst.TCPListen = addr
				case "stealth":
					lst.StealthListen = addr
				case "kcp":
					lst.KCPListen = addr
				case "quic":
					lst.QUICListen = addr
				case "grpc":
					lst.GRPCListen = addr
				case "dns":
					lst.DNSListen = addr
				}
				ep := []Endpoint{{tr, addr}}

				srv := &Config{Mode: "server", PSK: testPSK, Targets: "127.0.0.1", ServerName: "www.example.com",
					Ports: []string{fmt.Sprint(svc)}, Reverse: reverse, WSPath: "/w"}
				cli := &Config{Mode: "client", PSK: testPSK, ServerName: "www.example.com", Fingerprint: "chrome",
					Insecure: true, Carriers: 2, Ports: []string{fmt.Sprintf("%d:%d", local, svc)}, Reverse: reverse, WSPath: "/w"}
				if reverse {
					cli.Listen, cli.TCPListen, cli.StealthListen, cli.KCPListen, cli.QUICListen = lst.Listen, lst.TCPListen, lst.StealthListen, lst.KCPListen, lst.QUICListen
					cli.GRPCListen = lst.GRPCListen
					cli.DNSListen = lst.DNSListen
					srv.Endpoints, srv.Insecure, srv.Fingerprint = ep, true, "chrome"
					// the listening client must be up before the server dials it
					go func() { _ = runClient(cli) }()
					time.Sleep(400 * time.Millisecond)
					go func() { _ = runServer(srv) }()
					time.Sleep(1500 * time.Millisecond)
				} else {
					srv.Listen, srv.TCPListen, srv.StealthListen, srv.KCPListen, srv.QUICListen = lst.Listen, lst.TCPListen, lst.StealthListen, lst.KCPListen, lst.QUICListen
					srv.GRPCListen = lst.GRPCListen
					srv.DNSListen = lst.DNSListen
					cli.Endpoints = ep
					startPair(t, srv, cli)
				}
				relayRoundTrip(t, local, 256*1024)
				relayRoundTrip(t, local, 1)
			})
		}
	}
}

// With the first endpoint dead, the client must fall over to the next one.
func TestEndpointFailover(t *testing.T) {
	svc := freePort(t)
	local := freePort(t)
	echoServer(t, svc)
	dead := freePort(t) // nothing listens here
	live := freePort(t)

	srv := &Config{Mode: "server", PSK: testPSK, TCPListen: fmt.Sprintf("127.0.0.1:%d", live),
		Targets: "127.0.0.1", Ports: []string{fmt.Sprint(svc)}}
	cli := &Config{Mode: "client", PSK: testPSK, Carriers: 1,
		Endpoints: []Endpoint{{"tls", fmt.Sprintf("127.0.0.1:%d", dead)}, {"tcp", fmt.Sprintf("127.0.0.1:%d", live)}},
		Ports:     []string{fmt.Sprintf("%d:%d", local, svc)}}
	startPair(t, srv, cli)
	relayRoundTrip(t, local, 4096)
	if got := stats.good.Load(); got != 1 {
		t.Fatalf("active endpoint is %d, want the second one (1)", got)
	}
}

// With proxy_protocol on both ends, the service must receive a PROXY v2 header
// naming the user's real address before the first byte of payload.
func TestProxyProtocolV2(t *testing.T) {
	svc := freePort(t)
	local := freePort(t)
	tunnelPort := freePort(t)

	got := make(chan []byte, 1)
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", svc))
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		buf := make([]byte, 16+12+5)
		_ = c.SetReadDeadline(time.Now().Add(10 * time.Second))
		n, _ := io.ReadFull(c, buf)
		got <- buf[:n]
	}()

	srv := &Config{Mode: "server", PSK: testPSK, TCPListen: fmt.Sprintf("127.0.0.1:%d", tunnelPort),
		Targets: "127.0.0.1", Ports: []string{fmt.Sprint(svc)}, ProxyProtocol: true}
	cli := &Config{Mode: "client", PSK: testPSK, Carriers: 1, ProxyProtocol: true,
		Endpoints: []Endpoint{{"tcp", fmt.Sprintf("127.0.0.1:%d", tunnelPort)}},
		Ports:     []string{fmt.Sprintf("%d:%d", local, svc)}}
	startPair(t, srv, cli)

	c, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", local))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	userPort := c.LocalAddr().(*net.TCPAddr).Port
	_, _ = c.Write([]byte("hello"))

	select {
	case b := <-got:
		if len(b) < 33 || !bytes.Equal(b[:12], proxyV2Sig) {
			t.Fatalf("no PROXY v2 signature: %x", b)
		}
		if b[12] != 0x21 || b[13] != 0x11 {
			t.Fatalf("want v2 PROXY over TCP4, got %#x %#x", b[12], b[13])
		}
		src := net.IP(b[16:20])
		sport := int(b[24])<<8 | int(b[25])
		if !src.Equal(net.IPv4(127, 0, 0, 1)) || sport != userPort {
			t.Fatalf("header names %s:%d, want 127.0.0.1:%d", src, sport, userPort)
		}
		if string(b[28:33]) != "hello" {
			t.Fatalf("payload after header = %q", b[28:33])
		}
	case <-time.After(15 * time.Second):
		t.Fatal("service never received the connection")
	}
}

// The stats endpoint must report a live carrier with a measured path and the
// traffic that crossed it.
func TestStatsEndpoint(t *testing.T) {
	svc := freePort(t)
	local := freePort(t)
	tunnelPort := freePort(t)
	statsPort := freePort(t)
	echoServer(t, svc)

	srv := &Config{Mode: "server", PSK: testPSK, TCPListen: fmt.Sprintf("127.0.0.1:%d", tunnelPort),
		Targets: "127.0.0.1", Ports: []string{fmt.Sprint(svc)}}
	cli := &Config{Mode: "client", PSK: testPSK, Carriers: 1, Name: "t1",
		Endpoints:   []Endpoint{{"tcp", fmt.Sprintf("127.0.0.1:%d", tunnelPort)}},
		Ports:       []string{fmt.Sprintf("%d:%d", local, svc)},
		StatsListen: fmt.Sprintf("127.0.0.1:%d", statsPort)}
	startPair(t, srv, cli)
	relayRoundTrip(t, local, 10000)

	resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/stats", statsPort))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var d statsDoc
	if err := json.NewDecoder(bufio.NewReader(resp.Body)).Decode(&d); err != nil {
		t.Fatal(err)
	}
	if d.Name != "t1" || d.Mode != "client" || len(d.Carriers) == 0 {
		t.Fatalf("unexpected stats: %+v", d)
	}
	var seen bool
	for _, p := range d.Ports {
		if p.Proto == "tcp" && p.Port == svc && p.Up >= 10000 && p.Down >= 10000 {
			seen = true
		}
	}
	if !seen {
		t.Fatalf("port %d traffic not counted: %+v", svc, d.Ports)
	}
}

func TestStatsListenMustBeLoopback(t *testing.T) {
	c := &Config{Mode: "client", PSK: testPSK, Server: "1.2.3.4:443", StatsListen: "0.0.0.0:9000"}
	if err := c.normalize(); err == nil {
		t.Fatal("a non-loopback stats address was accepted")
	}
}

// An unauthenticated TLS connection (an HTTP probe) must be answered by the
// decoy site, not dropped - the prober should see an ordinary web response.
func TestDecoyServesProbes(t *testing.T) {
	// a stand-in "decoy website"
	decoy, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer decoy.Close()
	go func() {
		for {
			c, err := decoy.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				b := make([]byte, 256)
				n, _ := c.Read(b)
				if n > 0 { // echo a recognizable banner plus what was sent
					_, _ = c.Write(append([]byte("DECOY:"), b[:n]...))
				}
			}()
		}
	}()

	svc := freePort(t)
	tunnelPort := freePort(t)
	echoServer(t, svc)
	srv := &Config{Mode: "server", PSK: testPSK, Listen: fmt.Sprintf("127.0.0.1:%d", tunnelPort),
		Targets: "127.0.0.1", ServerName: "www.example.com", Ports: []string{fmt.Sprint(svc)},
		Decoy: decoy.Addr().String()}
	go func() { _ = runServer(srv) }()
	time.Sleep(300 * time.Millisecond)

	// connect with TLS but speak gibberish instead of the tunnel handshake
	conn, err := tls.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", tunnelPort), &tls.Config{InsecureSkipVerify: true, ServerName: "www.example.com"})
	if err != nil {
		t.Fatalf("tls dial: %v", err)
	}
	defer conn.Close()
	_, _ = conn.Write([]byte("GET / HTTP/1.1\r\nHost: x\r\n\r\n"))
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	buf := make([]byte, 128)
	n, err := conn.Read(buf)
	if err != nil {
		t.Fatalf("decoy did not answer: %v", err)
	}
	if !bytes.HasPrefix(buf[:n], []byte("DECOY:GET /")) {
		t.Fatalf("probe was not sent to the decoy with its bytes: %q", buf[:n])
	}
}

// Without a decoy, an unauthenticated connection is simply dropped.
func TestNoDecoyDropsProbes(t *testing.T) {
	svc := freePort(t)
	tunnelPort := freePort(t)
	echoServer(t, svc)
	srv := &Config{Mode: "server", PSK: testPSK, Listen: fmt.Sprintf("127.0.0.1:%d", tunnelPort),
		Targets: "127.0.0.1", ServerName: "www.example.com", Ports: []string{fmt.Sprint(svc)}}
	go func() { _ = runServer(srv) }()
	time.Sleep(300 * time.Millisecond)

	conn, err := tls.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", tunnelPort), &tls.Config{InsecureSkipVerify: true, ServerName: "www.example.com"})
	if err != nil {
		t.Fatalf("tls dial: %v", err)
	}
	defer conn.Close()
	_, _ = conn.Write([]byte("GET / HTTP/1.1\r\n\r\n"))
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	if n, err := conn.Read(make([]byte, 64)); err == nil && n > 0 {
		t.Fatalf("a probe got %d bytes back where it should get nothing", n)
	}
}

// The stealth carrier must put nothing recognizable on the wire: no TLS
// ClientHello and no cleartext magic - only random-looking bytes.
func TestStealthWireHasNoMagic(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	captured := make(chan []byte, 1)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		buf := make([]byte, 128)
		n, _ := c.Read(buf)
		captured <- buf[:n]
	}()
	raw, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	go func() {
		sc, err := newStealthConn(raw, testPSK, true, false)
		if err == nil {
			_, _ = sc.Write([]byte("MIDOSV1 and some payload that must never appear in clear"))
		}
	}()
	select {
	case first := <-captured:
		if bytes.Contains(first, []byte("MIDOSV1")) || bytes.Contains(first, []byte("payload")) {
			t.Fatalf("stealth leaked plaintext on the wire: %q", first)
		}
		if len(first) < stealthSaltLen {
			t.Fatalf("expected at least a salt, got %d bytes", len(first))
		}
	case <-time.After(5 * time.Second):
		t.Fatal("nothing was sent")
	}
}

// KCP with forward error correction turned off (0 parity shards) is the lean
// UDP carrier: KCP's reliability without the FEC overhead. It must still carry
// a stream intact.
func TestLeanUDPNoFEC(t *testing.T) {
	svc := freePort(t)
	local := freePort(t)
	addr := fmt.Sprintf("127.0.0.1:%d", freeUDPPort(t))
	echoServer(t, svc)
	srv := &Config{Mode: "server", PSK: testPSK, KCPListen: addr, Targets: "127.0.0.1",
		Ports: []string{fmt.Sprint(svc)}, KCPDataShards: 10, KCPParityShards: 0}
	cli := &Config{Mode: "client", PSK: testPSK, Carriers: 2,
		Endpoints: []Endpoint{{"kcp", addr}}, Ports: []string{fmt.Sprintf("%d:%d", local, svc)},
		KCPDataShards: 10, KCPParityShards: 0}
	startPair(t, srv, cli)
	relayRoundTrip(t, local, 128*1024)
}

func TestMuxV2EndToEnd(t *testing.T) {
	for _, reverse := range []bool{false, true} {
		name := "direct"
		if reverse {
			name = "reverse"
		}
		t.Run(name, func(t *testing.T) {
			svc := freePort(t)
			local := freePort(t)
			echoServer(t, svc)
			addr := fmt.Sprintf("127.0.0.1:%d", freePort(t))
			ep := []Endpoint{{"tls", addr}}
			srv := &Config{Mode: "server", PSK: testPSK, Targets: "127.0.0.1", ServerName: "www.example.com",
				Ports: []string{fmt.Sprint(svc)}, Reverse: reverse, MuxVersion: 2}
			cli := &Config{Mode: "client", PSK: testPSK, ServerName: "www.example.com", Fingerprint: "chrome",
				Insecure: true, Carriers: 2, Ports: []string{fmt.Sprintf("%d:%d", local, svc)}, Reverse: reverse, MuxVersion: 2}
			if reverse {
				cli.Listen = addr
				srv.Endpoints, srv.Insecure, srv.Fingerprint = ep, true, "chrome"
				go func() { _ = runClient(cli) }()
				time.Sleep(400 * time.Millisecond)
				go func() { _ = runServer(srv) }()
				time.Sleep(1500 * time.Millisecond)
			} else {
				srv.Listen = addr
				cli.Endpoints = ep
				startPair(t, srv, cli)
			}
			relayRoundTrip(t, local, 4<<20)
			relayRoundTrip(t, local, 1)
		})
	}
}
