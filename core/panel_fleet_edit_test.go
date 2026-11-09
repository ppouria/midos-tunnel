package main

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// lastConfig is the tunnel config most recently deployed on a fake server.
func lastConfig(t *testing.T, h *fakeHost) *Config {
	t.Helper()
	h.mu.Lock()
	defer h.mu.Unlock()
	for i := len(h.cmds) - 1; i >= 0; i-- {
		if strings.Contains(h.cmds[i], "midos-tunnel@$NAME") {
			var c Config
			if err := json.Unmarshal([]byte(h.stdin[i]), &c); err != nil {
				t.Fatalf("%s: config: %v", h.name, err)
			}
			return &c
		}
	}
	t.Fatalf("%s: no deploy", h.name)
	return nil
}

func TestFleetEditRebuildsCable(t *testing.T) {
	f := useFakeFleet(t)
	p := newTestPanel(t)
	p.login(testPassword)
	ir := addServer(t, p, "iran", "198.51.100.10")
	kh := addServer(t, p, "kharej", "198.51.100.20")
	_ = p.s.updateTopology(func(t *topology) error {
		t.Links = append(t.Links, topoLink{ID: "led", A: ir, B: kh, Kind: "direct", Transport: "tls", Port: 8443, Ports: "443", Carriers: 2})
		return nil
	})
	if w := p.do("POST", "/api/fleet/apply", map[string]any{"id": "led"}, true); w.Code != http.StatusOK {
		t.Fatalf("apply: %d %s", w.Code, w.Body)
	}
	name := func() string { l, _ := linkByID(p.s.loadTopology(), "led"); return l.Tunnel }
	first := name()

	// same kind: new transport, port and ports replace the old ones under the same name
	w := p.do("POST", "/api/fleet/edit", map[string]any{"id": "led", "kind": "direct", "transport": "stealth", "port": 2096,
		"ports": "443, 8443:2053", "udp_ports": "51820", "preset": "turbo", "carriers": 3}, true)
	if w.Code != http.StatusOK {
		t.Fatalf("edit: %d %s", w.Code, w.Body)
	}
	if name() != first {
		t.Fatalf("tunnel renamed: %q -> %q", first, name())
	}
	kc, ic := lastConfig(t, f.host("198.51.100.20")), lastConfig(t, f.host("198.51.100.10"))
	if kc.StealthListen != "0.0.0.0:2096" || kc.Listen != "" || kc.Name != first {
		t.Fatalf("kharej end: %+v", kc)
	}
	if len(ic.Endpoints) != 1 || ic.Endpoints[0] != (Endpoint{"stealth", "198.51.100.20:2096"}) ||
		strings.Join(ic.Ports, ",") != "443,8443:2053" || strings.Join(ic.UDPPorts, ",") != "51820" || ic.Preset != "turbo" || ic.Carriers != 3 {
		t.Fatalf("iran end: %+v", ic)
	}

	// direct -> kernel: the relay goes, the kernel tunnel comes
	if w := p.do("POST", "/api/fleet/edit", map[string]any{"id": "led", "kind": "kernel", "mode": "espudp", "ports": "443, 2000-3000"}, true); w.Code != http.StatusOK {
		t.Fatalf("to kernel: %d %s", w.Code, w.Body)
	}
	for _, ip := range []string{"198.51.100.10", "198.51.100.20"} {
		if cmd, _, ok := f.host(ip).ran("rm -f"); !ok || !strings.Contains(cmd, first) {
			t.Errorf("%s: relay tunnel not removed", ip)
		}
	}
	if hub, _, ok := f.host("198.51.100.10").ran(" provision"); !ok || !strings.Contains(hub, "MIDOS_MODE='espudp'") || !strings.Contains(hub, "2000-3000") {
		t.Fatalf("hub provision: %s", hub)
	}
	l, _ := linkByID(p.s.loadTopology(), "led")
	if l.Kind != "kernel" || l.Tunnel != "kernel" || l.Transport != "" || l.Slot == 0 {
		t.Fatalf("kernel cable: %+v", l)
	}

	// kernel -> reverse relay: the Kharej end is torn down, a relay is built
	f.host("198.51.100.20").cmds = nil
	if w := p.do("POST", "/api/fleet/edit", map[string]any{"id": "led", "kind": "direct", "reverse": true, "transport": "kcp", "ports": "443"}, true); w.Code != http.StatusOK {
		t.Fatalf("to relay: %d %s", w.Code, w.Body)
	}
	if _, _, ok := f.host("198.51.100.20").ran(" teardown"); !ok {
		t.Error("kernel tunnel not torn down on the kharej end")
	}
	l, _ = linkByID(p.s.loadTopology(), "led")
	if l.Kind != "direct" || !l.Reverse || l.Mode != "" || l.Slot != 0 || !tunnelNameRE.MatchString(l.Tunnel) || l.Tunnel == "kernel" {
		t.Fatalf("relay cable: %+v", l)
	}
	if ic := lastConfig(t, f.host("198.51.100.10")); ic.KCPListen != "0.0.0.0:4000" {
		t.Fatalf("reverse iran end listens: %+v", ic)
	}
}

