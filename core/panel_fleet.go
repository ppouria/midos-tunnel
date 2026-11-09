package main

// The fleet. The server the panel runs on is the "mother" server: from it the
// operator adds Iran and Kharej servers by IP and SSH login, and the panel
// prepares each one by itself - system update, prerequisites, the midos-tunnel
// binary, the kernel-tunnel base and a benchmark. The SSH password is used once,
// to install the mother's own key; from then on the panel reaches the server
// with that key and the host key it pinned on the first login, and nothing
// secret about the server is stored.
//
// Tunnels are cables between any Iran and any Kharej server, one to one or one
// to many, built on both ends from here. Each server gets a card with its specs,
// live resources, benchmark and the servers it is tunnelled with.
//
// The fleet is the designer's topology.json: devices are servers, cables are
// tunnels. Fields only the panel may set (install state, facts, the pinned host
// key, what a cable built) survive the browser's saves - see mergeServerOwned.

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"

	"golang.org/x/crypto/ssh"
)

const (
	fleetInstallTimeout = 40 * time.Minute
	fleetSampleEvery    = 10 * time.Second
	fleetIdleAfter      = 90 * time.Second // stop sampling when no page has asked for this long
	fleetKeyComment     = "midos-mother"
	remoteBin           = "/usr/local/sbin/midos-tunnel"
	remoteInstaller     = "/usr/local/sbin/Midos-tunnel.sh"
)

type nodeFacts struct {
	Hostname  string `json:"hostname"`
	OS        string `json:"os"`
	Kernel    string `json:"kernel"`
	Arch      string `json:"arch"`
	Virt      string `json:"virt"`
	CPU       string `json:"cpu"`
	Cores     int    `json:"cores"`
	MemTotal  uint64 `json:"mem_total"`
	DiskTotal uint64 `json:"disk_total"`
	Agent     string `json:"veil,omitempty"` // midos-tunnel version on the server (json name kept for saved fleets)
}

// ---------------------------------------------------------------------------
// running commands on a server
// ---------------------------------------------------------------------------

// hostExec runs a shell command on one server of the fleet.
type hostExec interface {
	Run(ctx context.Context, cmd string, stdin io.Reader, out io.Writer) error
}

type localExec struct{}

func (localExec) Run(ctx context.Context, cmd string, stdin io.Reader, out io.Writer) error {
	c := exec.CommandContext(ctx, "bash", "-c", cmd)
	c.Stdin, c.Stdout, c.Stderr = stdin, out, out
	return c.Run()
}

// fleetExecFor picks how to run commands on a server; tests replace it. A
// managed server is reached with the mother's key, one short connection per
// step (see sshjob.go).
var fleetExecFor = func(s *panelServer, n topoNode) hostExec {
	if n.Kind == "self" {
		return localExec{}
	}
	var mu sync.Mutex
	return jobExec{dial: func(ctx context.Context, deadline time.Time) (*ssh.Client, error) {
		mu.Lock()
		cur := n
		mu.Unlock()
		c, kt, err := s.fleetDial(ctx, cur, deadline)
		if err == nil {
			mu.Lock()
			n.HostKeyType = kt
			mu.Unlock()
		}
		return c, err
	}}
}

// fleetBootstrap opens the first, password (or operator key) login to a server
// and reports its host key fingerprint; tests replace it. Every later
// connection of the set-up must show the same key.
var fleetBootstrap = func(tgt sshTarget) (hostExec, string, func(), error) {
	auths, err := sshAuth(tgt.Password, tgt.Key)
	if err != nil {
		return nil, "", nil, err
	}
	var mu sync.Mutex
	var fp string
	addr := net.JoinHostPort(tgt.Host, strconv.Itoa(tgt.Port))
	cfg := &ssh.ClientConfig{
		User: tgt.User, Auth: auths,
		HostKeyCallback: func(_ string, _ net.Addr, k ssh.PublicKey) error {
			mu.Lock()
			defer mu.Unlock()
			got := ssh.FingerprintSHA256(k)
			if fp == "" {
				fp = got
			} else if got != fp {
				return errHostKeyChanged
			}
			return nil
		},
	}
	ex := jobExec{dial: func(ctx context.Context, deadline time.Time) (*ssh.Client, error) {
		return dialSSHBy(ctx, addr, cfg, deadline)
	}}
	// the first login proves the address and the password, and pins the key
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if _, err := ex.op(ctx, "true", nil, 20*time.Second); err != nil {
		return nil, "", nil, fmt.Errorf("اتصال SSH به %s ناموفق بود: %v", addr, err)
	}
	return ex, fp, func() {}, nil
}

func runOut(ctx context.Context, ex hostExec, cmd string) (string, error) {
	var b bytes.Buffer
	err := ex.Run(ctx, cmd, nil, &b)
	return strings.TrimSpace(b.String()), err
}

// ---------------------------------------------------------------------------
// the mother's SSH key and connection pool
// ---------------------------------------------------------------------------

func (s *panelServer) fleetKeyPath() string {
	return filepath.Join(filepath.Dir(s.cfgPath), "fleet_ed25519")
}

// fleetSigner loads the mother's private key, creating it on first use. It is
// an ordinary OpenSSH key, so the operator can use it by hand as well.
func (s *panelServer) fleetSigner() (ssh.Signer, error) {
	s.fleetMu.Lock()
	defer s.fleetMu.Unlock()
	if s.fleetKey != nil {
		return s.fleetKey, nil
	}
	path := s.fleetKeyPath()
	if b, err := os.ReadFile(path); err == nil {
		k, err := ssh.ParsePrivateKey(b)
		if err != nil {
			return nil, fmt.Errorf("fleet key %s: %w", path, err)
		}
		s.fleetKey = k
		return k, nil
	}
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	blk, err := ssh.MarshalPrivateKey(priv, fleetKeyComment)
	if err != nil {
		return nil, err
	}
	if err := writeFileAtomic(path, pem.EncodeToMemory(blk), 0o600); err != nil {
		return nil, err
	}
	k, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		return nil, err
	}
	s.fleetKey = k
	return k, nil
}

func (s *panelServer) fleetAuthorizedLine() (string, error) {
	k, err := s.fleetSigner()
	if err != nil {
		return "", err
	}
	host, _ := os.Hostname()
	line := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(k.PublicKey())))
	return line + " " + fleetKeyComment + "@" + sanitizeLabel(host, "panel"), nil
}

var errHostKeyChanged = errors.New("کلید میزبان سرور عوض شده است (سرور دوباره نصب شده؟)؛ سرور را دوباره اضافه کنید")

// hostKeyAlgos maps a host key's type to the algorithms that ask a server for
// that key; "" leaves the choice to the library.
var hostKeyAlgos = map[string][]string{
	ssh.KeyAlgoED25519:  {ssh.KeyAlgoED25519},
	ssh.KeyAlgoECDSA256: {ssh.KeyAlgoECDSA256},
	ssh.KeyAlgoECDSA384: {ssh.KeyAlgoECDSA384},
	ssh.KeyAlgoECDSA521: {ssh.KeyAlgoECDSA521},
	ssh.KeyAlgoRSA:      {ssh.KeyAlgoRSASHA512, ssh.KeyAlgoRSASHA256, ssh.KeyAlgoRSA},
}

var hostKeyTypes = []string{ssh.KeyAlgoED25519, ssh.KeyAlgoECDSA256, ssh.KeyAlgoRSA, ssh.KeyAlgoECDSA384, ssh.KeyAlgoECDSA521}

// fleetDial opens a fresh connection to a managed server with the mother's
// key, accepted only with the host key pinned on the first login. It reports
// the type of that key, to be asked for first next time.
func (s *panelServer) fleetDial(ctx context.Context, n topoNode, deadline time.Time) (*ssh.Client, string, error) {
	signer, err := s.fleetSigner()
	if err != nil {
		return nil, "", err
	}
	want := n.HostKey
	port := n.SSHPort
	if port == 0 {
		port = 22
	}
	addr := net.JoinHostPort(n.IP, strconv.Itoa(port))
	dial := func(keyType string) (*ssh.Client, error) {
		return dialSSHBy(ctx, addr, &ssh.ClientConfig{
			User: firstNonEmpty(n.SSHUser, "root"), Auth: []ssh.AuthMethod{ssh.PublicKeys(signer)},
			HostKeyAlgorithms: hostKeyAlgos[keyType],
			HostKeyCallback: func(_ string, _ net.Addr, k ssh.PublicKey) error {
				if want != "" && ssh.FingerprintSHA256(k) != want {
					return errHostKeyChanged
				}
				return nil
			},
		}, deadline)
	}
	c, err := dial(n.HostKeyType)
	if !errors.Is(err, errHostKeyChanged) {
		return c, n.HostKeyType, err
	}
	// A server has several host keys, and which one it shows first can change
	// (an sshd upgrade, a key type added). Ask for each type in turn: the
	// connection still goes through only with the pinned key.
	for _, kt := range hostKeyTypes {
		if kt == n.HostKeyType {
			continue
		}
		c2, err2 := dial(kt)
		if errors.Is(err2, errHostKeyChanged) || (err2 != nil && strings.Contains(err2.Error(), "no common algorithm for host key")) {
			continue // another key, or no key of this type
		}
		if err2 == nil {
			s.setNode(n.ID, func(x *topoNode) { x.HostKeyType = kt })
		}
		return c2, kt, err2
	}
	return nil, "", err
}

// ---------------------------------------------------------------------------
// topology access shared with the designer
// ---------------------------------------------------------------------------

// updateTopology loads, changes and saves the fleet under one lock.
func (s *panelServer) updateTopology(fn func(t *topology) error) error {
	s.topoMu.Lock()
	defer s.topoMu.Unlock()
	t := s.loadTopology()
	if err := fn(&t); err != nil {
		return err
	}
	return s.saveTopology(&t)
}

func (s *panelServer) saveTopology(t *topology) error {
	if t.Nodes == nil {
		t.Nodes = []topoNode{}
	}
	if t.Links == nil {
		t.Links = []topoLink{}
	}
	b, _ := json.MarshalIndent(t, "", "  ")
	return writeFileAtomic(s.topologyPath(), append(b, '\n'), 0o600)
}

func (s *panelServer) setNode(id string, fn func(n *topoNode)) {
	_ = s.updateTopology(func(t *topology) error {
		for i := range t.Nodes {
			if t.Nodes[i].ID == id {
				fn(&t.Nodes[i])
			}
		}
		return nil
	})
}

