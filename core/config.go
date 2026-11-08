package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
)

// Config is read from a small JSON file so the tunnel manager can generate it.
//
// Two roles are fixed by where the services live: the "client" sits in Iran and
// listens on the user-facing ports, the "server" sits abroad and reaches the
// real services ("targets"). Which of the two opens the carrier connections is
// a separate choice: normally the client dials the server, and with "reverse"
// the server dials the client instead, so the server abroad needs no open
// inbound port at all.
type Config struct {
	Mode string `json:"mode"` // "server" (abroad) or "client" (Iran side)
	PSK  string `json:"psk"`  // shared secret, identical on both ends
	Name string `json:"name"` // label for logs and the stats endpoint

	// reverse: the server opens the carriers towards the client
	Reverse bool `json:"reverse"`

	// speed preset: balance (default), turbo, aggressive or throughput. It sets
	// the multiplexer's buffer sizes, a default carrier count and how hard KCP
	// pushes - larger presets trade memory and bandwidth for throughput.
	Preset string `json:"preset"`

	// The dialing side. "endpoints" lists every way to reach the peer, tried in
	// order and kept on the last one that worked; "server" + "transport" is the
	// single-endpoint shorthand older configurations use.
	Endpoints []Endpoint `json:"endpoints"`
	Server    string     `json:"server"`
	Transport string     `json:"transport"`

	ServerName  string `json:"server_name"`  // SNI presented in the handshake
	Fingerprint string `json:"fingerprint"`  // browser to imitate: chrome, firefox, safari, edge, random
	Insecure    bool   `json:"insecure_tls"` // accept any certificate (self-signed origin)
	Carriers    int    `json:"carriers"`     // parallel carriers to spread streams over
	Host        string `json:"host"`         // ws: HTTP Host header, default server_name

	// both sides with transport ws: the secret path of the WebSocket. On the
	// listening side it enables WebSocket sessions next to plain ones on "listen".
	WSPath string `json:"ws_path"`

	// both sides with transport grpc: the gRPC service name; the call is made
	// to /<service>/Tun, the shape a CDN with gRPC support expects
	GRPCService string `json:"grpc_service"`

	// transport ssh: how to log in to the listening side's sshd, and where the
	// relay listens there (ssh_listen on that side). The dialing side uses
	// these; a listening side keeps them only to hand to a peer that dials it.
	SSHUser     string `json:"ssh_user,omitempty"`
	SSHPassword string `json:"ssh_password,omitempty"`
	SSHKey      string `json:"ssh_key,omitempty"`
	SSHPort     int    `json:"ssh_port,omitempty"`
	SSHRemote   string `json:"ssh_remote,omitempty"`

	// The listening side: one address per transport, any combination.
	Listen        string `json:"listen"`         // tls (and ws on the same port)
	TCPListen     string `json:"tcp_listen"`     // plain tcp
	StealthListen string `json:"stealth_listen"` // encrypted tcp, no TLS fingerprint
	KCPListen     string `json:"kcp_listen"`     // kcp over udp
	QUICListen    string `json:"quic_listen"`    // quic over udp
	ICMPListen    string `json:"icmp_listen"`    // kcp over icmp echo (set to a bind IP, e.g. 0.0.0.0, to enable)
	DNSListen     string `json:"dns_listen"`     // kcp over udp shaped as DNS (e.g. 0.0.0.0:53)
	GRPCListen    string `json:"grpc_listen"`    // a gRPC stream over HTTP/2 + TLS
	// ssh: carriers that arrive through this server's own sshd, as forwarded
	// connections to this loopback address; nothing new faces the network
	SSHListen string `json:"ssh_listen"`
	CertPEM   string `json:"cert_pem"` // certificate; generated self-signed when empty
	KeyPEM    string `json:"key_pem"`
	// optional plain-HTTP listener for WebSocket sessions, for a CDN that
	// reaches the origin over HTTP (Cloudflare "Flexible"); needs ws_path
	HTTPListen string `json:"http_listen"`

	// kcp forward error correction: parity shards rebuild lost packets without
	// waiting a round trip for a retransmission
	KCPDataShards   int `json:"kcp_data_shards"`
	KCPParityShards int `json:"kcp_parity_shards"`

	// shared: which ports are relayed, "localPort:remotePort" or just "port"
	Ports []string `json:"ports"`
	// shared: UDP ports carried as framed datagrams inside the same session
	// (same "localPort:remotePort" form); for networks that let only TCP out
	UDPPorts []string `json:"udp_ports"`

	Targets string `json:"targets"` // server side: where to send a stream, default 127.0.0.1

	// the listening side: where to send a connection that is not a tunnel client.
	// With a "host:port" here, a scanner or active probe is proxied to that
	// address and sees exactly what it serves - an ordinary website - instead
	// of a dead port. Empty keeps the old behaviour (a slow, silent drop).
	Decoy string `json:"decoy"`

	// both sides: pass the user's real address to the service as a PROXY v2
	// header, for services that limit or log per client
	ProxyProtocol bool `json:"proxy_protocol"`

	// anti-DPI (dialing side): split the TLS ClientHello across TCP segments so
	// an SNI-matching censor cannot read the server name
	TLSFragment bool `json:"tls_fragment"`
	// obfuscation: pad stealth frames to random sizes (shaping), and recycle
	// each carrier after about this many seconds so no flow is long-lived
	Shape     bool `json:"shape"`
	RotateSec int  `json:"rotate_sec"`

	StallSec int `json:"stall_sec"`

	MuxVersion int `json:"mux_version"`

	// loopback address of a small JSON status endpoint, read by the web panel
	StatsListen string `json:"stats_listen"`
}