func TestFleetEditRejectsBadInputFirst(t *testing.T) {
	f := useFakeFleet(t)
	p := newTestPanel(t)
	p.login(testPassword)
	ir := addServer(t, p, "iran", "198.51.100.10")
	kh := addServer(t, p, "kharej", "198.51.100.20")
	_ = p.s.updateTopology(func(t *topology) error {
		t.Links = append(t.Links, topoLink{ID: "lnew", A: ir, B: kh, Kind: "direct", Transport: "tls", Ports: "443"})
		return nil
	})
	if w := p.do("POST", "/api/fleet/edit", map[string]any{"id": "lnew", "kind": "direct", "ports": "443"}, true); w.Code != http.StatusBadRequest {
		t.Fatalf("an unapplied cable was edited: %d", w.Code)
	}
	if w := p.do("POST", "/api/fleet/apply", map[string]any{"id": "lnew"}, true); w.Code != http.StatusOK {
		t.Fatalf("apply: %d %s", w.Code, w.Body)
	}
	before := len(f.host("198.51.100.10").cmds)
	for _, bad := range []map[string]any{
		{"id": "lnew", "kind": "direct", "ports": ""},
		{"id": "lnew", "kind": "direct", "transport": "ssh", "ports": "443"},
		{"id": "lnew", "kind": "direct", "transport": "tls", "ports": "443", "preset": "warp"},
		{"id": "lnew", "kind": "kernel", "mode": "esp", "ports": "443=10.0.0.5:2096"},
		{"id": "lnew", "kind": "bridge", "ports": "443"},
	} {
		if w := p.do("POST", "/api/fleet/edit", bad, true); w.Code != http.StatusBadRequest {
			t.Errorf("%v: %d %s", bad, w.Code, w.Body)
		}
	}
	if len(f.host("198.51.100.10").cmds) != before {
		t.Fatal("a rejected edit touched the server")
	}
}

