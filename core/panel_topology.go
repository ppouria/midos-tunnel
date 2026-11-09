package main

// Network designer: the panel's drag-and-drop view of the fleet - the mother
// server, the Iran and Kharej servers it manages and the cables (tunnels)
// between them. The layout is stored next to the panel config so it survives a
// reload. SSH passwords and keys are never stored; adding a server installs the
// mother's own key instead (panel_fleet.go), which also builds the cables.

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"time"
)

type topoNode struct {
	ID      string  `json:"id"`
	Kind    string  `json:"kind"` // self, iran, kharej
	Label   string  `json:"label"`
	IP      string  `json:"ip"`
	X       float64 `json:"x"`
	Y       float64 `json:"y"`
	SSHUser string  `json:"ssh_user,omitempty"`
	SSHPort int     `json:"ssh_port,omitempty"`

	// set by the panel only (see mergeServerOwned)
	State   string     `json:"state,omitempty"` // installing, ready, error
	Error   string     `json:"error,omitempty"`
	Facts   *nodeFacts `json:"facts,omitempty"`
	HostKey string     `json:"host_key,omitempty"` // SHA256 fingerprint pinned on the first login
	// the pinned key's type (ssh-rsa, ssh-ed25519, ...), found when the server
	// starts offering another of its host keys first; "" until then
	HostKeyType string `json:"host_key_type,omitempty"`
	AddedAt     int64  `json:"added_at,omitempty"`
}

type topoLink struct {
	ID        string `json:"id"`
	A         string `json:"a"`
	B         string `json:"b"`
	Kind      string `json:"kind"` // direct, kernel
	Transport string `json:"transport,omitempty"`
	Port      int    `json:"port,omitempty"`
	Ports     string `json:"ports,omitempty"`
	UDPPorts  string `json:"udp_ports,omitempty"`
	Reverse   bool   `json:"reverse,omitempty"`
	Preset    string `json:"preset,omitempty"`
	Carriers  int    `json:"carriers,omitempty"`
	// direct cables: the multiplexer version both ends use (0 or 1 = v1, 2)
	MuxVersion int    `json:"mux_version,omitempty"`
	Mode       string `json:"mode,omitempty"`   // kernel: esp, espudp, gre, fou, ipsec
	Tunnel     string `json:"tunnel,omitempty"` // the tunnel this cable created

	// kernel cables of one Iran hub share a passphrase; each Kharej end keeps
	// its slot so rebuilding the hub never renumbers the others
	PSK  string `json:"psk,omitempty"`
	Slot int    `json:"slot,omitempty"`
}

type topology struct {
	SelfRole string     `json:"self_role"` // the mother's own role: iran, kharej or none (manager only)
	Nodes    []topoNode `json:"nodes"`
	Links    []topoLink `json:"links"`
}

const maxTopoItems = 200

func (s *panelServer) topologyPath() string {
	return filepath.Join(filepath.Dir(s.cfgPath), "topology.json")
}

func (s *panelServer) handleTopologyGet(w http.ResponseWriter, r *http.Request) {
	t := s.loadTopology()
	for i := range t.Links {
		t.Links[i].PSK = ""
	}
	if t.Nodes == nil {
		t.Nodes = []topoNode{}
	}
	if t.Links == nil {
		t.Links = []topoLink{}
	}
	cfg := s.config()
	jsonOK(w, map[string]any{"topology": t, "self_ip": publicHost(&cfg)})
}

