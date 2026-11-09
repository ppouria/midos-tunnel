package main

// Tunnel management for the panel.
//
// A tunnel the panel created is one JSON file in the tunnels directory, run by
// the systemd template midos-tunnel@<name>. Tunnels set up before the panel
// existed (units named Midos-tunnel-veil*) are found too and can be watched,
// edited, started and stopped - but not deleted, since the panel did not
// create them.

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

var tunnelNameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,31}$`)

type tunnelRec struct {
	ID         string `json:"id"`
	Unit       string `json:"unit"`
	ConfigPath string `json:"config_path"`
	Managed    bool   `json:"managed"`
}

// findTunnels lists every tunnel on this server.
func (s *panelServer) findTunnels() []tunnelRec {
	cfg := s.config()
	var out []tunnelRec
	files, _ := filepath.Glob(filepath.Join(cfg.TunnelsDir, "*.json"))
	for _, f := range files {
		id := strings.TrimSuffix(filepath.Base(f), ".json")
		if tunnelNameRE.MatchString(id) {
			out = append(out, tunnelRec{ID: id, Unit: "midos-tunnel@" + id + ".service", ConfigPath: f, Managed: true})
		}
	}
	units, _ := filepath.Glob(filepath.Join(s.unitDir, "Midos-tunnel-veil*.service"))
	for _, u := range units {
		b, err := os.ReadFile(u)
		if err != nil {
			continue
		}
		path := unitConfigPath(string(b))
		id := strings.TrimSuffix(filepath.Base(u), ".service")
		if path == "" || !tunnelNameRE.MatchString(id) {
			continue
		}
		out = append(out, tunnelRec{ID: id, Unit: filepath.Base(u), ConfigPath: path, Managed: false})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// unitConfigPath pulls the -config argument out of a unit's ExecStart line.
func unitConfigPath(unit string) string {
	for _, line := range strings.Split(unit, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "ExecStart=") {
			continue
		}
		f := strings.Fields(strings.TrimPrefix(line, "ExecStart="))
		for i := 0; i+1 < len(f); i++ {
			if f[i] == "-config" {
				return f[i+1]
			}
		}
	}
	return ""
}

func (s *panelServer) tunnelByID(id string) (tunnelRec, bool) {
	for _, t := range s.findTunnels() {
		if t.ID == id {
			return t, true
		}
	}
	return tunnelRec{}, false
}

func readTunnelConfig(path string) (*Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var c Config
	if err := json.Unmarshal(b, &c); err != nil {
		return nil, err
	}
	return &c, nil
}

func writeTunnelConfig(path string, c *Config) error {
	// the file holds the pre-shared key, so it is never world-readable
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(path, append(b, '\n'), 0o600)
}

// ---------------------------------------------------------------------------
// systemd
// ---------------------------------------------------------------------------

func (s *panelServer) systemctl(args ...string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	out, err := s.run(ctx, "systemctl", args...)
	if err != nil {
		return fmt.Errorf("systemctl %s: %v %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}

type unitState struct {
	Active   string `json:"active"`
	Sub      string `json:"sub"`
	Enabled  string `json:"enabled"`
	Since    string `json:"since"`
	Restarts int    `json:"restarts"`
}

func (s *panelServer) unitStatus(unit string) unitState {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, _ := s.run(ctx, "systemctl", "show", unit, "--no-pager",
		"-p", "ActiveState,SubState,UnitFileState,ActiveEnterTimestamp,NRestarts")
	var st unitState
	for _, line := range strings.Split(string(out), "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok {
			continue
		}
		switch k {
		case "ActiveState":
			st.Active = v
		case "SubState":
			st.Sub = v
		case "UnitFileState":
			st.Enabled = v
		case "ActiveEnterTimestamp":
			st.Since = v
		case "NRestarts":
			st.Restarts, _ = strconv.Atoi(v)
		}
	}
	if st.Active == "" {
		st.Active = "unknown"
	}
	return st
}

func (s *panelServer) ensureTemplateUnit() error {
	path := filepath.Join(s.unitDir, "midos-tunnel@.service")
	if _, err := os.Stat(path); err == nil {
		return nil
	}
	if err := writeFileAtomic(path, []byte(tunnelUnitTemplate), 0o644); err != nil {
		return err
	}
	return s.systemctl("daemon-reload")
}

// ---------------------------------------------------------------------------
// views
// ---------------------------------------------------------------------------

type tunnelView struct {
	tunnelRec
	Config      *Config   `json:"config,omitempty"`
	ConfigError string    `json:"config_error,omitempty"`
	State       unitState `json:"state"`
	Stats       *statsDoc `json:"stats,omitempty"`
	StatsError  string    `json:"stats_error,omitempty"`
}

var statsClient = &http.Client{Timeout: 1500 * time.Millisecond}

func fetchStats(addr string) (*statsDoc, error) {
	resp, err := statsClient.Get("http://" + addr + "/stats")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var d statsDoc
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&d); err != nil {
		return nil, err
	}
	return &d, nil
}

func (s *panelServer) view(t tunnelRec) tunnelView {
	v := tunnelView{tunnelRec: t, State: s.unitStatus(t.Unit)}
	c, err := readTunnelConfig(t.ConfigPath)
	if err != nil {
		v.ConfigError = err.Error()
		return v
	}
	v.Config = c
	if c.StatsListen == "" {
		v.StatsError = "disabled"
	} else if v.State.Active == "active" {
		if d, err := fetchStats(c.StatsListen); err != nil {
			v.StatsError = err.Error()
		} else {
			v.Stats = d
		}
	}
	return v
}

func (s *panelServer) handleListTunnels(w http.ResponseWriter, r *http.Request) {
	ts := s.findTunnels()
	views := make([]tunnelView, len(ts))
	var wg sync.WaitGroup
	for i, t := range ts {
		wg.Add(1)
		go func(i int, t tunnelRec) {
			defer wg.Done()
			views[i] = s.view(t)
		}(i, t)
	}
	wg.Wait()
	jsonOK(w, views)
}

func (s *panelServer) handleGetTunnel(w http.ResponseWriter, r *http.Request) {
	t, ok := s.tunnelByID(r.PathValue("id"))
	if !ok {
		jsonError(w, http.StatusNotFound, "no such tunnel")
		return
	}
	jsonOK(w, s.view(t))
}

// ---------------------------------------------------------------------------
// create / update / delete
// ---------------------------------------------------------------------------

// validated returns a normalised deep copy of c, leaving c itself untouched.
func validated(c *Config) (*Config, error) {
	b, err := json.Marshal(c)
	if err != nil {
		return nil, err
	}
	var cp Config
	if err := json.Unmarshal(b, &cp); err != nil {
		return nil, err
	}
	if err := cp.normalize(); err != nil {
		return nil, err
	}
	if _, err := cp.parsePorts(); err != nil {
		return nil, err
	}
	if _, err := cp.parseUDPPorts(); err != nil {
		return nil, fmt.Errorf("udp: %w", err)
	}
	return &cp, nil
}

// boundAddrs lists every address the tunnel will listen on, as "tcp :443".
func boundAddrs(c *Config) []string {
	var out []string
	add := func(proto, addr string) {
		if addr != "" {
			out = append(out, proto+" "+addr)
		}
	}
	if c.Mode == "client" {
		maps, _ := c.parsePorts()
		for _, m := range maps {
			add("tcp", fmt.Sprintf(":%d", m.local))
		}
		udp, _ := c.parseUDPPorts()
		for _, m := range udp {
			add("udp", fmt.Sprintf(":%d", m.local))
		}
	}
	if !c.dials() {
		add("tcp", c.Listen)
		add("tcp", c.HTTPListen)
		add("tcp", c.TCPListen)
		add("tcp", c.StealthListen)
		add("udp", c.KCPListen)
		add("udp", c.QUICListen)
		add("tcp", c.GRPCListen)
		add("tcp", c.SSHListen)
		add("udp", c.DNSListen)
	}
	return out
}

// portsFree refuses a configuration whose ports something else already holds,
// before systemd starts it into a crash loop.
func portsFree(c *Config) error {
	for _, a := range boundAddrs(c) {
		proto, addr, _ := strings.Cut(a, " ")
		if proto == "tcp" {
			ln, err := net.Listen("tcp", addr)
			if err != nil {
				return fmt.Errorf("tcp %s is already in use", addr)
			}
			_ = ln.Close()
		} else {
			pc, err := net.ListenPacket("udp", addr)
			if err != nil {
				return fmt.Errorf("udp %s is already in use", addr)
			}
			_ = pc.Close()
		}
	}
	return nil
}

// freeStatsAddr picks a loopback port for a new tunnel's stats endpoint that no
// other tunnel uses.
func (s *panelServer) freeStatsAddr() string {
	used := map[string]bool{}
	for _, t := range s.findTunnels() {
		if c, err := readTunnelConfig(t.ConfigPath); err == nil {
			used[c.StatsListen] = true
		}
	}
	for p := 39100; p < 40000; p++ {
		a := fmt.Sprintf("127.0.0.1:%d", p)
		if used[a] {
			continue
		}
		if ln, err := net.Listen("tcp", a); err == nil {
			_ = ln.Close()
			return a
		}
	}
	return ""
}

func (s *panelServer) handleCreateTunnel(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name   string `json:"name"`
		Config Config `json:"config"`
	}
	if err := readJSON(r, &req); err != nil {
		jsonError(w, http.StatusBadRequest, "bad request: "+err.Error())
		return
	}
	if !tunnelNameRE.MatchString(req.Name) {
		jsonError(w, http.StatusBadRequest, "name: letters, digits, - and _ only (up to 32)")
		return
	}
	if _, exists := s.tunnelByID(req.Name); exists {
		jsonError(w, http.StatusConflict, "a tunnel with this name exists")
		return
	}
	req.Config.Name = req.Name
	if req.Config.StatsListen == "" {
		req.Config.StatsListen = s.freeStatsAddr()
	}
	c, err := validated(&req.Config)
	if err != nil {
		jsonError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := portsFree(c); err != nil {
		jsonError(w, http.StatusConflict, err.Error())
		return
	}
	path := filepath.Join(s.config().TunnelsDir, req.Name+".json")
	if err := writeTunnelConfig(path, c); err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := s.ensureTemplateUnit(); err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	unit := "midos-tunnel@" + req.Name + ".service"
	if err := s.systemctl("enable", "--now", unit); err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.audit(r, "tunnel-create", req.Name)
	s.notify("➕ تانل «" + req.Name + "» ساخته شد")
	log.Printf("tunnel %s created", req.Name)
	jsonOK(w, s.view(tunnelRec{ID: req.Name, Unit: unit, ConfigPath: path, Managed: true}))
}

func (s *panelServer) handleUpdateTunnel(w http.ResponseWriter, r *http.Request) {
	t, ok := s.tunnelByID(r.PathValue("id"))
	if !ok {
		jsonError(w, http.StatusNotFound, "no such tunnel")
		return
	}
	if !strings.HasPrefix(filepath.Clean(t.ConfigPath), "/etc/Midos-tunnel/") && !t.Managed {
		jsonError(w, http.StatusForbidden, "this tunnel's configuration lives outside /etc/Midos-tunnel")
		return
	}
	var req struct {
		Config Config `json:"config"`
	}
	if err := readJSON(r, &req); err != nil {
		jsonError(w, http.StatusBadRequest, "bad request: "+err.Error())
		return
	}
	if old, err := readTunnelConfig(t.ConfigPath); err == nil && req.Config.StatsListen == "" {
		req.Config.StatsListen = old.StatsListen
	}
	if req.Config.Name == "" {
		req.Config.Name = t.ID
	}
	c, err := validated(&req.Config)
	if err != nil {
		jsonError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := writeTunnelConfig(t.ConfigPath, c); err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := s.systemctl("restart", t.Unit); err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.audit(r, "tunnel-update", t.ID)
	log.Printf("tunnel %s updated", t.ID)
	jsonOK(w, s.view(t))
}

func (s *panelServer) handleDeleteTunnel(w http.ResponseWriter, r *http.Request) {
	t, ok := s.tunnelByID(r.PathValue("id"))
	if !ok {
		jsonError(w, http.StatusNotFound, "no such tunnel")
		return
	}
	if !t.Managed {
		jsonError(w, http.StatusForbidden, "this tunnel was not created by the panel; it can only be stopped or disabled here")
		return
	}
	_ = s.systemctl("disable", "--now", t.Unit)
	if err := os.Remove(t.ConfigPath); err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.audit(r, "tunnel-delete", t.ID)
	log.Printf("tunnel %s deleted", t.ID)
	jsonOK(w, map[string]bool{"ok": true})
}

func (s *panelServer) handleTunnelAction(w http.ResponseWriter, r *http.Request) {
	t, ok := s.tunnelByID(r.PathValue("id"))
	if !ok {
		jsonError(w, http.StatusNotFound, "no such tunnel")
		return
	}
	var req struct {
		Action string `json:"action"`
	}
	if err := readJSON(r, &req); err != nil {
		jsonError(w, http.StatusBadRequest, "bad request")
		return
	}
	switch req.Action {
	case "start", "stop", "restart":
		if err := s.systemctl(req.Action, t.Unit); err != nil {
			jsonError(w, http.StatusInternalServerError, err.Error())
			return
		}
	case "enable", "disable":
		if err := s.systemctl(req.Action, "--now", t.Unit); err != nil {
			jsonError(w, http.StatusInternalServerError, err.Error())
			return
		}
	default:
		jsonError(w, http.StatusBadRequest, "unknown action")
		return
	}
	s.audit(r, "tunnel-"+req.Action, t.ID)
	log.Printf("tunnel %s: %s", t.ID, req.Action)
	jsonOK(w, s.view(t))
}

func (s *panelServer) handleTunnelLogs(w http.ResponseWriter, r *http.Request) {
	t, ok := s.tunnelByID(r.PathValue("id"))
	if !ok {
		jsonError(w, http.StatusNotFound, "no such tunnel")
		return
	}
	n, _ := strconv.Atoi(r.URL.Query().Get("lines"))
	if n <= 0 || n > 1000 {
		n = 200
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := s.run(ctx, "journalctl", "-u", t.Unit, "-n", strconv.Itoa(n), "--no-pager", "-o", "short-iso")
	if err != nil && len(out) == 0 {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	jsonOK(w, map[string]string{"logs": string(out)})
}

// handleEnableStats turns on the stats endpoint of a tunnel that predates it.
func (s *panelServer) handleEnableStats(w http.ResponseWriter, r *http.Request) {
	t, ok := s.tunnelByID(r.PathValue("id"))
	if !ok {
		jsonError(w, http.StatusNotFound, "no such tunnel")
		return
	}
	c, err := readTunnelConfig(t.ConfigPath)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if c.StatsListen == "" {
		c.StatsListen = s.freeStatsAddr()
		if c.Name == "" {
			c.Name = t.ID
		}
		if err := writeTunnelConfig(t.ConfigPath, c); err != nil {
			jsonError(w, http.StatusInternalServerError, err.Error())
			return
		}
		if err := s.systemctl("restart", t.Unit); err != nil {
			jsonError(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	jsonOK(w, s.view(t))
}

// ---------------------------------------------------------------------------
// connection test
// ---------------------------------------------------------------------------

type pingResult struct {
	Target  string  `json:"target"`
	Kind    string  `json:"kind"` // icmp, tcp
	Sent    int     `json:"sent"`
	Lost    float64 `json:"loss_pct"`
	Min     float64 `json:"min_ms"`
	Avg     float64 `json:"avg_ms"`
	Max     float64 `json:"max_ms"`
	Error   string  `json:"error,omitempty"`
	Comment string  `json:"comment,omitempty"`
}

var pingSummaryRE = regexp.MustCompile(`([\d.]+)% packet loss`)
var pingRTTRE = regexp.MustCompile(`= ([\d.]+)/([\d.]+)/([\d.]+)/`)

func (s *panelServer) icmpPing(host string) pingResult {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	out, _ := s.run(ctx, "ping", "-n", "-q", "-c", "10", "-i", "0.3", "-W", "2", host)
	return parsePing(host, out)
}

// parsePing reads the summary of "ping -q -c 10"; the route test also runs it
// on fleet servers.
func parsePing(host string, out []byte) pingResult {
	res := pingResult{Target: host, Kind: "icmp", Sent: 10}
	if m := pingSummaryRE.FindSubmatch(out); m != nil {
		res.Lost, _ = strconv.ParseFloat(string(m[1]), 64)
	} else {
		res.Error = "no answer from ping"
		return res
	}
	if m := pingRTTRE.FindSubmatch(out); m != nil {
		res.Min, _ = strconv.ParseFloat(string(m[1]), 64)
		res.Avg, _ = strconv.ParseFloat(string(m[2]), 64)
		res.Max, _ = strconv.ParseFloat(string(m[3]), 64)
	}
	res.Comment = "ICMP may be rate-limited by routers on the way; the carrier figures are the real ones"
	return res
}

func tcpConnectTest(addr string) pingResult {
	res := pingResult{Target: addr, Kind: "tcp", Sent: 5}
	var times []float64
	for i := 0; i < res.Sent; i++ {
		t0 := time.Now()
		c, err := net.DialTimeout("tcp", addr, 5*time.Second)
		if err != nil {
			res.Error = err.Error()
			continue
		}
		times = append(times, float64(time.Since(t0).Microseconds())/1000)
		_ = c.Close()
	}
	return connectStats(res, times)
}

// connectStats fills in loss and timings from the handshakes that succeeded.
func connectStats(res pingResult, times []float64) pingResult {
	res.Lost = 100 * float64(res.Sent-len(times)) / float64(res.Sent)
	if len(times) > 0 {
		sort.Float64s(times)
		res.Min, res.Max = times[0], times[len(times)-1]
		var sum float64
		for _, t := range times {
			sum += t
		}
		res.Avg = sum / float64(len(times))
		res.Error = ""
	}
	return res
}

func (s *panelServer) handleTunnelTest(w http.ResponseWriter, r *http.Request) {
	t, ok := s.tunnelByID(r.PathValue("id"))
	if !ok {
		jsonError(w, http.StatusNotFound, "no such tunnel")
		return
	}
	c, err := validated(mustRead(t.ConfigPath))
	if err != nil {
		jsonError(w, http.StatusBadRequest, err.Error())
		return
	}
	var results []pingResult
	if c.dials() {
		seen := map[string]bool{}
		for _, ep := range c.Endpoints {
			host, _, _ := net.SplitHostPort(ep.Server)
			if !seen[host] {
				seen[host] = true
				results = append(results, s.icmpPing(host))
			}
			switch ep.Transport {
			case "tls", "ws", "tcp":
				results = append(results, tcpConnectTest(ep.Server))
			default:
				results = append(results, pingResult{Target: ep.Server, Kind: ep.Transport,
					Comment: "a UDP transport has no connection to time; see the carrier RTT"})
			}
		}
	}
	resp := map[string]any{"results": results, "listening": boundAddrs(c)}
	if c.StatsListen != "" {
		if d, err := fetchStats(c.StatsListen); err == nil {
			resp["carriers"] = d.Carriers
		}
	}
	jsonOK(w, resp)
}

func mustRead(path string) *Config {
	c, err := readTunnelConfig(path)
	if err != nil {
		return &Config{}
	}
	return c
}

// ---------------------------------------------------------------------------
// the other end
// ---------------------------------------------------------------------------

type peerToken struct {
	name    string
	config  []byte
	open    []string // "443/tcp" to open in the peer's firewall
	expires time.Time
}

// peerConfig builds the configuration for the other end of a tunnel: same key,
// same transports and ports, opposite role. host is how the peer reaches this
// server when the peer is the one that dials.
func peerConfig(c *Config, host string) (*Config, []string, error) {
	p := &Config{
		PSK: c.PSK, Name: c.Name, Reverse: c.Reverse, Preset: c.Preset,
		ServerName: c.ServerName, Fingerprint: c.Fingerprint, Insecure: true,
		Carriers: c.Carriers, Host: c.Host, WSPath: c.WSPath, GRPCService: c.GRPCService,
		TLSFragment: c.TLSFragment, Shape: c.Shape, RotateSec: c.RotateSec,
		KCPDataShards: c.KCPDataShards, KCPParityShards: c.KCPParityShards,
		ProxyProtocol: c.ProxyProtocol, StallSec: c.StallSec,
		// both ends must speak the same multiplexer version
		MuxVersion:  c.MuxVersion,
		StatsListen: fmt.Sprintf("127.0.0.1:%d", 39000+int(fnvHash(c.Name)%900)),
	}
	maps, err := c.parsePorts()
	if err != nil {
		return nil, nil, err
	}
	udp, err := c.parseUDPPorts()
	if err != nil {
		return nil, nil, err
	}
	// The service side names its own ports; the user side may remap them. A
	// per-port destination (443=10.0.0.5:2096) belongs on the side that dials
	// the service, so it is carried onto the server peer.
	for _, m := range maps {
		p.Ports = append(p.Ports, serverPortSpec(m))
	}
	for _, m := range udp {
		p.UDPPorts = append(p.UDPPorts, serverPortSpec(m))
	}
	if c.Mode == "client" {
		p.Mode, p.Targets = "server", "127.0.0.1"
	} else {
		p.Mode = "client"
	}

	var open []string
	if c.dials() {
		// the peer listens where this side dials
		for _, ep := range c.Endpoints {
			_, port, _ := net.SplitHostPort(ep.Server)
			addr := "0.0.0.0:" + port
			switch ep.Transport {
			case "tls", "ws":
				p.Listen = addr
				open = append(open, port+"/tcp")
			case "tcp":
				p.TCPListen = addr
				open = append(open, port+"/tcp")
			case "stealth":
				p.StealthListen = addr
				open = append(open, port+"/tcp")
			case "kcp":
				p.KCPListen = addr
				open = append(open, port+"/udp")
			case "quic":
				p.QUICListen = addr
				open = append(open, port+"/udp")
			case "icmp":
				// ICMP has no port: the peer answers on every address
				p.ICMPListen = "0.0.0.0"
			case "dns":
				p.DNSListen = addr
				open = append(open, port+"/udp")
			case "grpc":
				p.GRPCListen = addr
				open = append(open, port+"/tcp")
			case "ssh":
				// reached through the peer's own sshd: only loopback, no new port
				p.SSHListen = c.SSHRemote
				if p.SSHListen == "" {
					p.SSHListen = defaultSSHRemote
				}
			}
		}
	} else {
		// the peer dials the listeners on this side
		add := func(tr, listen string) {
			if listen == "" {
				return
			}
			_, port, _ := net.SplitHostPort(listen)
			p.Endpoints = append(p.Endpoints, Endpoint{Transport: tr, Server: net.JoinHostPort(host, port)})
		}
		add("tls", c.Listen)
		add("tcp", c.TCPListen)
		add("stealth", c.StealthListen)
		add("kcp", c.KCPListen)
		add("quic", c.QUICListen)
		if c.ICMPListen != "" {
			p.Endpoints = append(p.Endpoints, Endpoint{Transport: "icmp", Server: host})
		}
		add("grpc", c.GRPCListen)
		if c.DNSListen != "" {
			_, port, _ := net.SplitHostPort(c.DNSListen)
			p.Endpoints = append(p.Endpoints, Endpoint{Transport: "dns", Server: net.JoinHostPort(host, port)})
		}
		if c.SSHListen != "" {
			// the peer logs in to this server's sshd with the login kept here
			p.Endpoints = append(p.Endpoints, Endpoint{Transport: "ssh", Server: sshEndpoint(host, c.SSHPort)})
			p.SSHUser, p.SSHPassword, p.SSHKey, p.SSHPort = c.SSHUser, c.SSHPassword, c.SSHKey, c.SSHPort
			p.SSHRemote = c.SSHListen
		}
		if p.Fingerprint == "" {
			p.Fingerprint = "chrome"
		}
	}
	if c.Mode != "client" {
		// the peer is the Iran side: its users come in on these ports
		for _, m := range maps {
			open = append(open, fmt.Sprintf("%d/tcp", m.remote))
		}
		for _, m := range udp {
			open = append(open, fmt.Sprintf("%d/udp", m.remote))
		}
	}
	if err := p.normalize(); err != nil {
		return nil, nil, err
	}
	return p, open, nil
}

// serverPortSpec writes the port the server peer should listen on in the stream
// header's terms (the remote port), carrying any per-port destination along.
func serverPortSpec(m portMap) string {
	s := strconv.Itoa(int(m.remote))
	if m.host != "" {
		s += "=" + m.host
		if m.hostPort != 0 {
			s += ":" + strconv.Itoa(int(m.hostPort))
		}
	}
	return s
}

func fnvHash(s string) uint32 {
	h := fnv.New32a()
	_, _ = h.Write([]byte(s))
	return h.Sum32()
}

func (s *panelServer) handlePeerCommand(w http.ResponseWriter, r *http.Request) {
	t, ok := s.tunnelByID(r.PathValue("id"))
	if !ok {
		jsonError(w, http.StatusNotFound, "no such tunnel")
		return
	}
	cmd, body, expires, err := s.mintPeerCommand(t)
	if err != nil {
		jsonError(w, http.StatusBadRequest, err.Error())
		return
	}
	jsonOK(w, map[string]any{"command": cmd, "config": body, "expires": expires.Unix()})
}

// mintPeerCommand prepares the other end of a tunnel and returns the one-line
// command that installs it there (valid for two hours).
func (s *panelServer) mintPeerCommand(t tunnelRec) (cmd, config string, expires time.Time, err error) {
	c, err := validated(mustRead(t.ConfigPath))
	if err != nil {
		return "", "", time.Time{}, err
	}
	if c.Name == "" {
		c.Name = t.ID
	}
	cfg := s.config()
	host := publicHost(&cfg)
	p, open, err := peerConfig(c, host)
	if err != nil {
		return "", "", time.Time{}, err
	}
	body, _ := json.MarshalIndent(p, "", "  ")
	token := randomToken(16)
	s.mu.Lock()
	now := time.Now()
	for k, v := range s.peers {
		if now.After(v.expires) {
			delete(s.peers, k)
		}
	}
	expires = now.Add(2 * time.Hour)
	s.peers[token] = peerToken{name: c.Name, config: body, open: open, expires: expires}
	s.mu.Unlock()

	_, port, _ := net.SplitHostPort(cfg.Listen)
	url := fmt.Sprintf("https://%s%s/peer/%s", net.JoinHostPort(host, port), cfg.BasePath, token)
	return fmt.Sprintf("curl -fsSLk '%s' | bash", url), string(body), expires, nil
}

func (s *panelServer) peerByToken(r *http.Request) (peerToken, bool) {
	tok := r.PathValue("token")
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.peers[tok]
	if !ok || time.Now().After(p.expires) {
		return peerToken{}, false
	}
	return p, true
}

func (s *panelServer) handlePeerConfig(w http.ResponseWriter, r *http.Request) {
	p, ok := s.peerByToken(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(p.config)
}

var (
	selfOnce sync.Once
	selfSum  string
	selfErr  error
)

func selfBinary() (string, string, error) {
	path, err := os.Executable()
	if err != nil {
		return "", "", err
	}
	selfOnce.Do(func() {
		f, err := os.Open(path)
		if err != nil {
			selfErr = err
			return
		}
		defer f.Close()
		h := sha256.New()
		if _, err := io.Copy(h, f); err != nil {
			selfErr = err
			return
		}
		selfSum = hex.EncodeToString(h.Sum(nil))
	})
	return path, selfSum, selfErr
}

func (s *panelServer) handlePeerBinary(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.peerByToken(r); !ok {
		http.NotFound(w, r)
		return
	}
	path, _, err := selfBinary()
	if err != nil {
		http.Error(w, "binary unavailable", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	http.ServeFile(w, r, path)
}

func (s *panelServer) handlePeerScript(w http.ResponseWriter, r *http.Request) {
	p, ok := s.peerByToken(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	_, sum, err := selfBinary()
	if err != nil {
		http.Error(w, "binary unavailable", http.StatusInternalServerError)
		return
	}
	cfg := s.config()
	_, port, _ := net.SplitHostPort(cfg.Listen)
	base := fmt.Sprintf("https://%s%s/peer/%s", net.JoinHostPort(publicHost(&cfg), port), cfg.BasePath, r.PathValue("token"))
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = io.WriteString(w, peerScript(base, p.name, sum, strings.Join(p.open, " ")))
}

func goArchToUname() string {
	switch runtime.GOARCH {
	case "arm64":
		return "aarch64|arm64"
	default:
		return "x86_64|amd64"
	}
}

func peerScript(base, name, sum, open string) string {
	return fmt.Sprintf(`#!/bin/bash