func TestFleetLinksOneIranManyKharej(t *testing.T) {
	f := useFakeFleet(t)
	p := newTestPanel(t)
	p.login(testPassword)
	k1 := addServer(t, p, "kharej", "198.51.100.21")
	k2 := addServer(t, p, "kharej", "198.51.100.22")
	w := p.do("POST", "/api/fleet/links", map[string]any{"self_role": "iran", "links": []map[string]any{
		{"a": "self", "b": k1, "kind": "direct", "reverse": true, "transport": "tls", "port": 443, "ports": "2083"},
		{"a": "self", "b": k2, "kind": "direct", "reverse": true, "transport": "tls", "port": 443, "ports": "2087"},
	}}, true)
	if w.Code != http.StatusOK {
		t.Fatalf("links: %d %s", w.Code, w.Body)
	}
	var r struct{ IDs []string }
	_ = json.NewDecoder(w.Body).Decode(&r)
	topo := p.s.loadTopology()
	if topo.SelfRole != "iran" || len(r.IDs) != 2 || len(topo.Links) != 2 {
		t.Fatalf("topology: %+v ids %v", topo, r.IDs)
	}
	for _, id := range r.IDs {
		if w := p.do("POST", "/api/fleet/apply", map[string]any{"id": id}, true); w.Code != http.StatusOK {
			t.Fatalf("apply %s: %d %s", id, w.Code, w.Body)
		}
	}
	// reverse: each Kharej server dials the mother's carrier port
	if c := lastConfig(t, f.host("198.51.100.22")); len(c.Endpoints) != 1 || !strings.HasSuffix(c.Endpoints[0].Server, ":443") {
		t.Fatalf("kharej 2 endpoints: %+v", c.Endpoints)
	}

	// a second cable between the same two servers is refused, as is an empty list
	if w := p.do("POST", "/api/fleet/links", map[string]any{"links": []map[string]any{{"a": "self", "b": k1, "kind": "direct", "ports": "9000"}}}, true); w.Code != http.StatusBadRequest {
		t.Fatalf("duplicate cable: %d", w.Code)
	}
	if w := p.do("POST", "/api/fleet/links", map[string]any{"links": []any{}}, true); w.Code != http.StatusBadRequest {
		t.Fatalf("empty list: %d", w.Code)
	}
	if n := len(p.s.loadTopology().Links); n != 2 {
		t.Fatalf("a refused request still added cables: %d", n)
	}
}

// Each kernel cable of one Iran server forwards its own ports, so several
// locations running the same service each answer on a port of their own.
func TestFleetKernelPortsPerLocation(t *testing.T) {
	f := useFakeFleet(t)
	p := newTestPanel(t)
	p.login(testPassword)
	ir := addServer(t, p, "iran", "198.51.100.10")
	k1 := addServer(t, p, "kharej", "198.51.100.21")
	k2 := addServer(t, p, "kharej", "198.51.100.22")
	k3 := addServer(t, p, "kharej", "198.51.100.23")
	w := p.do("POST", "/api/fleet/links", map[string]any{"links": []map[string]any{
		{"a": ir, "b": k1, "kind": "kernel", "mode": "esp", "ports": "37825, 62623"},
		{"a": ir, "b": k2, "kind": "kernel", "mode": "esp", "ports": "37826:37825, 62624:62623", "udp_ports": "37826:37825"},
	}}, true)
	if w.Code != http.StatusOK {
		t.Fatalf("links: %d %s", w.Code, w.Body)
	}
	var r struct{ IDs []string }
	_ = json.NewDecoder(w.Body).Decode(&r)
	if w := p.do("POST", "/api/fleet/apply", map[string]any{"id": r.IDs[0]}, true); w.Code != http.StatusOK {
		t.Fatalf("apply: %d %s", w.Code, w.Body)
	}
	hub, _, _ := f.host("198.51.100.10").ran(" provision")
	topo := p.s.loadTopology()
	l1, _ := linkByID(topo, r.IDs[0])
	l2, _ := linkByID(topo, r.IDs[1])
	for _, want := range []string{
		"MIDOS_PEER_" + itoa(l1.Slot) + "_TCP='37825,62623'",
		"MIDOS_PEER_" + itoa(l2.Slot) + "_TCP='37826:37825,62624:62623'",
		"MIDOS_PEER_" + itoa(l2.Slot) + "_UDP='37826:37825'",
	} {
		if !strings.Contains(hub, want) {
			t.Errorf("hub provision lacks %s:\n%s", want, hub)
		}
	}
	if strings.Contains(hub, "MIDOS_TCP_PORTS") || l1.Ports != "37825, 62623" || l2.Ports != "37826:37825, 62624:62623" {
		t.Fatalf("ports were shared: %s\n%+v %+v", hub, l1, l2)
	}

	// a third location on a port the first already forwards is refused, both
	// as a new cable and as an edit, before any server is touched
	before := len(f.host("198.51.100.10").cmds)
	w = p.do("POST", "/api/fleet/links", map[string]any{"links": []map[string]any{{"a": ir, "b": k3, "kind": "kernel", "mode": "esp", "ports": "62623"}}}, true)
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "62623") {
		t.Fatalf("clashing location: %d %s", w.Code, w.Body)
	}
	w = p.do("POST", "/api/fleet/edit", map[string]any{"id": r.IDs[1], "kind": "kernel", "mode": "esp", "ports": "37825"}, true)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("clashing edit: %d %s", w.Code, w.Body)
	}
	if len(f.host("198.51.100.10").cmds) != before || len(p.s.loadTopology().Links) != 2 {
		t.Fatal("a refused request changed something")
	}
	// moving a location to free ports is fine
	if w := p.do("POST", "/api/fleet/edit", map[string]any{"id": r.IDs[1], "kind": "kernel", "mode": "esp", "ports": "37827:37825"}, true); w.Code != http.StatusOK {
		t.Fatalf("edit to free ports: %d %s", w.Code, w.Body)
	}
}