// Endpoint is one way to reach the peer.
type Endpoint struct {
	Transport string `json:"transport"` // tls, ws, tcp, stealth, kcp, quic, icmp, grpc, ssh
	Server    string `json:"server"`    // host:port
}

var transports = map[string]bool{"tls": true, "ws": true, "tcp": true, "stealth": true, "kcp": true, "quic": true, "icmp": true, "grpc": true, "ssh": true, "dns": true}

// defaultSSHRemote is where a relay reached through sshd listens by default.
const defaultSSHRemote = "127.0.0.1:8710"

// dials reports whether this side opens the carrier connections.
func (c *Config) dials() bool { return (c.Mode == "client") != c.Reverse }

// tuning is the set of knobs a preset chooses.
type tuning struct {
	recvBuf, streamBuf int // smux buffers, bytes
	carriers           int // default carrier count when none is set
	kcpWindow          int // kcp send/recv window, packets
	kcpResend          int // kcp fast-resend threshold (lower = more eager)
}

var presets = map[string]tuning{
	"balance":    {8 << 20, 1 << 20, 2, 1024, 2},
	"turbo":      {16 << 20, 2 << 20, 4, 2048, 2},
	"aggressive": {32 << 20, 4 << 20, 6, 4096, 1},
	"throughput": {64 << 20, 8 << 20, 8, 8192, 1},
}

func (c *Config) tune() tuning {
	t, ok := presets[c.Preset]
	if !ok {
		t = presets["balance"]
	}
	return t
}

// portMap is one forwarded port. On the client only local and remote matter
// (listen on local, name remote in the stream header). On the server, remote is
// the port a stream may ask for, and host:hostPort is where it is then sent -
// defaulting to the configured targets and the same port when unset. The wire
// protocol never carries the host, so the two ends can map a port differently.
type portMap struct {
	local    uint16
	remote   uint16
	host     string // server side: destination host, "" = cfg.Targets
	hostPort uint16 // server side: destination port, 0 = remote
}

func (m portMap) dest(targets string) string {
	host := m.host
	if host == "" {
		host = targets
	}
	port := m.hostPort
	if port == 0 {
		port = m.remote
	}
	return net.JoinHostPort(host, strconv.Itoa(int(port)))
}

// parsePorts returns the TCP port mappings. A configuration may relay only UDP,
// so an empty TCP list is an error only when there is no UDP list either.
func (c *Config) parsePorts() ([]portMap, error) {
	out, err := parsePortList(c.Ports)
	if err != nil {
		return nil, err
	}
	if len(out) == 0 && len(c.UDPPorts) == 0 {
		return nil, errors.New("no ports configured")
	}
	return out, nil
}

// parseUDPPorts returns the UDP port mappings (possibly none).
func (c *Config) parseUDPPorts() ([]portMap, error) {
	return parsePortList(c.UDPPorts)
}