// mergeServerOwned copies the fields only the panel sets from the stored fleet
// onto a layout the browser sent, so a save from the page never forges or
// loses them. A server that was added keeps its address and login.
func mergeServerOwned(in *topology, old topology) {
	on := map[string]topoNode{}
	for _, n := range old.Nodes {
		on[n.ID] = n
	}
	for i := range in.Nodes {
		n := &in.Nodes[i]
		o, ok := on[n.ID]
		n.State, n.Error, n.Facts, n.HostKey, n.HostKeyType, n.AddedAt = o.State, o.Error, o.Facts, o.HostKey, o.HostKeyType, o.AddedAt
		if ok && o.State != "" {
			n.IP, n.SSHUser, n.SSHPort = o.IP, o.SSHUser, o.SSHPort
		}
	}
	ol := map[string]topoLink{}
	for _, l := range old.Links {
		ol[l.ID] = l
	}
	seen := map[string]bool{}
	for i := range in.Links {
		l := &in.Links[i]
		seen[l.ID] = true
		o, ok := ol[l.ID]
		if ok && o.Tunnel != "" {
			// an applied cable changes only through the panel (edit, unlink),
			// so a page holding an older copy cannot undo an edit
			*l = o
			continue
		}
		l.Tunnel, l.PSK, l.Slot = o.Tunnel, o.PSK, o.Slot
	}
	// nor can it drop an applied cable it has not seen yet (one built from
	// the tunnel form while the designer was open), as long as both of its
	// servers are still on the map
	ids := map[string]bool{}
	for _, n := range in.Nodes {
		ids[n.ID] = true
	}
	for _, o := range old.Links {
		if o.Tunnel != "" && !seen[o.ID] && ids[o.A] && ids[o.B] {
			in.Links = append(in.Links, o)
		}
	}
}

func nodeByID(t topology, id string) (topoNode, bool) {
	for _, n := range t.Nodes {
		if n.ID == id {
			return n, true
		}
	}
	if id == "self" {
		return topoNode{ID: "self", Kind: "self"}, true
	}
	return topoNode{}, false
}

func linkByID(t topology, id string) (topoLink, bool) {
	for _, l := range t.Links {
		if l.ID == id {
			return l, true
		}
	}
	return topoLink{}, false
}

func roleOf(t topology, n topoNode) string {
	if n.Kind == "self" {
		return t.SelfRole
	}
	return n.Kind
}

func nodeName(n topoNode) string {
	return firstNonEmpty(n.Label, n.IP, n.ID)
}

// fleetEnd is one server taking part in a tunnel.
type fleetEnd struct {
	node topoNode
	host string // how the other end reaches it
	ex   hostExec
}

func (s *panelServer) endOf(t topology, id string) (fleetEnd, error) {
	n, ok := nodeByID(t, id)
	if !ok {
		return fleetEnd{}, errors.New("یکی از سرورهای این کابل پیدا نشد")
	}
	if n.Kind == "self" {
		cfg := s.config()
		return fleetEnd{node: n, host: publicHost(&cfg), ex: fleetExecFor(s, n)}, nil
	}
	if n.State != "ready" {
		return fleetEnd{}, fmt.Errorf("سرور «%s» هنوز آماده نیست؛ اول آن را اضافه و نصب کنید", nodeName(n))
	}
	return fleetEnd{node: n, host: n.IP, ex: fleetExecFor(s, n)}, nil
}

// ---------------------------------------------------------------------------
// adding a server
// ---------------------------------------------------------------------------

type fleetStep struct {
	Name  string `json:"name"`
	State string `json:"state"` // run, ok, bad, warn, skip
	Note  string `json:"note,omitempty"`
}

type fleetJob struct {
	mu      sync.Mutex
	log     benchJob
	steps   []fleetStep
	started time.Time
	done    bool
	ok      bool
}

type fleetJobView struct {
	Steps   []fleetStep `json:"steps"`
	Tail    string      `json:"tail"`
	Started int64       `json:"started"`
	Done    bool        `json:"done"`
	OK      bool        `json:"ok"`
}

func (j *fleetJob) begin(name string) int {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.steps = append(j.steps, fleetStep{Name: name, State: "run"})
	_, _ = fmt.Fprintf(&j.log, "\n▶ %s\n", name)
	return len(j.steps) - 1
}

func (j *fleetJob) end(i int, state, note string) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.steps[i].State, j.steps[i].Note = state, note
}

func (j *fleetJob) finish(ok bool) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.done, j.ok = true, ok
}

func (j *fleetJob) view() *fleetJobView {
	j.mu.Lock()
	steps := append([]fleetStep(nil), j.steps...)
	v := &fleetJobView{Steps: steps, Started: j.started.Unix(), Done: j.done, OK: j.ok}
	j.mu.Unlock()
	tail := cleanBench(j.log.output())
	if len(tail) > 6000 {
		tail = tail[len(tail)-6000:]
	}
	v.Tail = tail
	return v
}

type fleetAddReq struct {
	ID       string `json:"id"`
	Label    string `json:"label"`
	Kind     string `json:"kind"` // iran or kharej
	IP       string `json:"ip"`
	SSHPort  int    `json:"ssh_port"`
	SSHUser  string `json:"ssh_user"`
	Password string `json:"password"`
	Key      string `json:"key"`
	Upgrade  bool   `json:"upgrade"`
	Bench    bool   `json:"bench"`
}

func validHost(h string) bool {
	return net.ParseIP(h) != nil || validDomain(h)
}

func (s *panelServer) handleFleetAdd(w http.ResponseWriter, r *http.Request) {
	var req fleetAddReq
	if err := readJSON(r, &req); err != nil {
		jsonError(w, http.StatusBadRequest, "bad request")
		return
	}
	req.IP = strings.TrimSpace(req.IP)
	req.SSHUser = firstNonEmpty(strings.TrimSpace(req.SSHUser), "root")
	if req.SSHPort == 0 {
		req.SSHPort = 22
	}
	if req.Kind != "iran" && req.Kind != "kharej" {
		jsonError(w, http.StatusBadRequest, "نوع سرور باید ایران یا خارج باشد")
		return
	}
	if !validHost(req.IP) {
		jsonError(w, http.StatusBadRequest, "IP سرور معتبر نیست")
		return
	}
	if req.SSHPort < 1 || req.SSHPort > 65535 || len(req.SSHUser) > 32 || len(req.Label) > 40 {
		jsonError(w, http.StatusBadRequest, "پورت یا کاربر SSH معتبر نیست")
		return
	}

	s.fleetMu.Lock()
	if j := s.fleetJobs[req.ID]; req.ID != "" && j != nil && !j.view().Done {
		s.fleetMu.Unlock()
		jsonError(w, http.StatusConflict, "نصب این سرور در حال انجام است")
		return
	}
	s.fleetMu.Unlock()

	// a server added before may be set up again with the mother's own key
	var useKey bool
	var node topoNode
	err := s.updateTopology(func(t *topology) error {
		idx := -1
		for i, n := range t.Nodes {
			if req.ID != "" && n.ID == req.ID {
				idx = i
			} else if n.Kind != "self" && n.IP == req.IP && n.State != "" {
				return fmt.Errorf("سرور %s قبلاً با نام «%s» اضافه شده است", req.IP, nodeName(n))
			}
		}
		if idx >= 0 && t.Nodes[idx].Kind == "self" {
			return errors.New("سرور مادر نیازی به افزودن ندارد")
		}
		if idx < 0 {
			if len(t.Nodes) >= maxTopoItems {
				return errors.New("تعداد سرورها زیاد است")
			}
			n := topoNode{ID: "n" + randomToken(5), Kind: req.Kind}
			// place it on a ring around the mother so the designer shows it
			a := float64(len(t.Nodes)) * 2 * math.Pi / 7
			n.X, n.Y = math.Round(260*math.Cos(a)/20)*20, math.Round(260*math.Sin(a)/20)*20
			t.Nodes = append(t.Nodes, n)
			idx = len(t.Nodes) - 1
		}
		n := &t.Nodes[idx]
		same := n.HostKey != "" && n.IP == req.IP && n.SSHPort == req.SSHPort && n.SSHUser == req.SSHUser
		useKey = same && req.Password == "" && strings.TrimSpace(req.Key) == ""
		if !useKey && req.Password == "" && strings.TrimSpace(req.Key) == "" {
			return errors.New("رمز یا کلید SSH سرور را وارد کنید")
		}
		if !same {
			n.HostKey, n.HostKeyType = "", ""
		}
		n.Kind, n.IP, n.SSHUser, n.SSHPort = req.Kind, req.IP, req.SSHUser, req.SSHPort
		if strings.TrimSpace(req.Label) != "" {
			n.Label = strings.TrimSpace(req.Label)
		}
		if n.Label == "" {
			n.Label = req.Kind + "-" + strconv.Itoa(len(t.Nodes))
		}
		n.State, n.Error = "installing", ""
		if n.AddedAt == 0 {
			n.AddedAt = time.Now().Unix()
		}
		node = *n
		return checkTopology(t)
	})
	if err != nil {
		jsonError(w, http.StatusBadRequest, err.Error())
		return
	}
	job := &fleetJob{started: time.Now()}
	s.fleetMu.Lock()
	s.fleetJobs[node.ID] = job
	s.fleetMu.Unlock()
	tgt := sshTarget{Host: node.IP, Port: node.SSHPort, User: node.SSHUser, Password: req.Password, Key: req.Key}
	s.audit(r, "fleet-add", node.Label+" "+node.IP+" ("+node.Kind+")")
	go s.installServer(node, tgt, useKey, req.Upgrade, req.Bench, job)
	jsonOK(w, map[string]any{"id": node.ID})
}

const factsScript = `export LC_ALL=C
echo "hostname=$(hostname)"
. /etc/os-release 2>/dev/null; echo "os=${PRETTY_NAME:-$(uname -s)}"
echo "kernel=$(uname -r)"
echo "arch=$(uname -m)"
echo "virt=$(systemd-detect-virt 2>/dev/null || echo unknown)"
echo "cpu=$(awk -F: '/model name/{sub(/^ +/,"",$2);print $2;exit}' /proc/cpuinfo)"
echo "cores=$(nproc 2>/dev/null || grep -c ^processor /proc/cpuinfo)"
echo "mem=$(awk '/MemTotal/{printf "%.0f", $2*1024}' /proc/meminfo)"
echo "disk=$(df -P -B1 / | awk 'NR==2{print $2}')"
B=` + remoteBin + `; [ -x "$B" ] || B=` + legacyBinary + `
echo "agent=$("$B" -version 2>&1 | awk '{print $2}')"
echo "uid=$(id -u)"
`

func parseFacts(out string) (nodeFacts, string) {
	var f nodeFacts
	var uid string
	for _, line := range strings.Split(out, "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok {
			continue
		}
		v = strings.TrimSpace(v)
		switch k {
		case "hostname":
			f.Hostname = v
		case "os":
			f.OS = v
		case "kernel":
			f.Kernel = v
		case "arch":
			f.Arch = v
		case "virt":
			f.Virt = v
		case "cpu":
			f.CPU = v
		case "cores":
			f.Cores, _ = strconv.Atoi(v)
		case "mem":
			f.MemTotal, _ = strconv.ParseUint(v, 10, 64)
		case "disk":
			f.DiskTotal, _ = strconv.ParseUint(v, 10, 64)
		case "agent":
			f.Agent = v
		case "uid":
			uid = v
		}
	}
	return f, uid
}