# Midos Tunnel - sets up the other end of tunnel %[2]q
set -euo pipefail
BASE='%[1]s'
NAME='%[2]s'
SUM='%[3]s'
OPEN='%[4]s'
VER='%[6]s'

[ "$(id -u)" = 0 ] || { echo "Run this as root."; exit 1; }
case "$(uname -m)" in %[5]s) ;; *) echo "This server is $(uname -m); the binary offered here is for %[5]s."; exit 1 ;; esac
command -v curl >/dev/null || { echo "curl is required."; exit 1; }

tmp=$(mktemp -d); trap 'rm -rf "$tmp"' EXIT
curl -fsSk "$BASE/config" -o "$tmp/$NAME.json"

BIN=/usr/local/sbin/midos-tunnel
cur=""
[ -x "$BIN" ] && cur=$("$BIN" -version 2>&1 | awk '{print $2}' || true)
# install when missing or older; never replace a newer binary with this one
if [ -z "$cur" ] || { [ "$cur" != "$VER" ] && [ "$(printf '%%s\n%%s\n' "$cur" "$VER" | sort -V | head -1)" = "$cur" ]; }; then
    echo "Installing midos-tunnel $VER..."
    curl -fsSk "$BASE/bin" -o "$tmp/midos-tunnel"
    echo "$SUM  $tmp/midos-tunnel" | sha256sum -c --quiet
    install -m 755 "$tmp/midos-tunnel" "$BIN.new" && mv -f "$BIN.new" "$BIN"