func (s *panelServer) handleTopologyPut(w http.ResponseWriter, r *http.Request) {
	var t topology
	if err := readJSON(r, &t); err != nil {
		jsonError(w, http.StatusBadRequest, "bad request: "+err.Error())
		return
	}
	s.topoMu.Lock()
	defer s.topoMu.Unlock()
	mergeServerOwned(&t, s.loadTopology())
	if err := checkTopology(&t); err != nil {
		jsonError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.saveTopology(&t); err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	jsonOK(w, map[string]bool{"ok": true})
}

// checkTopology keeps a stored layout small and well-formed: known kinds, ids
// that exist, and every cable joining an Iran server to a Kharej server.
func checkTopology(t *topology) error {
	if t.SelfRole != "iran" && t.SelfRole != "kharej" && t.SelfRole != "none" {
		return errors.New("self_role must be iran, kharej or none")
	}
	if len(t.Nodes) > maxTopoItems || len(t.Links) > maxTopoItems {
		return errors.New("too many devices or cables")
	}
	ids := map[string]string{}
	for _, n := range t.Nodes {
		if n.ID == "" || len(n.ID) > 40 {
			return errors.New("every device needs a short id")
		}
		switch n.Kind {
		case "self", "iran", "kharej":
		default:
			return fmt.Errorf("device %s: unknown kind %q", n.ID, n.Kind)
		}
		if len(n.Label) > 40 || len(n.IP) > 64 || len(n.SSHUser) > 32 {
			return fmt.Errorf("device %s: a field is too long", n.ID)
		}
		ids[n.ID] = n.Kind
	}
	role := func(kind string) string {
		if kind == "self" {
			return t.SelfRole
		}
		return kind
	}
	pairs := map[string]bool{}
	kernelOf := map[string]int{}
	for _, l := range t.Links {
		ka, okA := ids[l.A]
		kb, okB := ids[l.B]
		if !okA || !okB {
			return fmt.Errorf("cable %s joins a device that does not exist", l.ID)
		}
		if l.Kind != "direct" && l.Kind != "kernel" {
			return fmt.Errorf("cable %s: unknown kind %q", l.ID, l.Kind)
		}
		ra, rb := role(ka), role(kb)
		if !(ra == "iran" && rb == "kharej" || ra == "kharej" && rb == "iran") {
			return fmt.Errorf("cable %s must join an Iran server to a Kharej server", l.ID)
		}
		key := l.A + "|" + l.B
		if l.B < l.A {
			key = l.B + "|" + l.A
		}
		if pairs[key] {
			return fmt.Errorf("cable %s: these two servers already have a cable", l.ID)
		}
		pairs[key] = true
		if l.Kind == "kernel" {
			k := l.A
			if ra == "iran" {
				k = l.B
			}
			if kernelOf[k]++; kernelOf[k] > 1 {
				return errors.New("a Kharej server can take only one kernel cable")
			}
		}
	}
	return nil
}

// linkRemote runs the peer command on the far server; tests replace it.
var linkRemote = func(ctx context.Context, tgt sshTarget, cmd string) ([]byte, error) {
	return sshRun(ctx, tgt, cmd)
}

// handleTopologyLink installs the far end of an existing direct tunnel on a
// remote server over SSH.
func (s *panelServer) handleTopologyLink(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID  string    `json:"id"`
		SSH sshTarget `json:"ssh"`
	}
	if err := readJSON(r, &req); err != nil {
		jsonError(w, http.StatusBadRequest, "bad request")
		return
	}
	t, ok := s.tunnelByID(req.ID)
	if !ok {
		jsonError(w, http.StatusNotFound, "no such tunnel")
		return
	}
	if err := validateTarget(req.SSH); err != nil {
		jsonError(w, http.StatusBadRequest, err.Error())
		return
	}
	cmd, _, _, err := s.mintPeerCommand(t)
	if err != nil {
		jsonError(w, http.StatusBadRequest, err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	out, err := linkRemote(ctx, req.SSH, cmd)
	res := stepResult{Step: "سرور مقابل (" + req.SSH.Host + ")", OK: err == nil, Output: tailLines(string(out), 40)}
	if err != nil {
		res.Error = err.Error()
		s.audit(r, "topology-link", req.ID+" -> "+req.SSH.Host+" failed")
		jsonError2(w, http.StatusBadGateway, "remote install failed", []stepResult{res})
		return
	}
	s.audit(r, "topology-link", req.ID+" -> "+req.SSH.Host)
	s.notify("🔌 تانل «" + req.ID + "» از طراحی شبکه به " + req.SSH.Host + " وصل شد")
	jsonOK(w, map[string]any{"ok": true, "results": []stepResult{res}})
}

func tailLines(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}