const upgradeScript = `export DEBIAN_FRONTEND=noninteractive NEEDRESTART_MODE=a
if command -v apt-get >/dev/null 2>&1; then
  O="-o DPkg::Lock::Timeout=600"
  apt-get $O update && apt-get $O -y -o Dpkg::Options::=--force-confdef -o Dpkg::Options::=--force-confold upgrade
elif command -v dnf >/dev/null 2>&1; then dnf -y upgrade
elif command -v yum >/dev/null 2>&1; then yum -y update
else echo "no known package manager"; exit 1; fi
`

const prereqScript = `export DEBIAN_FRONTEND=noninteractive NEEDRESTART_MODE=a
if command -v apt-get >/dev/null 2>&1; then
  O="-o DPkg::Lock::Timeout=600"
  apt-get $O update -qq >/dev/null 2>&1 || true
  apt-get $O install -y -o Dpkg::Options::=--force-confdef -o Dpkg::Options::=--force-confold curl wget ca-certificates iproute2 iptables iputils-ping procps tar
elif command -v dnf >/dev/null 2>&1; then dnf -y install curl wget ca-certificates iproute iptables iputils procps-ng tar
elif command -v yum >/dev/null 2>&1; then yum -y install curl wget ca-certificates iproute iptables iputils procps-ng tar
else echo "no known package manager"; exit 1; fi
install -d -m 700 /etc/Midos-tunnel/tunnels
`

// unameArch maps uname -m onto Go's architecture names.
func unameArch(m string) string {
	switch m {
	case "x86_64", "amd64":
		return "amd64"
	case "aarch64", "arm64":
		return "arm64"
	}
	return m
}

// newerVersion reports whether a is a later release than b ("2.3.1" > "2.3.0").
func newerVersion(a, b string) bool {
	pa, pb := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(pa) || i < len(pb); i++ {
		var x, y int
		if i < len(pa) {
			x, _ = strconv.Atoi(pa[i])
		}
		if i < len(pb) {
			y, _ = strconv.Atoi(pb[i])
		}
		if x != y {
			return x > y
		}
	}
	return false
}

func (s *panelServer) installServer(node topoNode, tgt sshTarget, useKey, upgrade, bench bool, job *fleetJob) {
	ctx, cancel := context.WithTimeout(context.Background(), fleetInstallTimeout)
	defer cancel()
	failed := func(i int, msg string) {
		s.setNode(node.ID, func(n *topoNode) { n.State, n.Error = "error", msg })
		job.end(i, "bad", msg)
		job.finish(false)
		s.notify("⚠️ نصب سرور «" + nodeName(node) + "» ناموفق بود: " + msg)
	}

	// 1) the first login
	i := job.begin("اتصال SSH و بررسی دسترسی root")
	var ex hostExec
	if useKey {
		ex = fleetExecFor(s, node)
	} else {
		bx, fp, closeFn, err := fleetBootstrap(tgt)
		if err != nil {
			failed(i, err.Error())
			return
		}
		defer closeFn()
		ex = bx
		node.HostKey = fp
	}
	uid, err := runOut(ctx, ex, "id -u")
	if err != nil {
		failed(i, "اجرای فرمان روی سرور ناموفق بود: "+err.Error())
		return
	}
	if uid != "0" {
		failed(i, "کاربر SSH باید root باشد")
		return
	}
	job.end(i, "ok", "")

	// 2) the mother's key, then every later step goes through it
	i = job.begin("نصب کلید سرور مادر")
	line, err := s.fleetAuthorizedLine()
	if err != nil {
		failed(i, err.Error())
		return
	}
	if !useKey {
		cmd := `set -e; umask 077; mkdir -p ~/.ssh; touch ~/.ssh/authorized_keys; L=` + shQuote(line) + `
grep -qF "$(echo "$L" | awk '{print $2}')" ~/.ssh/authorized_keys || echo "$L" >> ~/.ssh/authorized_keys`
		if err := ex.Run(ctx, cmd, nil, &job.log); err != nil {
			failed(i, "نوشتن authorized_keys ناموفق بود: "+err.Error())
			return
		}
		s.setNode(node.ID, func(n *topoNode) { n.HostKey, n.HostKeyType = node.HostKey, "" })
		kx := fleetExecFor(s, node)
		if _, err := runOut(ctx, kx, "true"); err != nil {
			failed(i, "ورود با کلید سرور مادر ناموفق بود (PubkeyAuthentication در sshd خاموش است؟): "+err.Error())
			return
		}
		ex = kx
	}
	job.end(i, "ok", "رمز SSH ذخیره نمی‌شود؛ مدیریت با کلید انجام می‌شود")

	// 3) what the server is
	i = job.begin("خواندن مشخصات سرور")
	out, err := runOut(ctx, ex, factsScript)
	if err != nil {
		failed(i, err.Error())
		return
	}
	facts, _ := parseFacts(out)
	s.setNode(node.ID, func(n *topoNode) { n.Facts = &facts })
	job.end(i, "ok", strings.TrimSpace(facts.OS+" · "+facts.Arch))

	// 4) system update
	i = job.begin("به‌روزرسانی سیستم‌عامل")
	if !upgrade {
		job.end(i, "skip", "به انتخاب شما رد شد")
	} else if err := ex.Run(ctx, upgradeScript, nil, &job.log); err != nil {
		job.end(i, "warn", "به‌روزرسانی کامل نشد؛ ادامه می‌دهیم ("+err.Error()+")")
	} else {
		job.end(i, "ok", "")
	}

	// 5) prerequisites
	i = job.begin("نصب پیش‌نیازها")
	if err := ex.Run(ctx, prereqScript, nil, &job.log); err != nil {
		failed(i, "نصب بسته‌ها ناموفق بود: "+err.Error())
		return
	}
	job.end(i, "ok", "")

	// 6) the tunnel binary and its service template
	i = job.begin("نصب midos-tunnel")
	note, err := s.installBinary(ctx, ex, facts, &job.log)
	if err != nil {
		failed(i, err.Error())
		return
	}
	job.end(i, "ok", note)

	// 7) the kernel-tunnel base; a direct tunnel works without it
	i = job.begin("آماده‌سازی تانل کرنلی")
	if script, err := s.kernelScript(); err != nil {
		job.end(i, "skip", "اسکریپت نصب روی سرور مادر نیست")
	} else {
		cmd := "cat > " + remoteInstaller + ".new && chmod 755 " + remoteInstaller + ".new && mv -f " + remoteInstaller + ".new " + remoteInstaller +
			" && bash " + remoteInstaller + " prepare-kernel"
		if node.Kind == "self" {
			cmd = "bash " + remoteInstaller + " prepare-kernel"
		}
		if err := ex.Run(ctx, cmd, bytes.NewReader(script), &job.log); err != nil {
			job.end(i, "warn", "پیش‌نیاز کرنلی کامل نصب نشد؛ تانل مستقیم کار می‌کند")
		} else {
			job.end(i, "ok", "")
		}
	}

	if out, err := runOut(ctx, ex, factsScript); err == nil {
		facts, _ = parseFacts(out)
	}
	s.setNode(node.ID, func(n *topoNode) { n.State, n.Error, n.Facts = "ready", "", &facts })
	node.State = "ready"

	// 8) benchmark in the background
	i = job.begin("بنچمارک سرور (bench.sh)")
	if !bench {
		job.end(i, "skip", "به انتخاب شما رد شد")
	} else if err := s.startBench(node.ID, node.IP, func(ctx context.Context, w io.Writer) error {
		return fleetExecFor(s, node).Run(ctx, benchCommand, nil, w)
	}); err != nil {
		job.end(i, "warn", err.Error())
	} else {
		job.end(i, "ok", "در پس‌زمینه شروع شد؛ نتیجه روی کارت سرور می‌آید")
	}
	job.finish(true)
	s.notify("🖥️ سرور «" + nodeName(node) + "» (" + node.IP + ") به ناوگان اضافه و آماده شد")
}

// endStaleInstalls fails the installs the panel was running when it stopped,
// so those servers can be set up again instead of showing "installing" for good.
func (s *panelServer) endStaleInstalls() {
	stale := false
	for _, n := range s.loadTopology().Nodes {
		stale = stale || n.State == "installing"
	}
	if !stale {
		return
	}
	_ = s.updateTopology(func(t *topology) error {
		for i := range t.Nodes {
			if t.Nodes[i].State == "installing" {
				t.Nodes[i].State, t.Nodes[i].Error = "error", "نصب با ری‌استارت پنل نیمه‌کاره ماند؛ دوباره نصب کنید"
			}
		}
		return nil
	})
}

// ensureBinary installs this release on a remote server whose binary is missing
// (an older install has only the old path) or older. This server
// always runs the current binary already.
func (s *panelServer) ensureBinary(ctx context.Context, e fleetEnd, log io.Writer) error {
	if e.node.Kind == "self" {
		return nil
	}
	cur, _ := runOut(ctx, e.ex, "test -x "+remoteBin+" && "+remoteBin+" -version 2>&1 | awk '{print $2}'")
	if cur != "" && !newerVersion(version, cur) {
		return nil
	}
	f := nodeFacts{}
	if e.node.Facts != nil {
		f = *e.node.Facts
	}
	if f.Arch == "" {
		arch, err := runOut(ctx, e.ex, "uname -m")
		if err != nil {
			return err
		}
		f.Arch = arch
	}
	f.Agent = cur
	_, err := s.installBinary(ctx, e.ex, f, log)
	return err
}