// Ports typed with a Persian comma or Persian digits are read as the user
// meant them; a port the installer would refuse stops the build before any
// server is touched.
func TestFleetKernelPortsAsTyped(t *testing.T) {
	if got := strings.Join(splitList("37825،62623; ۱۸۴۴۳ ٥٤٢٩٤,  2000-3000"), "|"); got != "37825|62623|18443|54294|2000-3000" {
		t.Fatalf("splitList: %s", got)
	}
	f := useFakeFleet(t)
	p := newTestPanel(t)
	p.login(testPassword)
	ir := addServer(t, p, "iran", "198.51.100.10")
	k1 := addServer(t, p, "kharej", "198.51.100.21")
	_ = p.s.updateTopology(func(t *topology) error {
		t.Links = append(t.Links, topoLink{ID: "lfa", A: ir, B: k1, Kind: "kernel", Mode: "esp", Ports: "37825،62623", UDPPorts: "۳۷۸۲۵"})
		return nil
	})
	if w := p.do("POST", "/api/fleet/apply", map[string]any{"id": "lfa"}, true); w.Code != http.StatusOK {
		t.Fatalf("apply: %d %s", w.Code, w.Body)
	}
	hub, _, _ := f.host("198.51.100.10").ran(" provision")
	if !strings.Contains(hub, "_TCP='37825,62623'") || !strings.Contains(hub, "_UDP='37825'") {
		t.Fatalf("hub provision: %s", hub)
	}

	_ = p.s.updateTopology(func(t *topology) error {
		t.Links[0].Ports = "37825=10.0.0.5:2096"
		return nil
	})
	f.host("198.51.100.10").cmds = nil
	w := p.do("POST", "/api/fleet/apply", map[string]any{"id": "lfa"}, true)
	if w.Code != http.StatusBadGateway || !strings.Contains(w.Body.String(), "37825=10.0.0.5:2096") {
		t.Fatalf("bad kernel port: %d %s", w.Code, w.Body)
	}
	if len(f.host("198.51.100.10").cmds) != 0 {
		t.Fatal("a refused build touched the hub")
	}
}