else
    echo "midos-tunnel $cur is already installed."
fi

"$BIN" -config "$tmp/$NAME.json" -check
install -d -m 700 /etc/Midos-tunnel/tunnels
install -m 600 "$tmp/$NAME.json" "/etc/Midos-tunnel/tunnels/$NAME.json"

if [ ! -f /etc/systemd/system/midos-tunnel@.service ]; then
cat > /etc/systemd/system/midos-tunnel@.service <<'UNIT'
%[7]sUNIT
fi
systemctl daemon-reload
# an older install moves its tunnels to the current unit names
"$BIN" migrate >/dev/null 2>&1 || true
systemctl disable --now "midos-veil@$NAME" >/dev/null 2>&1 || true

for p in $OPEN; do
    if command -v ufw >/dev/null && ufw status 2>/dev/null | grep -q "Status: active"; then
        ufw allow "$p" >/dev/null
    elif command -v firewall-cmd >/dev/null && firewall-cmd --state >/dev/null 2>&1; then
        firewall-cmd -q --permanent --add-port="$p" && firewall-cmd -q --reload
    fi
done

systemctl daemon-reload
systemctl enable "midos-tunnel@$NAME" >/dev/null 2>&1
systemctl restart "midos-tunnel@$NAME"
sleep 2
if systemctl is-active --quiet "midos-tunnel@$NAME"; then
    echo "Tunnel $NAME is running on this server."
