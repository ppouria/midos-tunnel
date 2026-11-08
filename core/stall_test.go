package main

import (
	"io"
	"net"
	"testing"
	"time"

	"github.com/xtaci/smux"
)

func loopbackPair(t *testing.T) (net.Conn, net.Conn) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	type res struct {
		c   net.Conn
		err error
	}
	ch := make(chan res, 1)
	go func() {
		c, err := ln.Accept()
		ch <- res{c, err}
	}()
	a, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	r := <-ch
	if r.err != nil {
		t.Fatal(r.err)
	}
	t.Cleanup(func() { a.Close(); r.c.Close() })
	return a, r.c
}

func echoAfterStall(t *testing.T, stall time.Duration, wait time.Duration, version int) bool {
	t.Helper()
	cfg := smux.DefaultConfig()
	cfg.Version = version
	cfg.MaxReceiveBuffer = 256 << 10
	cfg.MaxStreamBuffer = 64 << 10
	cfg.KeepAliveDisabled = true
	ca, cb := loopbackPair(t)
	sender, err := smux.Client(ca, cfg)
	if err != nil {
		t.Fatal(err)
	}
	receiver, err := smux.Server(cb, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer sender.Close()
	defer receiver.Close()

	userNear, userFar := loopbackPair(t)
	userNear.(*net.TCPConn).SetWriteBuffer(4096)
	userFar.(*net.TCPConn).SetReadBuffer(4096)

	slowA, err := sender.OpenStream()
	if err != nil {
		t.Fatal(err)
	}
	go io.CopyN(slowA, zeroReader{}, 16<<20)
	slowB, err := receiver.AcceptStream()
	if err != nil {
		t.Fatal(err)
	}
	go pipeCounted(userNear, slowB, &counters{}, stall)

	time.Sleep(500 * time.Millisecond)

	opened := make(chan *smux.Stream, 1)
	go func() {
		st, err := sender.OpenStream()
		if err != nil {
			opened <- nil
			return
		}
		opened <- st
	}()
	go func() {
		probeB, err := receiver.AcceptStream()
		if err == nil {
			io.Copy(probeB, probeB)
		}
	}()
	var probeA *smux.Stream
	select {
	case probeA = <-opened:
	case <-time.After(wait):
		return false
	}
	if probeA == nil {
		return false
	}
	probeA.Write([]byte("ping"))
	probeA.SetReadDeadline(time.Now().Add(wait))
	buf := make([]byte, 4)
	_, err = io.ReadFull(probeA, buf)
	return err == nil && string(buf) == "ping"
}

type zeroReader struct{}

func (zeroReader) Read(p []byte) (int, error) { return len(p), nil }

func TestStalledStreamBlocksCarrierWithoutGuard(t *testing.T) {
	if echoAfterStall(t, 0, 4*time.Second, 1) {
		t.Fatal("expected the stalled stream to block the whole carrier when the guard is off")
	}
}

func TestStallGuardFreesCarrier(t *testing.T) {
	if !echoAfterStall(t, 2*time.Second, 10*time.Second, 1) {
		t.Fatal("a stalled stream must be closed so other streams on the carrier keep working")
	}
}

func TestStallLimit(t *testing.T) {
	cases := []struct {
		sec  int
		want time.Duration
	}{{0, defaultStall}, {-1, 0}, {30, 30 * time.Second}}
	for _, c := range cases {
		if got := (&Config{StallSec: c.sec}).stallLimit(); got != c.want {
			t.Fatalf("stall_sec=%d: got %s want %s", c.sec, got, c.want)
		}
	}
}

func TestMuxV2SlowStreamDoesNotBlockCarrier(t *testing.T) {
	if !echoAfterStall(t, 0, 4*time.Second, 2) {
		t.Fatal("with per-stream flow control a slow stream must not block other streams, even without the stall guard")
	}
}

func TestMuxVersionConfig(t *testing.T) {
	v1 := smuxConfig(&Config{Preset: "turbo"})
	if v1.Version != 1 || v1.MaxReceiveBuffer != 16<<20 {
		t.Fatalf("default must stay on v1 with the preset buffer: %+v", v1)
	}
	v2 := smuxConfig(&Config{Preset: "turbo", MuxVersion: 2})
	if v2.Version != 2 || v2.MaxReceiveBuffer != muxV2RecvBuffer || v2.MaxStreamBuffer != 2<<20 {
		t.Fatalf("v2 must keep the stream window and lift the session cap: %+v", v2)
	}
	if err := smux.VerifyConfig(v2); err != nil {
		t.Fatal(err)
	}
	for _, v := range []int{0, 1, 2} {
		c := &Config{Mode: "client", PSK: testPSK, Server: "1.2.3.4:4000", Transport: "kcp", Ports: []string{"443"}, MuxVersion: v}
		if err := c.normalize(); err != nil {
			t.Fatalf("mux_version %d must be accepted: %v", v, err)
		}
	}
	for _, v := range []int{-1, 3} {
		c := &Config{Mode: "client", PSK: testPSK, Server: "1.2.3.4:4000", Transport: "kcp", Ports: []string{"443"}, MuxVersion: v}
		if err := c.normalize(); err == nil {
			t.Fatalf("mux_version %d must be rejected", v)
		}
	}
}