// mux v2 only works when both ends use it, so the panel sets it on both: the
// other end's config of a tunnel, and both ends of a designer cable, also
// after an edit.
func TestMuxVersionReachesBothEnds(t *testing.T) {
	c := &Config{Mode: "client", PSK: testPSK, Name: "x", Endpoints: []Endpoint{{"tls", "198.51.100.20:443"}}, Ports: []string{"443"}, MuxVersion: 2, StallSec: 60}
	p, _, err := peerConfig(c, "198.51.100.10")
	if err != nil || p.MuxVersion != 2 || p.StallSec != 60 {
		t.Fatalf("peer config: %+v %v", p, err)
	}

	f := useFakeFleet(t)
	pn := newTestPanel(t)
	pn.login(testPassword)
	ir := addServer(t, pn, "iran", "198.51.100.10")
	kh := addServer(t, pn, "kharej", "198.51.100.20")
	_ = pn.s.updateTopology(func(t *topology) error {
		t.Links = append(t.Links, topoLink{ID: "lmux", A: ir, B: kh, Kind: "direct", Transport: "tls", Port: 8443, Ports: "443", MuxVersion: 2})
		return nil
	})
	if w := pn.do("POST", "/api/fleet/apply", map[string]any{"id": "lmux"}, true); w.Code != http.StatusOK {
		t.Fatalf("apply: %d %s", w.Code, w.Body)
	}
	if ic, kc := lastConfig(t, f.host("198.51.100.10")), lastConfig(t, f.host("198.51.100.20")); ic.MuxVersion != 2 || kc.MuxVersion != 2 {
		t.Fatalf("mux version per end: iran %d kharej %d", ic.MuxVersion, kc.MuxVersion)
	}
	if w := pn.do("POST", "/api/fleet/edit", map[string]any{"id": "lmux", "kind": "direct", "transport": "tls", "port": 8443, "ports": "443", "mux_version": 1}, true); w.Code != http.StatusOK {
		t.Fatalf("edit: %d %s", w.Code, w.Body)
	}
	if ic, kc := lastConfig(t, f.host("198.51.100.10")), lastConfig(t, f.host("198.51.100.20")); ic.MuxVersion != 1 || kc.MuxVersion != 1 {
		t.Fatalf("after edit: iran %d kharej %d", ic.MuxVersion, kc.MuxVersion)
	}
	if w := pn.do("POST", "/api/fleet/edit", map[string]any{"id": "lmux", "kind": "direct", "transport": "tls", "ports": "443", "mux_version": 3}, true); w.Code != http.StatusBadRequest {
		t.Fatalf("mux_version 3 accepted: %d", w.Code)
	}
}

func TestIranPorts(t *testing.T) {
	got := iranPorts("443, 2000-2002, 37826:37825, 9000-9001:9000, junk")
	for _, p := range []int{443, 2000, 2001, 2002, 37826, 9000, 9001} {
		if !got[p] {
			t.Errorf("missing %d in %v", p, got)
		}
	}
	if got[37825] || len(got) != 7 {
		t.Fatalf("iran ports: %v", got)
	}
}

// A page with an older copy of the map can neither undo an edit of an applied
// cable nor drop a cable built while it was open.
func TestTopologyPutKeepsAppliedCables(t *testing.T) {
	useFakeFleet(t)
	p := newTestPanel(t)
	p.login(testPassword)
	ir := addServer(t, p, "iran", "198.51.100.10")
	kh := addServer(t, p, "kharej", "198.51.100.20")
	_ = p.s.updateTopology(func(t *topology) error {
		t.Links = append(t.Links, topoLink{ID: "lap", A: ir, B: kh, Kind: "direct", Transport: "tls", Port: 443, Ports: "443"})
		return nil
	})
	if w := p.do("POST", "/api/fleet/apply", map[string]any{"id": "lap"}, true); w.Code != http.StatusOK {
		t.Fatalf("apply: %d %s", w.Code, w.Body)
	}
	applied, _ := linkByID(p.s.loadTopology(), "lap")
	nodes := []map[string]any{{"id": "self", "kind": "self"}, {"id": ir, "kind": "iran"}, {"id": kh, "kind": "kharej"}}

	stale := map[string]any{"self_role": "none", "nodes": nodes, "links": []map[string]any{{"id": "lap", "a": ir, "b": kh, "kind": "direct", "transport": "icmp", "ports": "80"}}}
	if w := p.do("PUT", "/api/topology", stale, true); w.Code != http.StatusOK {
		t.Fatalf("put: %d %s", w.Code, w.Body)
	}
	if got, _ := linkByID(p.s.loadTopology(), "lap"); got != applied {
		t.Fatalf("stale page changed an applied cable: %+v", got)
	}
	empty := map[string]any{"self_role": "none", "nodes": nodes, "links": []any{}}
	if w := p.do("PUT", "/api/topology", empty, true); w.Code != http.StatusOK {
		t.Fatalf("put: %d %s", w.Code, w.Body)
	}
	if _, ok := linkByID(p.s.loadTopology(), "lap"); !ok {
		t.Fatal("a page that never saw the cable dropped it")
	}
}