else
    journalctl -u "midos-tunnel@$NAME" -n 20 --no-pager
    exit 1
fi
`, base, name, sum, open, goArchToUname(), version, tunnelUnitTemplate)
}

// ---------------------------------------------------------------------------
// backup / restore
// ---------------------------------------------------------------------------

func (s *panelServer) handleBackup(w http.ResponseWriter, r *http.Request) {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, t := range s.findTunnels() {
		b, err := os.ReadFile(t.ConfigPath)
		if err != nil {
			continue
		}
		dir := "tunnels"
		if !t.Managed {
			dir = "legacy"
		}
		hdr := &tar.Header{Name: dir + "/" + t.ID + ".json", Mode: 0o600, Size: int64(len(b)), ModTime: time.Now()}
		if tw.WriteHeader(hdr) != nil || func() error { _, err := tw.Write(b); return err }() != nil {
			jsonError(w, http.StatusInternalServerError, "backup failed")
			return
		}
	}
	_ = tw.Close()
	_ = gz.Close()
	host, _ := os.Hostname()
	w.Header().Set("Content-Type", "application/gzip")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="midos-tunnels-%s-%s.tar.gz"`, host, time.Now().Format("20060102-1504")))
	_, _ = w.Write(buf.Bytes())
}

// handleRestore brings tunnels back from a backup. A tunnel that already exists
// is left alone; everything restored becomes a panel-managed tunnel.
func (s *panelServer) handleRestore(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 10<<20)
	f, _, err := r.FormFile("file")
	if err != nil {
		jsonError(w, http.StatusBadRequest, "no file")
		return
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		jsonError(w, http.StatusBadRequest, "not a .tar.gz backup")
		return
	}
	tr := tar.NewReader(gz)
	type result struct {
		Name   string `json:"name"`
		Status string `json:"status"`
	}
	var results []result
	dir := s.config().TunnelsDir
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			jsonError(w, http.StatusBadRequest, "corrupt backup: "+err.Error())
			return
		}
		if hdr.Typeflag != tar.TypeReg || hdr.Size > 1<<20 {
			continue
		}
		folder, file := filepath.Split(filepath.ToSlash(hdr.Name))
		if folder != "tunnels/" && folder != "legacy/" {
			continue
		}
		name := strings.TrimSuffix(file, ".json")
		if !tunnelNameRE.MatchString(name) || !strings.HasSuffix(file, ".json") {
			results = append(results, result{file, "skipped: bad name"})
			continue
		}
		if _, exists := s.tunnelByID(name); exists {
			results = append(results, result{name, "skipped: already exists"})
			continue
		}
		var c Config
		if err := json.NewDecoder(io.LimitReader(tr, 1<<20)).Decode(&c); err != nil {
			results = append(results, result{name, "skipped: " + err.Error()})
			continue
		}
		c.Name = name
		if c.StatsListen == "" {
			c.StatsListen = s.freeStatsAddr()
		}
		vc, err := validated(&c)
		if err != nil {
			results = append(results, result{name, "skipped: " + err.Error()})
			continue
		}
		if err := writeTunnelConfig(filepath.Join(dir, name+".json"), vc); err != nil {
			results = append(results, result{name, "failed: " + err.Error()})
			continue
		}
		if err := s.ensureTemplateUnit(); err == nil {
			if err := s.systemctl("enable", "--now", "midos-tunnel@"+name+".service"); err != nil {
				results = append(results, result{name, "restored, but did not start: " + err.Error()})
				continue
			}
		}
		results = append(results, result{name, "restored"})
	}
	log.Printf("restore: %d entries", len(results))
	jsonOK(w, map[string]any{"results": results})
}