// installBinary puts this panel's midos-tunnel on the server (or the published one
// when the server has another CPU architecture), never downgrading it.
func (s *panelServer) installBinary(ctx context.Context, ex hostExec, f nodeFacts, log io.Writer) (string, error) {
	unit := "install -d -m 700 /etc/Midos-tunnel/tunnels\nif [ ! -f /etc/systemd/system/midos-tunnel@.service ]; then\ncat > /etc/systemd/system/midos-tunnel@.service <<'UNIT'\n" +
		tunnelUnitTemplate + "UNIT\nfi\nsystemctl daemon-reload\n" +
		// an older install moves its tunnels to the current unit names
		remoteBin + " migrate >/dev/null 2>&1 || true\n"
	if f.Agent != "" && !newerVersion(version, f.Agent) {
		return "نسخهٔ " + f.Agent + " از قبل نصب است", ex.Run(ctx, unit, nil, log)
	}
	// running tunnels move onto the new binary
	restart := "systemctl try-restart 'midos-tunnel@*' >/dev/null 2>&1 || true\n"
	if unameArch(f.Arch) == runtime.GOARCH {
		path, sum, err := selfBinary()
		if err != nil {
			return "", err
		}
		bin, err := os.Open(path)
		if err != nil {
			return "", err
		}
		defer bin.Close()
		cmd := "set -e\nT=" + remoteBin + ".new\ncat > \"$T\"\necho '" + sum + "  '\"$T\" | sha256sum -c --quiet\nchmod 755 \"$T\"\nmv -f \"$T\" " + remoteBin + "\n" + unit + restart
		if err := ex.Run(ctx, cmd, bin, log); err != nil {
			return "", fmt.Errorf("کپی باینری ناموفق بود: %w", err)
		}
		return "نسخهٔ " + version + " از سرور مادر کپی شد", nil
	}
	asset := "midos-tunnel"
	switch unameArch(f.Arch) {
	case "arm64":
		asset = "midos-tunnel-arm64"
	case "amd64":
	default:
		return "", fmt.Errorf("معماری %s پشتیبانی نمی‌شود", f.Arch)
	}
	cmd := "set -e\nT=" + remoteBin + ".new\nB=" + updateBase + "/" + asset + "\n" +
		"curl -fsSL \"$B\" -o \"$T\"\nS=$(curl -fsSL \"$B.sha256\" | awk '{print $1}')\necho \"$S  $T\" | sha256sum -c --quiet\nchmod 755 \"$T\"\nmv -f \"$T\" " + remoteBin + "\n" + unit + restart
	if err := ex.Run(ctx, cmd, nil, log); err != nil {
		return "", fmt.Errorf("دانلود باینری %s ناموفق بود: %w", f.Arch, err)
	}
	return "نسخهٔ منتشرشده برای " + f.Arch + " نصب شد", nil
}

// ---------------------------------------------------------------------------
// live resources
// ---------------------------------------------------------------------------

type nodeTunnel struct {
	Name     string  `json:"name"`
	Active   string  `json:"active"`
	Carriers int     `json:"carriers"`
	RTT      float64 `json:"rtt_ms,omitempty"`
	Bytes    uint64  `json:"bytes,omitempty"` // moved since the relay started
}

type nodeStats struct {
	At        int64        `json:"at"`
	Error     string       `json:"error,omitempty"`
	CPU       float64      `json:"cpu"`
	MemUsed   uint64       `json:"mem_used"`
	MemTotal  uint64       `json:"mem_total"`
	DiskUsed  uint64       `json:"disk_used"`
	DiskTotal uint64       `json:"disk_total"`
	Load      string       `json:"load"`
	Uptime    int64        `json:"uptime_s"`
	Rx        float64      `json:"rx"` // bytes per second
	Tx        float64      `json:"tx"`
	Tunnels   []nodeTunnel `json:"tunnels"`
	Kernel    string       `json:"kernel,omitempty"` // kernel tunnel unit state, "" when none
	// bytes through each kernel peer's interface (rx+tx), by the peer's public
	// IP, since the interface was last created
	KernelPeers map[string]uint64 `json:"kernel_peers,omitempty"`
}

type rawSample struct {
	busy, total uint64
	rx, tx      uint64
	at          time.Time
}

const statsScript = `export LC_ALL=C
echo "cpu $(head -1 /proc/stat)"
awk '/^MemTotal/{t=$2}/^MemAvailable/{a=$2}END{printf "mem %.0f %.0f\n", t*1024, (t-a)*1024}' /proc/meminfo
df -P -B1 / | awk 'NR==2{print "disk", $2, $3}'
echo "load $(cut -d' ' -f1-3 /proc/loadavg)"
echo "up $(cut -d' ' -f1 /proc/uptime)"
d=$(ip -o route get 1.1.1.1 2>/dev/null | sed -n 's/.* dev \([^ ]*\).*/\1/p')
[ -n "$d" ] && echo "net $(cat /sys/class/net/$d/statistics/rx_bytes) $(cat /sys/class/net/$d/statistics/tx_bytes)"
for f in /etc/Midos-tunnel/tunnels/*.json; do
  [ -e "$f" ] || continue
  n=$(basename "$f" .json)
  a=$(systemctl is-active "midos-tunnel@$n" 2>/dev/null)
  [ "$a" = active ] || { o=$(systemctl is-active "midos-veil@$n" 2>/dev/null); [ "$o" = active ] && a=$o; }
  j=""
  st=$(grep -o '"stats_listen": *"[^"]*"' "$f" | sed 's/.*"\([^"]*\)"$/\1/')
  if [ "$a" = active ] && [ -n "$st" ]; then j=$(curl -s -m 1 "http://$st/stats" 2>/dev/null | tr -d '\n'); fi
  echo "tun $n $a $j"
done
[ -f /etc/Midos-tunnel/config ] && echo "kernel $(systemctl is-active Midos-tunnel.service 2>/dev/null)"
[ -f /etc/Midos-tunnel/config ] && awk -F= '/^PEER_[0-9]+_(IP|IFACE)=/ { split($1, k, "_"); gsub(/["\047]/, "", $2); v[k[2] " " k[3]] = $2; n[k[2]] = 1 }
  END { for (i in n) print v[i " IP"], v[i " IFACE"] }' /etc/Midos-tunnel/config | while read -r ip ifc; do
  s=/sys/class/net/$ifc/statistics
  [ -n "$ifc" ] && [ -r "$s/rx_bytes" ] && echo "kif $ip $(cat "$s/rx_bytes") $(cat "$s/tx_bytes")"
done
true
`

func parseStats(out string, prev *rawSample, now time.Time) (nodeStats, rawSample) {
	st := nodeStats{At: now.Unix(), Tunnels: []nodeTunnel{}}
	cur := rawSample{at: now}
	u64 := func(s string) uint64 { v, _ := strconv.ParseUint(s, 10, 64); return v }
	sc := bufio.NewScanner(strings.NewReader(out))
	sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) == 0 {
			continue
		}
		switch f[0] {
		case "cpu":
			// cpu  user nice system idle iowait irq softirq steal ...
			if len(f) >= 6 && f[1] == "cpu" {
				var total uint64
				for _, x := range f[2:] {
					total += u64(x)
				}
				idle := u64(f[5])
				if len(f) > 6 {
					idle += u64(f[6])
				}
				cur.total, cur.busy = total, total-idle
			}
		case "mem":
			if len(f) == 3 {
				st.MemTotal, st.MemUsed = u64(f[1]), u64(f[2])
			}
		case "disk":
			if len(f) == 3 {
				st.DiskTotal, st.DiskUsed = u64(f[1]), u64(f[2])
			}
		case "load":
			st.Load = strings.Join(f[1:], " ")
		case "up":
			if len(f) == 2 {
				v, _ := strconv.ParseFloat(f[1], 64)
				st.Uptime = int64(v)
			}
		case "net":
			if len(f) == 3 {
				cur.rx, cur.tx = u64(f[1]), u64(f[2])
			}
		case "tun":
			if len(f) < 3 {
				continue
			}
			t := nodeTunnel{Name: f[1], Active: f[2]}
			if len(f) > 3 {
				var d statsDoc
				if json.Unmarshal([]byte(strings.Join(f[3:], " ")), &d) == nil {
					t.Carriers = len(d.Carriers)
					t.Bytes = bytesFromStats(&d)
					for _, c := range d.Carriers {
						if c.RTTms > 0 && (t.RTT == 0 || c.RTTms < t.RTT) {
							t.RTT = c.RTTms
						}
					}
				}
			}
			st.Tunnels = append(st.Tunnels, t)
		case "kif":
			if len(f) == 4 {
				if st.KernelPeers == nil {
					st.KernelPeers = map[string]uint64{}
				}
				st.KernelPeers[f[1]] = u64(f[2]) + u64(f[3])
			}
		case "kernel":
			if len(f) == 2 {
				st.Kernel = f[1]
			} else {
				st.Kernel = "unknown"
			}
		}
	}
	if prev != nil && !prev.at.IsZero() {
		// counters only grow; a reboot in between resets them, so a reading
		// that went backwards is skipped rather than wrapped around
		if cur.total > prev.total && cur.busy >= prev.busy {
			st.CPU = math.Min(100, 100*float64(cur.busy-prev.busy)/float64(cur.total-prev.total))
		}
		if secs := now.Sub(prev.at).Seconds(); secs > 0 {
			if cur.rx >= prev.rx {
				st.Rx = float64(cur.rx-prev.rx) / secs
			}
			if cur.tx >= prev.tx {
				st.Tx = float64(cur.tx-prev.tx) / secs
			}
		}
	}
	return st, cur
}

func (s *panelServer) fleetLoop() {
	t := time.NewTicker(fleetSampleEvery)
	defer t.Stop()
	s.fleetSample()
	last := time.Now()
	for now := range t.C {
		if time.Since(time.Unix(s.fleetSeen.Load(), 0)) > fleetIdleAfter {
			// nobody is looking: sample only as often as the traffic count
			// needs, and only while there are fleet tunnels to count
			if now.Sub(last) < usageInterval || len(usageCables(s.loadTopology())) == 0 {
				continue
			}
		}
		s.fleetSample()
		last = time.Now()
	}
}

func (s *panelServer) fleetSample() {
	t := s.loadTopology()
	nodes := []topoNode{{ID: "self", Kind: "self"}}
	for _, n := range t.Nodes {
		if n.Kind != "self" && n.State == "ready" {
			nodes = append(nodes, n)
		}
	}
	var wg sync.WaitGroup
	for _, n := range nodes {
		wg.Add(1)
		go func(n topoNode) {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
			defer cancel()
			ex := fleetExecFor(s, n)
			s.fleetMu.Lock()
			prev := s.fleetPrev[n.ID]
			s.fleetMu.Unlock()
			if prev.at.IsZero() {
				// rates need two readings: take the first one a second early
				if out, err := runOut(ctx, ex, statsScript); err == nil {
					_, prev = parseStats(out, nil, time.Now())
					time.Sleep(time.Second)
				}
			}
			out, err := runOut(ctx, ex, statsScript)
			now := time.Now()
			var st nodeStats
			if err != nil {
				st = nodeStats{At: now.Unix(), Error: "سرور در دسترس نیست: " + err.Error(), Tunnels: []nodeTunnel{}}
			} else {
				var cur rawSample
				st, cur = parseStats(out, &prev, now)
				s.fleetMu.Lock()
				s.fleetPrev[n.ID] = cur
				s.fleetMu.Unlock()
			}
			if n.Kind == "self" && s.selfFacts.Load() == nil {
				if fo, err := runOut(ctx, ex, factsScript); err == nil {
					f, _ := parseFacts(fo)
					s.selfFacts.Store(&f)
				}
			}
			s.fleetMu.Lock()
			s.fleetStats[n.ID] = st
			s.fleetMu.Unlock()
		}(n)
	}
	wg.Wait()
	s.recordFleetUsage(t)
}