// parsePortList accepts, per entry:
//
//	443                listen 443, forward to the target on 443
//	443:2096           listen 443, forward to the target on 2096
//	443=10.0.0.5:2096  listen 443, forward to 10.0.0.5:2096
//	8000-8010          a range, each port forwarded to itself
func parsePortList(list []string) ([]portMap, error) {
	var out []portMap
	seen := map[uint16]bool{}
	add := func(m portMap, raw string) error {
		if seen[m.local] {
			return fmt.Errorf("local port %d is listed twice", m.local)
		}
		seen[m.local] = true
		out = append(out, m)
		return nil
	}
	for _, raw := range list {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		// optional "=host[:port]" destination
		spec, dest, hasDest := strings.Cut(raw, "=")
		var host string
		var hostPort uint16
		if hasDest {
			h, p, err := splitHostPortOpt(dest)
			if err != nil {
				return nil, fmt.Errorf("port %q: %w", raw, err)
			}
			host, hostPort = h, p
		}
		// a range expands to one mapping per port, forwarded to itself
		if lo, hi, ok := strings.Cut(spec, "-"); ok {
			a, err := parsePort(lo)
			b, err2 := parsePort(hi)
			if err != nil || err2 != nil || a > b {
				return nil, fmt.Errorf("port range %q is not valid", raw)
			}
			if hasDest {
				return nil, fmt.Errorf("port range %q cannot have a fixed destination port", raw)
			}
			for p := a; p <= b; p++ {
				if err := add(portMap{local: p, remote: p, host: host}, raw); err != nil {
					return nil, err
				}
				if p == 65535 {
					break
				}
			}
			continue
		}
		l, r, _ := strings.Cut(spec, ":")
		if r == "" {
			r = l
		}
		lp, err := parsePort(l)
		if err != nil {
			return nil, fmt.Errorf("port %q: %w", raw, err)
		}
		rp, err := parsePort(r)
		if err != nil {
			return nil, fmt.Errorf("port %q: %w", raw, err)
		}
		if err := add(portMap{local: lp, remote: rp, host: host, hostPort: hostPort}, raw); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// splitHostPortOpt parses "host", "host:port" or ":port" for a destination.
func splitHostPortOpt(s string) (string, uint16, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", 0, errors.New("empty destination")
	}
	host, portStr, ok := strings.Cut(s, ":")
	if !ok {
		return host, 0, nil // host only, port defaults to the remote port
	}
	p, err := parsePort(portStr)
	if err != nil {
		return "", 0, err
	}
	return host, p, nil
}

func parsePort(s string) (uint16, error) {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil {
		return 0, err
	}
	if n < 1 || n > 65535 {
		return 0, fmt.Errorf("%d out of range", n)
	}
	return uint16(n), nil
}

func loadConfig(path string) (*Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var c Config
	if err := json.Unmarshal(b, &c); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if err := c.normalize(); err != nil {
		return nil, err
	}
	return &c, nil
}

// normalize validates the configuration and fills in defaults.
func (c *Config) normalize() error {
	if len(c.PSK) < 16 {
		return errors.New("psk must be at least 16 characters")
	}
	if c.WSPath != "" && !strings.HasPrefix(c.WSPath, "/") {
		return errors.New("ws_path must start with \"/\"")
	}
	if c.Mode != "server" && c.Mode != "client" {
		return fmt.Errorf("mode must be \"server\" or \"client\", got %q", c.Mode)
	}
	if c.Mode == "server" && c.Targets == "" {
		c.Targets = "127.0.0.1"
	}
	if c.Preset == "" {
		c.Preset = "balance"
	}
	if _, ok := presets[c.Preset]; !ok {
		return fmt.Errorf("preset must be balance, turbo, aggressive or throughput, got %q", c.Preset)
	}
	if c.Carriers < 1 {
		c.Carriers = c.tune().carriers
	}
	if c.KCPDataShards == 0 && c.KCPParityShards == 0 {
		c.KCPDataShards, c.KCPParityShards = 10, 3
	}
	if c.KCPDataShards < 0 || c.KCPParityShards < 0 || c.KCPDataShards > 128 || c.KCPParityShards > 128 {
		return errors.New("kcp shards must be between 0 and 128")
	}
	if c.StatsListen != "" {
		host, _, err := net.SplitHostPort(c.StatsListen)
		if err != nil {
			return fmt.Errorf("stats_listen: %w", err)
		}
		// the endpoint has no authentication, so it must never face the network
		if ip := net.ParseIP(host); ip == nil || !ip.IsLoopback() {
			return errors.New("stats_listen must be a loopback address")
		}
	}

	if c.GRPCService == "" {
		c.GRPCService = "GunService"
	}
	if strings.ContainsAny(c.GRPCService, "/ ?#") {
		return errors.New("grpc_service must be a plain service name, like GunService")
	}
	if c.SSHListen != "" {
		host, _, err := net.SplitHostPort(c.SSHListen)
		if err != nil {
			return fmt.Errorf("ssh_listen: %w", err)
		}
		// only sshd should ever reach it
		if ip := net.ParseIP(host); ip == nil || !ip.IsLoopback() {
			return errors.New("ssh_listen must be a loopback address, e.g. 127.0.0.1:8710")
		}
	}
	if c.SSHRemote == "" {
		c.SSHRemote = defaultSSHRemote
	}
	if c.SSHPort == 0 {
		c.SSHPort = 22
	}
	if c.MuxVersion < 0 || c.MuxVersion > 2 {
		return errors.New("mux_version must be 1 or 2")
	}
	if c.RotateSec != 0 && c.RotateSec < 30 {
		return errors.New("rotate_sec must be 0 (off) or at least 30 seconds")
	}

	if c.Decoy != "" {
		if _, _, err := net.SplitHostPort(c.Decoy); err != nil {
			return fmt.Errorf("decoy must be host:port: %w", err)
		}
	}
	if !c.dials() {
		if c.Listen == "" && c.TCPListen == "" && c.StealthListen == "" && c.KCPListen == "" && c.QUICListen == "" && c.ICMPListen == "" && c.GRPCListen == "" && c.SSHListen == "" && c.DNSListen == "" {
			return errors.New("the listening side needs a listener: listen, tcp_listen, stealth_listen, kcp_listen, quic_listen, icmp_listen, dns_listen, grpc_listen or ssh_listen")
		}
		if c.HTTPListen != "" && c.WSPath == "" {
			return errors.New("http_listen needs \"ws_path\"")
		}
		return nil
	}

	if len(c.Endpoints) == 0 {
		if c.Server == "" {
			return errors.New("the dialing side needs \"server\" or \"endpoints\"")
		}
		c.Endpoints = []Endpoint{{Transport: c.Transport, Server: c.Server}}
	}
	for i := range c.Endpoints {
		ep := &c.Endpoints[i]
		if ep.Transport == "" {
			ep.Transport = "tls"
		}
		if !transports[ep.Transport] {
			return fmt.Errorf("endpoint %d: unknown transport %q (tls, ws, tcp, stealth, kcp, quic, icmp, dns, grpc, ssh)", i+1, ep.Transport)
		}
		if ep.Transport == "icmp" {
			// ICMP has no ports; the server is a bare host or IP.
			if strings.TrimSpace(ep.Server) == "" {
				return fmt.Errorf("endpoint %d: icmp server must be a host or IP", i+1)
			}
		} else if _, _, err := net.SplitHostPort(ep.Server); err != nil {
			return fmt.Errorf("endpoint %d: server must be host:port: %w", i+1, err)
		}
		if ep.Transport == "ws" && c.WSPath == "" {
			return fmt.Errorf("endpoint %d: transport ws needs \"ws_path\"", i+1)
		}
		if ep.Transport == "ssh" {
			if c.SSHUser == "" || (c.SSHPassword == "" && strings.TrimSpace(c.SSHKey) == "") {
				return fmt.Errorf("endpoint %d: transport ssh needs ssh_user and ssh_password or ssh_key", i+1)
			}
			if _, _, err := net.SplitHostPort(c.SSHRemote); err != nil {
				return fmt.Errorf("ssh_remote must be host:port: %w", err)
			}
		}
	}
	// ICMP has no port, so every carrier from one client shares a source
	// address the server's KCP demux cannot tell apart. Ride a single carrier;
	// smux still multiplexes every stream over it.
	for _, ep := range c.Endpoints {
		if ep.Transport == "icmp" && c.Carriers > 1 {
			c.Carriers = 1
			break
		}
	}
	if c.ServerName == "" {
		// An empty SNI is itself unusual. Fall back to the dial host.
		c.ServerName, _, _ = net.SplitHostPort(c.Endpoints[0].Server)
	}
	if c.Host == "" {
		c.Host = c.ServerName
	}
	return nil
}