type fleetView struct {
	topoNode
	Role  string        `json:"role"`
	Host  string        `json:"host"`
	Stats *nodeStats    `json:"stats,omitempty"`
	Job   *fleetJobView `json:"job,omitempty"`
}

func (s *panelServer) handleFleet(w http.ResponseWriter, r *http.Request) {
	s.fleetSeen.Store(time.Now().Unix())
	s.fleetOnce.Do(func() { go s.fleetLoop() })

	t := s.loadTopology()
	cfg := s.config()
	hasSelf := false
	for _, n := range t.Nodes {
		if n.Kind == "self" {
			hasSelf = true
		}
	}
	if !hasSelf {
		host, _ := os.Hostname()
		t.Nodes = append([]topoNode{{ID: "self", Kind: "self", Label: host}}, t.Nodes...)
	}
	views := make([]fleetView, 0, len(t.Nodes))
	s.fleetMu.Lock()
	for _, n := range t.Nodes {
		v := fleetView{topoNode: n, Role: roleOf(t, n), Host: n.IP}
		if n.Kind == "self" {
			v.Host = publicHost(&cfg)
			v.State = "ready"
			v.Facts = s.selfFacts.Load()
		}
		if st, ok := s.fleetStats[n.ID]; ok {
			v.Stats = &st
		}
		if j := s.fleetJobs[n.ID]; j != nil {
			v.Job = j.view()
		}
		views = append(views, v)
	}
	s.fleetMu.Unlock()
	for i := range t.Links {
		t.Links[i].PSK = ""
	}
	jsonOK(w, map[string]any{"self_role": t.SelfRole, "nodes": views, "links": t.Links})
}

// ---------------------------------------------------------------------------
// tunnels between two servers
// ---------------------------------------------------------------------------

var defaultCarrierPort = map[string]int{"tls": 443, "ws": 443, "tcp": 8080, "stealth": 8443, "kcp": 4000, "quic": 8443, "grpc": 2096, "dns": 53}

// splitList splits a list a person typed: entries may be separated by Latin or
// Persian commas, semicolons or spaces, and Persian or Arabic digits count as
// digits ("37825،62623" is two ports).
func splitList(s string) []string {
	s = strings.Map(func(r rune) rune {
		switch {
		case r >= '۰' && r <= '۹':
			return '0' + (r - '۰')
		case r >= '٠' && r <= '٩':
			return '0' + (r - '٠')
		}
		return r
	}, s)
	return strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == '،' || r == ';' || unicode.IsSpace(r) })
}

func carrierProto(tr string) string {
	switch tr {
	case "kcp", "quic", "dns":
		return "udp"
	}
	return "tcp"
}

func setListener(c *Config, tr, addr string) {
	switch tr {
	case "tls", "ws":
		c.Listen = addr
	case "tcp":
		c.TCPListen = addr
	case "stealth":
		c.StealthListen = addr
	case "kcp":
		c.KCPListen = addr
	case "quic":
		c.QUICListen = addr
	case "icmp":
		c.ICMPListen = "0.0.0.0"
	case "grpc":
		c.GRPCListen = addr
	case "dns":
		c.DNSListen = addr
	}
}

// fleetDirectConfigs builds both ends of a direct cable: the Iran end (users
// come in on the forwarded ports) and the Kharej end (streams leave to the
// services), plus the ports each end opens in its firewall.
func fleetDirectConfigs(l topoLink, name, iranHost, kharejHost string) (ic, kc *Config, iOpen, kOpen []string, err error) {
	tr := firstNonEmpty(l.Transport, "tls")
	if tr == "ssh" || !transports[tr] {
		return nil, nil, nil, nil, fmt.Errorf("ترنسپورت %q برای کابل پشتیبانی نمی‌شود", tr)
	}
	port := l.Port
	if port == 0 {
		port = defaultCarrierPort[tr]
	}
	carriers := l.Carriers
	if carriers <= 0 {
		carriers = 2
	}
	if tr == "icmp" {
		carriers = 1
	}
	c := &Config{
		Mode: "client", Reverse: l.Reverse, PSK: randomToken(24), Name: name,
		Preset: firstNonEmpty(l.Preset, "balance"), Carriers: carriers, MuxVersion: l.MuxVersion,
		ServerName: "www.cloudflare.com", Fingerprint: "chrome", Insecure: true,
		Ports: splitList(l.Ports), UDPPorts: splitList(l.UDPPorts),
		StatsListen: fmt.Sprintf("127.0.0.1:%d", 38000+int(fnvHash(name)%900)),
	}
	if len(c.Ports) == 0 && len(c.UDPPorts) == 0 {
		return nil, nil, nil, nil, errors.New("حداقل یک پورت منتقل‌شونده وارد کنید")
	}
	if tr == "ws" {
		c.WSPath = "/" + randomToken(6)
	}
	pstr := strconv.Itoa(port)
	if !l.Reverse {
		srv := net.JoinHostPort(kharejHost, pstr)
		if tr == "icmp" {
			srv = kharejHost
		}
		c.Endpoints = []Endpoint{{Transport: tr, Server: srv}}
	} else {
		setListener(c, tr, "0.0.0.0:"+pstr)
		if tr != "icmp" {
			iOpen = append(iOpen, pstr+"/"+carrierProto(tr))
		}
	}
	if ic, err = validated(c); err != nil {
		return nil, nil, nil, nil, err
	}
	p, open, err := peerConfig(ic, iranHost)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	if kc, err = validated(p); err != nil {
		return nil, nil, nil, nil, err
	}
	maps, _ := ic.parsePorts()
	for _, m := range maps {
		iOpen = append(iOpen, fmt.Sprintf("%d/tcp", m.local))
	}
	udp, _ := ic.parseUDPPorts()
	for _, m := range udp {
		iOpen = append(iOpen, fmt.Sprintf("%d/udp", m.local))
	}
	return ic, kc, iOpen, open, nil
}

// fleetTunnelName names a cable's tunnel the same on both servers.
func fleetTunnelName(l topoLink, iran, kharej topoNode) string {
	clean := func(s string) string {
		var b strings.Builder
		for _, r := range strings.ToLower(s) {
			if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
				b.WriteRune(r)
			} else if b.Len() > 0 && !strings.HasSuffix(b.String(), "-") {
				b.WriteByte('-')
			}
		}
		return strings.Trim(b.String(), "-")
	}
	a, k := clean(iran.Label), clean(kharej.Label)
	if len(a) > 8 {
		a = a[:8]
	}
	if len(k) > 8 {
		k = k[:8]
	}
	id := l.ID
	if len(id) > 5 {
		id = id[len(id)-5:]
	}
	name := strings.Trim(strings.Join([]string{"fl", firstNonEmpty(a, "ir"), firstNonEmpty(k, "kh"), clean(id)}, "-"), "-")
	for strings.Contains(name, "--") {
		name = strings.ReplaceAll(name, "--", "-")
	}
	return name
}

const deployScript = `set -e
NAME=%[1]s
BIN=` + remoteBin + `
[ -x "$BIN" ] || { echo "midos-tunnel is not installed on this server"; exit 1; }
systemctl stop "midos-tunnel@$NAME" >/dev/null 2>&1 || true
systemctl disable --now "midos-veil@$NAME" >/dev/null 2>&1 || true
for p in %[2]s; do
  if ss -lntu "sport = :$p" 2>/dev/null | tail -n +2 | grep -q .; then echo "port $p is already in use on this server"; exit 3; fi
done
install -d -m 700 /etc/Midos-tunnel/tunnels
tmp=$(mktemp); trap 'rm -f "$tmp"' EXIT
cat > "$tmp"
"$BIN" -config "$tmp" -check
install -m 600 "$tmp" "/etc/Midos-tunnel/tunnels/$NAME.json"
if [ ! -f /etc/systemd/system/midos-tunnel@.service ]; then
cat > /etc/systemd/system/midos-tunnel@.service <<'UNIT'
%[4]sUNIT
fi
for p in %[3]s; do
  if command -v ufw >/dev/null 2>&1 && ufw status 2>/dev/null | grep -q "Status: active"; then
    ufw allow "$p" >/dev/null
  elif command -v firewall-cmd >/dev/null 2>&1 && firewall-cmd --state >/dev/null 2>&1; then
    firewall-cmd -q --permanent --add-port="$p" && firewall-cmd -q --reload
  fi
done
systemctl daemon-reload
systemctl enable "midos-tunnel@$NAME" >/dev/null 2>&1
systemctl restart "midos-tunnel@$NAME"
sleep 2
if ! systemctl is-active --quiet "midos-tunnel@$NAME"; then journalctl -u "midos-tunnel@$NAME" -n 20 --no-pager; exit 1; fi
echo "tunnel $NAME is running"
`

func listenPorts(c *Config) []string {
	seen := map[string]bool{}
	var out []string
	for _, a := range boundAddrs(c) {
		_, addr, _ := strings.Cut(a, " ")
		_, p, err := net.SplitHostPort(addr)
		if err == nil && p != "" && !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	return out
}

func deployTunnel(ctx context.Context, e fleetEnd, name string, c *Config, open []string) (string, error) {
	body, _ := json.MarshalIndent(c, "", "  ")
	cmd := fmt.Sprintf(deployScript, shQuote(name), strings.Join(listenPorts(c), " "), strings.Join(open, " "), tunnelUnitTemplate)
	var out bytes.Buffer
	err := e.ex.Run(ctx, cmd, bytes.NewReader(body), &out)
	return tailLines(out.String(), 30), err
}

func removeTunnelOn(ctx context.Context, e fleetEnd, name string) (string, error) {
	if !tunnelNameRE.MatchString(name) {
		return "", errors.New("bad tunnel name")
	}
	cmd := fmt.Sprintf(`N=%s; systemctl disable --now "midos-tunnel@$N" "midos-veil@$N" >/dev/null 2>&1 || true; rm -f "/etc/Midos-tunnel/tunnels/$N.json"; echo "tunnel $N removed"`, shQuote(name))
	out, err := runOut(ctx, e.ex, cmd)
	return out, err
}

func (s *panelServer) applyDirect(ctx context.Context, t topology, l topoLink) ([]stepResult, error) {
	a, err := s.endOf(t, l.A)
	if err != nil {
		return nil, err
	}
	b, err := s.endOf(t, l.B)
	if err != nil {
		return nil, err
	}
	iran, kharej := a, b
	if roleOf(t, a.node) == "kharej" {
		iran, kharej = b, a
	}
	name := l.Tunnel
	if name == "" || name == "kernel" {
		name = fleetTunnelName(l, iran.node, kharej.node)
	}
	ic, kc, iOpen, kOpen, err := fleetDirectConfigs(l, name, iran.host, kharej.host)
	if err != nil {
		return nil, err
	}
	var results []stepResult
	// the listening end first, so the dialing end finds it up
	order := []struct {
		e    fleetEnd
		c    *Config
		open []string
	}{{kharej, kc, kOpen}, {iran, ic, iOpen}}
	if l.Reverse {
		order[0], order[1] = order[1], order[0]
	}
	for _, o := range order {
		// a server still on an older binary is brought up to this panel's
		// version first, so both ends speak the same release
		var up bytes.Buffer
		if err := s.ensureBinary(ctx, o.e, &up); err != nil {
			results = append(results, stepResult{Step: nodeName(o.e.node) + " (" + o.e.host + ")", Error: err.Error(), Output: tailLines(up.String(), 20)})
			return results, fmt.Errorf("به‌روزرسانی باینری روی «%s» ناموفق بود", nodeName(o.e.node))
		}
		out, err := deployTunnel(ctx, o.e, name, o.c, o.open)
		res := stepResult{Step: nodeName(o.e.node) + " (" + o.e.host + ")", OK: err == nil, Output: out}
		if err != nil {
			res.Error = err.Error()
			results = append(results, res)
			return results, fmt.Errorf("نصب تانل روی «%s» ناموفق بود", nodeName(o.e.node))
		}
		results = append(results, res)
	}
	err = s.updateTopology(func(t *topology) error {
		for i := range t.Links {
			if t.Links[i].ID == l.ID {
				t.Links[i].Tunnel = name
			}
		}
		return nil
	})
	return results, err
}

// kernelGroup is every kernel cable of one Iran hub.
func kernelGroup(t topology, hub string) []topoLink {
	var out []topoLink
	for _, l := range t.Links {
		if l.Kind == "kernel" && (l.A == hub || l.B == hub) {
			out = append(out, l)
		}
	}
	return out
}

func otherEnd(l topoLink, id string) string {
	if l.A == id {
		return l.B
	}
	return l.A
}

func (s *panelServer) kernelRun(ctx context.Context, e fleetEnd, env []envKV, action string) (string, error) {
	script, err := s.kernelScript()
	if err != nil {
		return "", errors.New("اسکریپت نصب روی سرور مادر نیست")
	}
	cmd := "cat > " + remoteInstaller + ".new && chmod 755 " + remoteInstaller + ".new && mv -f " + remoteInstaller + ".new " + remoteInstaller +
		" && " + envPrefix(env) + " bash " + remoteInstaller + " " + action
	var stdin io.Reader = bytes.NewReader(script)
	if e.node.Kind == "self" {
		cmd, stdin = envPrefix(env)+" bash "+remoteInstaller+" "+action, nil
	}
	var out bytes.Buffer
	err = e.ex.Run(ctx, cmd, stdin, &out)
	return tailLines(out.String(), 30), err
}

// applyKernel (re)builds the kernel tunnel of one Iran hub with all of its
// Kharej servers. keep lists the cables to build; a Kharej server left out is
// torn down by the caller. The hub's servers share one mode and key (tmpl's
// mode), but each cable forwards its own ports, so every location answers on
// ports of its own.
func (s *panelServer) applyKernel(ctx context.Context, t topology, hubID string, keep []topoLink, tmpl topoLink) ([]stepResult, error) {
	hub, err := s.endOf(t, hubID)
	if err != nil {
		return nil, err
	}
	if net.ParseIP(hub.host) == nil {
		return nil, fmt.Errorf("IP سرور ایران «%s» معتبر نیست", nodeName(hub.node))
	}
	psk := ""
	used := map[int]bool{}
	for _, l := range keep {
		if l.PSK != "" {
			psk = l.PSK
		}
		if l.Slot > 0 {
			used[l.Slot] = true
		}
	}
	if psk == "" {
		psk = randomToken(16)
	}
	base, err := commonKernelEnv(kernelCreateReq{Mode: firstNonEmpty(tmpl.Mode, "esp"), PSK: psk, Watchdog: true})
	if err != nil {
		return nil, err
	}
	type peer struct {
		link topoLink
		end  fleetEnd
	}
	var peers []peer
	var specs []string
	for _, l := range keep {
		e, err := s.endOf(t, otherEnd(l, hubID))
		if err != nil {
			return nil, err
		}
		if net.ParseIP(e.host) == nil {
			return nil, fmt.Errorf("IP سرور «%s» معتبر نیست", nodeName(e.node))
		}
		if l.Slot == 0 {
			for n := 1; n <= 32; n++ {
				if !used[n] {
					l.Slot, used[n] = n, true
					break
				}
			}
			if l.Slot == 0 {
				return nil, errors.New("بیش از ۳۲ سرور خارج روی یک سرور ایران ممکن نیست")
			}
		}
		// a port the kernel installer would refuse stops the build here, with
		// the server named, before any server is touched
		for _, p := range append(splitList(l.Ports), splitList(l.UDPPorts)...) {
			if !kernelPortsRE.MatchString(p) {
				return nil, fmt.Errorf("پورت %q کابل «%s» برای تانل کرنلی معتبر نیست (عدد، بازه مثل 2000-3000 یا تغییر شماره مثل 37826:37825)", p, nodeName(e.node))
			}
		}
		l.PSK, l.Mode, l.Tunnel = psk, firstNonEmpty(tmpl.Mode, "esp"), "kernel"
		peers = append(peers, peer{l, e})
		specs = append(specs, e.host+":"+sanitizeLabel(e.node.Label, "kharej"+strconv.Itoa(l.Slot))+":"+strconv.Itoa(l.Slot))
	}

	var results []stepResult
	step := func(e fleetEnd, out string, err error) error {
		r := stepResult{Step: nodeName(e.node) + " (" + e.host + ")", OK: err == nil, Output: out}
		if err != nil {
			r.Error = err.Error()
		}
		results = append(results, r)
		return err
	}
	hubEnv := kv(append([]envKV{}, base...), "MIDOS_ROLE", "IRAN")
	hubEnv = kv(hubEnv, "MIDOS_SELF_IP", hub.host)
	hubEnv = kv(hubEnv, "MIDOS_PEERS", strings.Join(specs, ","))
	for _, p := range peers {
		slot := strconv.Itoa(p.link.Slot)
		hubEnv = kv(hubEnv, "MIDOS_PEER_"+slot+"_TCP", strings.Join(splitList(p.link.Ports), ","))
		hubEnv = kv(hubEnv, "MIDOS_PEER_"+slot+"_UDP", strings.Join(splitList(p.link.UDPPorts), ","))
	}
	out, err := s.kernelRun(ctx, hub, hubEnv, "provision")
	if step(hub, out, err) != nil {
		return results, fmt.Errorf("ساخت تانل کرنلی روی «%s» ناموفق بود", nodeName(hub.node))
	}
	allOK := true
	for _, p := range peers {
		env := kv(append([]envKV{}, base...), "MIDOS_ROLE", "KHAREJ")
		env = kv(env, "MIDOS_SELF_IP", p.end.host)
		env = kv(env, "MIDOS_IRAN_IP", hub.host)
		env = kv(env, "MIDOS_SLOT", strconv.Itoa(p.link.Slot))
		out, err := s.kernelRun(ctx, p.end, env, "provision")
		if step(p.end, out, err) != nil {
			allOK = false
		}
	}
	err = s.updateTopology(func(t *topology) error {
		for _, p := range peers {
			for i := range t.Links {
				if t.Links[i].ID == p.link.ID {
					l := &t.Links[i]
					l.PSK, l.Slot, l.Mode, l.Ports, l.UDPPorts, l.Tunnel = p.link.PSK, p.link.Slot, p.link.Mode, p.link.Ports, p.link.UDPPorts, "kernel"
				}
			}
		}
		return nil
	})
	if err == nil && !allOK {
		err = errors.New("تانل کرنلی روی بعضی سرورهای خارج ساخته نشد")
	}
	return results, err
}

func kernelHub(t topology, l topoLink) string {
	if n, ok := nodeByID(t, l.A); ok && roleOf(t, n) == "iran" {
		return l.A
	}
	return l.B
}

func (s *panelServer) handleFleetApply(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID string `json:"id"`
	}
	if err := readJSON(r, &req); err != nil {
		jsonError(w, http.StatusBadRequest, "bad request")
		return
	}
	t := s.loadTopology()
	l, ok := linkByID(t, req.ID)
	if !ok {
		jsonError(w, http.StatusNotFound, "این کابل پیدا نشد؛ صفحه را تازه کنید")
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	var results []stepResult
	var err error
	if l.Kind == "kernel" {
		hub := kernelHub(t, l)
		results, err = s.applyKernel(ctx, t, hub, kernelGroup(t, hub), l)
	} else {
		results, err = s.applyDirect(ctx, t, l)
	}
	if err != nil {
		s.audit(r, "fleet-apply", l.ID+" failed: "+err.Error())
		jsonError2(w, http.StatusBadGateway, err.Error(), results)
		return
	}
	a, _ := nodeByID(t, l.A)
	b, _ := nodeByID(t, l.B)
	s.audit(r, "fleet-apply", l.Kind+" "+nodeName(a)+" <-> "+nodeName(b))
	s.notify("🔌 تانل بین «" + nodeName(a) + "» و «" + nodeName(b) + "» ساخته شد")
	jsonOK(w, map[string]any{"ok": true, "results": results})
}

// unlink takes a cable's tunnel off its servers and the cable off the map.
func (s *panelServer) unlink(ctx context.Context, t topology, l topoLink) ([]stepResult, error) {
	results, firstErr := s.takeDown(ctx, t, l)
	err := s.updateTopology(func(t *topology) error {
		out := t.Links[:0]
		for _, x := range t.Links {
			if x.ID != l.ID {
				out = append(out, x)
			}
		}
		t.Links = out
		return nil
	})
	if firstErr == nil {
		firstErr = err
	}
	return results, firstErr
}

// takeDown removes a cable's tunnel from its servers and leaves the map as it
// is. A kernel cable's Iran hub is rebuilt with its other Kharej servers.
func (s *panelServer) takeDown(ctx context.Context, t topology, l topoLink) ([]stepResult, error) {
	var results []stepResult
	add := func(e fleetEnd, out string, err error) {
		r := stepResult{Step: nodeName(e.node) + " (" + e.host + ")", OK: err == nil, Output: out}
		if err != nil {
			r.Error = err.Error()
		}
		results = append(results, r)
	}
	var firstErr error
	keepErr := func(err error) {
		if err != nil && firstErr == nil {
			firstErr = err
		}
	}
	if l.Tunnel != "" && l.Kind == "direct" {
		for _, id := range []string{l.A, l.B} {
			e, err := s.endOf(t, id)
			if err != nil {
				keepErr(err)
				continue
			}
			out, err := removeTunnelOn(ctx, e, l.Tunnel)
			add(e, out, err)
			keepErr(err)
		}
	}
	if l.Tunnel != "" && l.Kind == "kernel" {
		hub := kernelHub(t, l)
		if e, err := s.endOf(t, otherEnd(l, hub)); err == nil {
			out, err := s.kernelRun(ctx, e, nil, "teardown")
			add(e, out, err)
			keepErr(err)
		} else {
			keepErr(err)
		}
		var rest []topoLink
		for _, x := range kernelGroup(t, hub) {
			if x.ID != l.ID && x.Tunnel != "" {
				rest = append(rest, x)
			}
		}
		if len(rest) > 0 {
			res, err := s.applyKernel(ctx, t, hub, rest, rest[0])
			results = append(results, res...)
			keepErr(err)
		} else if e, err := s.endOf(t, hub); err == nil {
			out, err := s.kernelRun(ctx, e, nil, "teardown")
			add(e, out, err)
			keepErr(err)
		}
	}
	return results, firstErr
}

// cableSettings copies the settings a person chooses for a cable from req onto
// l; the ends, the id and what the panel set (tunnel, key, slot) stay. Fields
// that do not apply to the cable's kind are cleared.
func cableSettings(l, req topoLink) topoLink {
	l.Kind, l.Ports, l.UDPPorts = req.Kind, req.Ports, req.UDPPorts
	if l.Kind == "kernel" {
		l.Mode = firstNonEmpty(req.Mode, "esp")
		l.Transport, l.Port, l.Reverse, l.Preset, l.Carriers, l.MuxVersion = "", 0, false, "", 0, 0
		return l
	}
	l.Mode = ""
	l.Transport, l.Port, l.Reverse = firstNonEmpty(req.Transport, "tls"), req.Port, req.Reverse
	l.Preset, l.Carriers, l.MuxVersion = firstNonEmpty(req.Preset, "balance"), req.Carriers, req.MuxVersion
	return l
}

// a kernel port term: 443, 2000-3000, or 12053:2053 (this server's 12053 to
// the Kharej server's 2053), as the kernel installer reads them
var kernelPortsRE = regexp.MustCompile(`^(\d{1,5})(?:-(\d{1,5}))?(?::\d{1,5})?$`)

// iranPorts lists the ports a kernel cable takes on its Iran server.
func iranPorts(list string) map[int]bool {
	out := map[int]bool{}
	for _, p := range splitList(list) {
		m := kernelPortsRE.FindStringSubmatch(p)
		if m == nil {
			continue
		}
		a, _ := strconv.Atoi(m[1])
		b := a
		if m[2] != "" {
			b, _ = strconv.Atoi(m[2])
		}
		for n := a; n <= b && n <= 65535; n++ {
			out[n] = true
		}
	}
	return out
}

// kernelPortClash reports a port that cable l would take on its Iran server
// while another kernel cable of the same server already forwards it: only
// one of them would ever get the traffic.
func kernelPortClash(t topology, l topoLink) error {
	if l.Kind != "kernel" {
		return nil
	}
	hub := kernelHub(t, l)
	for _, x := range kernelGroup(t, hub) {
		if x.ID == l.ID {
			continue
		}
		for _, pair := range [][2]string{{l.Ports, x.Ports}, {l.UDPPorts, x.UDPPorts}} {
			theirs := iranPorts(pair[1])
			for p := range iranPorts(pair[0]) {
				if theirs[p] {
					other, _ := nodeByID(t, otherEnd(x, hub))
					return fmt.Errorf("پورت %d روی سرور ایران از قبل به «%s» منتقل می‌شود؛ برای هر لوکیشن پورت جدا بدهید (مثلاً 37826:37825)", p, nodeName(other))
				}
			}
		}
	}
	return nil
}

// checkCable rejects settings that could not be built, before anything on a
// server is touched.
func checkCable(l topoLink) error {
	if len(splitList(l.Ports)) == 0 && len(splitList(l.UDPPorts)) == 0 {
		return errors.New("حداقل یک پورت منتقل‌شونده وارد کنید")
	}
	if l.Kind == "kernel" {
		if _, err := commonKernelEnv(kernelCreateReq{Mode: l.Mode, PSK: strings.Repeat("x", 16)}); err != nil {
			return err
		}
		for _, p := range append(splitList(l.Ports), splitList(l.UDPPorts)...) {
			if !kernelPortsRE.MatchString(p) {
				return fmt.Errorf("پورت %q برای تانل کرنلی معتبر نیست (عدد، بازه مثل 2000-3000 یا تغییر شماره مثل 37826:37825)", p)
			}
		}
		return nil
	}
	if l.Port < 0 || l.Port > 65535 || l.Carriers < 0 || l.Carriers > 16 {
		return errors.New("پورت حامل یا تعداد حامل معتبر نیست")
	}
	if l.MuxVersion < 0 || l.MuxVersion > 2 {
		return errors.New("نسخهٔ مالتی‌پلکسر باید ۱ یا ۲ باشد")
	}
	if _, ok := presets[l.Preset]; !ok {
		return fmt.Errorf("پریست %q شناخته نیست", l.Preset)
	}
	_, _, _, _, err := fleetDirectConfigs(l, "check", "192.0.2.1", "192.0.2.2")
	return err
}

// handleFleetEdit changes an applied cable - its ports, transport, direction
// or kind - and rebuilds its tunnel on both servers. A cable that keeps its
// kind keeps its tunnel name, so the new settings simply replace the old ones;
// one that changes kind has its old tunnel taken down first.
func (s *panelServer) handleFleetEdit(w http.ResponseWriter, r *http.Request) {
	var req topoLink
	if err := readJSON(r, &req); err != nil {
		jsonError(w, http.StatusBadRequest, "bad request")
		return
	}
	t := s.loadTopology()
	old, ok := linkByID(t, req.ID)
	if !ok {
		jsonError(w, http.StatusNotFound, "این کابل پیدا نشد؛ صفحه را تازه کنید")
		return
	}
	if old.Tunnel == "" {
		jsonError(w, http.StatusBadRequest, "این کابل هنوز اعمال نشده؛ آن را در طراحی شبکه تنظیم و اعمال کنید")
		return
	}
	if req.Kind != "direct" && req.Kind != "kernel" {
		jsonError(w, http.StatusBadRequest, "نوع کابل باید مستقیم، معکوس یا کرنلی باشد")
		return
	}
	next := cableSettings(old, req)
	if err := checkCable(next); err != nil {
		jsonError(w, http.StatusBadRequest, err.Error())
		return
	}
	trial := t
	trial.Links = append([]topoLink{}, t.Links...)
	for i := range trial.Links {
		if trial.Links[i].ID == next.ID {
			trial.Links[i] = next
		}
	}
	if err := checkTopology(&trial); err != nil {
		jsonError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := kernelPortClash(trial, next); err != nil {
		jsonError(w, http.StatusBadRequest, err.Error())
		return
	}
	for _, id := range []string{next.A, next.B} {
		if _, err := s.endOf(t, id); err != nil {
			jsonError(w, http.StatusBadRequest, err.Error())
			return
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	var results []stepResult
	if old.Kind != next.Kind {
		res, err := s.takeDown(ctx, t, old)
		results = append(results, res...)
		if err != nil {
			s.audit(r, "fleet-edit", old.ID+" failed: "+err.Error())
			jsonError2(w, http.StatusBadGateway, "برداشتن تانل قبلی ناموفق بود: "+err.Error(), results)
			return
		}
		next.Tunnel, next.PSK, next.Slot = "", "", 0
	}
	if err := s.updateTopology(func(t *topology) error {
		for i := range t.Links {
			if t.Links[i].ID == next.ID {
				t.Links[i] = next
			}
		}
		return nil
	}); err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	t = s.loadTopology()
	var res []stepResult
	var err error
	if next.Kind == "kernel" {
		hub := kernelHub(t, next)
		res, err = s.applyKernel(ctx, t, hub, kernelGroup(t, hub), next)
	} else {
		res, err = s.applyDirect(ctx, t, next)
	}
	results = append(results, res...)
	if err != nil {
		s.audit(r, "fleet-edit", next.ID+" failed: "+err.Error())
		jsonError2(w, http.StatusBadGateway, err.Error(), results)
		return
	}
	a, _ := nodeByID(t, next.A)
	b, _ := nodeByID(t, next.B)
	s.audit(r, "fleet-edit", next.Kind+" "+nodeName(a)+" <-> "+nodeName(b))
	s.notify("✏️ تانل بین «" + nodeName(a) + "» و «" + nodeName(b) + "» ویرایش و دوباره ساخته شد")
	jsonOK(w, map[string]any{"ok": true, "results": results})
}

// handleFleetReaddress moves a server to the new IP its provider gave it and
// rebuilds every tunnel it takes part in, so both ends point at the new
// address. Before anything changes, the server must answer at the new IP with
// the host key pinned when it was added: a mistyped IP, or another machine
// there, never takes over its tunnels. For the mother, whose address is found
// on its own, it only rebuilds the tunnels.
func (s *panelServer) handleFleetReaddress(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID      string `json:"id"`
		IP      string `json:"ip"`
		SSHPort int    `json:"ssh_port"`
	}
	if err := readJSON(r, &req); err != nil {
		jsonError(w, http.StatusBadRequest, "bad request")
		return
	}
	req.IP = strings.TrimSpace(req.IP)
	t := s.loadTopology()
	n, ok := nodeByID(t, req.ID)
	if !ok {
		jsonError(w, http.StatusNotFound, "این سرور پیدا نشد؛ صفحه را تازه کنید")
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()
	var results []stepResult
	old := n.IP
	if n.Kind != "self" {
		if n.State != "ready" {
			jsonError(w, http.StatusBadRequest, "این سرور هنوز نصب نشده است؛ آن را با IP تازه دوباره اضافه کنید")
			return
		}
		if !validHost(req.IP) {
			jsonError(w, http.StatusBadRequest, "IP سرور معتبر نیست")
			return
		}
		if req.SSHPort < 0 || req.SSHPort > 65535 {
			jsonError(w, http.StatusBadRequest, "پورت یا کاربر SSH معتبر نیست")
			return
		}
		for _, x := range t.Nodes {
			if x.ID != n.ID && x.Kind != "self" && x.IP == req.IP {
				jsonError(w, http.StatusBadRequest, fmt.Sprintf("سرور %s قبلاً با نام «%s» اضافه شده است", req.IP, nodeName(x)))
				return
			}
		}
		moved := n
		moved.IP = req.IP
		if req.SSHPort > 0 {
			moved.SSHPort = req.SSHPort
		}
		out, err := runOut(ctx, fleetExecFor(s, moved), "hostname")
		res := stepResult{Step: nodeName(n) + " (" + req.IP + ")", OK: err == nil, Output: strings.TrimSpace(out)}
		if err != nil {
			if errors.Is(err, errHostKeyChanged) {
				err = errors.New("سرور روی این IP کلید میزبان دیگری دارد؛ یا IP اشتباه است یا سرور از نو نصب شده (در این صورت آن را با رمز دوباره اضافه کنید)")
			}
			res.Error = err.Error()
			s.audit(r, "fleet-readdress", nodeName(n)+" "+old+" -> "+req.IP+" failed: "+err.Error())
			jsonError2(w, http.StatusBadGateway, "سرور روی IP تازه در دسترس نیست: "+err.Error(), []stepResult{res})
			return
		}
		results = append(results, res)
		if err := s.updateTopology(func(t *topology) error {
			for i := range t.Nodes {
				if t.Nodes[i].ID == n.ID {
					t.Nodes[i].IP, t.Nodes[i].SSHPort = moved.IP, moved.SSHPort
				}
			}
			return nil
		}); err != nil {
			jsonError(w, http.StatusInternalServerError, err.Error())
			return
		}
		t = s.loadTopology()
	}

	// every applied cable of this server, the kernel ones once per Iran hub
	var firstErr error
	hubs := map[string]bool{}
	for _, l := range t.Links {
		if l.Tunnel == "" || (l.A != n.ID && l.B != n.ID) {
			continue
		}
		var res []stepResult
		var err error
		if l.Kind == "kernel" {
			hub := kernelHub(t, l)
			if hubs[hub] {
				continue
			}
			hubs[hub] = true
			res, err = s.applyKernel(ctx, t, hub, kernelGroup(t, hub), l)
		} else {
			res, err = s.applyDirect(ctx, t, l)
		}
		results = append(results, res...)
		if err != nil && firstErr == nil {
			firstErr = err
		}
	}
	detail := nodeName(n) + " " + old + " -> " + req.IP
	if n.Kind == "self" {
		detail = nodeName(n) + " (rebuild)"
	}
	if firstErr != nil {
		s.audit(r, "fleet-readdress", detail+" failed: "+firstErr.Error())
		msg := "ساخت دوبارهٔ بعضی تانل‌ها ناموفق بود: "
		if n.Kind != "self" {
			msg = "IP ذخیره شد ولی ساخت دوبارهٔ بعضی تانل‌ها ناموفق بود: "
		}
		jsonError2(w, http.StatusBadGateway, msg+firstErr.Error(), results)
		return
	}
	s.audit(r, "fleet-readdress", detail)
	if n.Kind != "self" {
		s.notify("🔁 IP سرور «" + nodeName(n) + "» از " + old + " به " + req.IP + " تغییر کرد و تانل‌هایش دوباره ساخته شدند")
	}
	jsonOK(w, map[string]any{"ok": true, "results": results})
}

// handleFleetLinks adds several cables in one go - one Iran server to several
// Kharej servers - and returns their ids; the page then applies each one.
// With self_role "iran" the mother itself becomes that Iran server.
func (s *panelServer) handleFleetLinks(w http.ResponseWriter, r *http.Request) {
	var req struct {
		SelfRole string     `json:"self_role"`
		Links    []topoLink `json:"links"`
	}
	if err := readJSON(r, &req); err != nil {
		jsonError(w, http.StatusBadRequest, "bad request")
		return
	}
	if len(req.Links) == 0 {
		jsonError(w, http.StatusBadRequest, "حداقل یک سرور خارج انتخاب کنید")
		return
	}
	var ids []string
	err := s.updateTopology(func(t *topology) error {
		if req.SelfRole == "iran" && t.SelfRole != "iran" {
			for _, l := range t.Links {
				if l.A == "self" || l.B == "self" {
					return errors.New("نقش این سرور در طراحی شبکه «ایران» نیست و کابل دارد؛ آن را در طراحی شبکه درست کنید")
				}
			}
			t.SelfRole = "iran"
		}
		if t.SelfRole == "" {
			t.SelfRole = "none"
		}
		// the map may not hold the mother yet if the designer was never opened
		hasSelf := false
		for _, n := range t.Nodes {
			hasSelf = hasSelf || n.Kind == "self"
		}
		if !hasSelf {
			host, _ := os.Hostname()
			t.Nodes = append([]topoNode{{ID: "self", Kind: "self", Label: host}}, t.Nodes...)
		}
		for _, in := range req.Links {
			l := cableSettings(topoLink{ID: "l" + randomToken(6), A: in.A, B: in.B}, in)
			if l.Kind != "direct" && l.Kind != "kernel" {
				return errors.New("نوع کابل باید مستقیم، معکوس یا کرنلی باشد")
			}
			if err := checkCable(l); err != nil {
				return err
			}
			for _, id := range []string{l.A, l.B} {
				if _, err := s.endOf(*t, id); err != nil {
					return err
				}
			}
			t.Links = append(t.Links, l)
			ids = append(ids, l.ID)
		}
		if err := checkTopology(t); err != nil {
			return err
		}
		for _, id := range ids {
			l, _ := linkByID(*t, id)
			if err := kernelPortClash(*t, l); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		jsonError(w, http.StatusBadRequest, err.Error())
		return
	}
	s.audit(r, "fleet-links", strconv.Itoa(len(ids))+" cables")
	jsonOK(w, map[string]any{"ok": true, "ids": ids})
}

func (s *panelServer) handleFleetUnlink(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID string `json:"id"`
	}
	if err := readJSON(r, &req); err != nil {
		jsonError(w, http.StatusBadRequest, "bad request")
		return
	}
	t := s.loadTopology()
	l, ok := linkByID(t, req.ID)
	if !ok {
		jsonError(w, http.StatusNotFound, "این کابل پیدا نشد")
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	results, err := s.unlink(ctx, t, l)
	s.audit(r, "fleet-unlink", l.ID)
	if err != nil {
		jsonError2(w, http.StatusBadGateway, "کابل حذف شد ولی پاک‌سازی بعضی سرورها ناموفق بود: "+err.Error(), results)
		return
	}
	jsonOK(w, map[string]any{"ok": true, "results": results})
}

func (s *panelServer) handleFleetRemove(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID    string `json:"id"`
		Purge bool   `json:"purge"` // also remove the mother's key from the server
	}
	if err := readJSON(r, &req); err != nil {
		jsonError(w, http.StatusBadRequest, "bad request")
		return
	}
	t := s.loadTopology()
	n, ok := nodeByID(t, req.ID)
	if !ok || n.Kind == "self" {
		jsonError(w, http.StatusBadRequest, "این سرور را نمی‌توان حذف کرد")
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	var results []stepResult
	var warn []string
	for _, l := range t.Links {
		if l.A != n.ID && l.B != n.ID {
			continue
		}
		res, err := s.unlink(ctx, s.loadTopology(), l)
		results = append(results, res...)
		if err != nil {
			warn = append(warn, err.Error())
		}
	}
	if req.Purge && n.State == "ready" {
		if line, err := s.fleetAuthorizedLine(); err == nil {
			body := strings.Fields(line)[1]
			_, err := runOut(ctx, fleetExecFor(s, n), `f=~/.ssh/authorized_keys; [ -f "$f" ] && grep -vF `+shQuote(body)+` "$f" > "$f.tmp"; cat "$f.tmp" > "$f"; rm -f "$f.tmp"`)
			if err != nil {
				warn = append(warn, "کلید سرور مادر از سرور پاک نشد: "+err.Error())
			}
		}
	}
	err := s.updateTopology(func(t *topology) error {
		out := t.Nodes[:0]
		for _, x := range t.Nodes {
			if x.ID != n.ID {
				out = append(out, x)
			}
		}
		t.Nodes = out
		links := t.Links[:0]
		for _, l := range t.Links {
			if l.A != n.ID && l.B != n.ID {
				links = append(links, l)
			}
		}
		t.Links = links
		return nil
	})
	if err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.fleetMu.Lock()
	delete(s.fleetJobs, n.ID)
	delete(s.fleetStats, n.ID)
	delete(s.fleetPrev, n.ID)
	s.fleetMu.Unlock()
	s.benchMu.Lock()
	all := s.loadBench()
	if _, ok := all[n.ID]; ok {
		delete(all, n.ID)
		if b, err := json.MarshalIndent(all, "", "  "); err == nil {
			_ = writeFileAtomic(s.benchPath(), b, 0o600)
		}
	}
	s.benchMu.Unlock()
	s.audit(r, "fleet-remove", nodeName(n)+" "+n.IP)
	sort.Strings(warn)
	jsonOK(w, map[string]any{"ok": true, "results": results, "warnings": warn})
}

// lentBlob is a payload a server fetches from the panel: on routes that hold
// SSH back, the binary or the kernel installer cannot be sent over it, while
// the server can download them (see sshjob.go).
type lentBlob struct {
	data    []byte
	expires time.Time
}

// lendBlob makes data downloadable once, for a few minutes, at a URL with an
// unguessable token; withdraw ends the loan.
func (s *panelServer) lendBlob(data []byte) (string, func()) {
	cfg := s.config()
	_, port, err := net.SplitHostPort(cfg.Listen)
	host := publicHost(&cfg)
	if err != nil || port == "" || host == "" {
		return "", func() {}
	}
	tok := randomToken(16)
	s.fleetMu.Lock()
	now := time.Now()
	for k, b := range s.fleetBlobs {
		if now.After(b.expires) {
			delete(s.fleetBlobs, k)
		}
	}
	s.fleetBlobs[tok] = lentBlob{data: data, expires: now.Add(10 * time.Minute)}
	s.fleetMu.Unlock()
	url := fmt.Sprintf("https://%s%s/blob/%s", net.JoinHostPort(host, port), cfg.BasePath, tok)
	return url, func() {
		s.fleetMu.Lock()
		delete(s.fleetBlobs, tok)
		s.fleetMu.Unlock()
	}
}

func (s *panelServer) handleBlob(w http.ResponseWriter, r *http.Request) {
	s.fleetMu.Lock()
	b, ok := s.fleetBlobs[r.PathValue("token")]
	s.fleetMu.Unlock()
	if !ok || time.Now().After(b.expires) {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", strconv.Itoa(len(b.data)))
	_, _ = w.Write(b.data)
}

// fleetState is the fleet's part of panelServer.
type fleetState struct {
	fleetMu    sync.Mutex
	fleetKey   ssh.Signer
	fleetJobs  map[string]*fleetJob
	fleetBlobs map[string]lentBlob
	fleetStats map[string]nodeStats
	fleetPrev  map[string]rawSample
	fleetOnce  sync.Once
	fleetSeen  atomic.Int64
	selfFacts  atomic.Pointer[nodeFacts]
	topoMu     sync.Mutex
}

func (f *fleetState) initFleet() {
	f.fleetJobs = map[string]*fleetJob{}
	f.fleetBlobs = map[string]lentBlob{}
	f.fleetStats = map[string]nodeStats{}
	f.fleetPrev = map[string]rawSample{}
}
