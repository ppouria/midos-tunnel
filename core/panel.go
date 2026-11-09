package main

// The web panel: a single page, served by this same binary, for watching and
// managing the tunnels on this server.
//
// It answers only under a random secret path - anything else gets a plain 404,
// so a scanner learns nothing - and only over HTTPS, behind a password, a
// and a captcha. Every change it makes goes through
// systemd exactly as an operator at the shell would make it, so the panel can
// be stopped or removed without touching a single running tunnel.

import (
	"context"
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"flag"
	"fmt"
	"log"
	"math/big"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"
)

const (
	defaultPanelConfig = "/etc/Midos-tunnel/panel.json"
	defaultTunnelsDir  = "/etc/Midos-tunnel/tunnels"
	defaultUnitDir     = "/etc/systemd/system"
	installedBinary    = "/usr/local/sbin/midos-tunnel"

	sessionTTL   = 12 * time.Hour
	maxLoginFail = 5
	loginLockout = 15 * time.Minute
)

// PanelConfig is the panel's own settings file.
type PanelConfig struct {
	Listen       string `json:"listen"`
	BasePath     string `json:"base_path"`
	Username     string `json:"username"`
	PasswordHash string `json:"password_hash"`
	APITokenHash string `json:"api_token_hash,omitempty"`
	CertPEM      string `json:"cert_pem"`
	KeyPEM       string `json:"key_pem"`
	TunnelsDir   string `json:"tunnels_dir"`
	// the address peers use to reach this server; detected when empty
	PublicHost string `json:"public_host,omitempty"`
	// Telegram alerts, off until a bot token and chat id are set
	TelegramToken string `json:"telegram_token,omitempty"`
	TelegramChat  string `json:"telegram_chat,omitempty"`
	// Automatic TLS via Let's Encrypt. Off until a domain is set. Method is
	// "http-01" (validated on port 80) or "tls-alpn-01" (validated on port
	// 443, so the panel must then listen on 443).
	ACMEDomain string `json:"acme_domain,omitempty"`
	ACMEMethod string `json:"acme_method,omitempty"`
	ACMEEmail  string `json:"acme_email,omitempty"`
	// Monitoring: a webhook URL that receives the same alerts as Telegram
	// (a JSON {"text":...} POST, which Slack/Discord-style hooks accept), and
	// a watchdog that restarts a managed tunnel whose unit has failed.
	AlertWebhook string `json:"alert_webhook,omitempty"`
	Watchdog     bool   `json:"watchdog,omitempty"`
}

func loadPanelConfig(path string) (*PanelConfig, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var c PanelConfig
	if err := json.Unmarshal(b, &c); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if c.Listen == "" || c.Username == "" || c.PasswordHash == "" {
		return nil, errors.New("panel config needs listen, username and password_hash")
	}
	if !strings.HasPrefix(c.BasePath, "/") || len(c.BasePath) < 8 || strings.HasSuffix(c.BasePath, "/") {
		return nil, errors.New("base_path must look like /secret (at least 7 characters, no trailing slash)")
	}
	if c.TunnelsDir == "" {
		c.TunnelsDir = defaultTunnelsDir
	}
	return &c, nil
}

func savePanelConfig(path string, c *PanelConfig) error {
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(path, append(b, '\n'), 0o600)
}

// writeFileAtomic replaces a file in one step, so a crash halfway leaves the
// old version rather than half of the new one.
func writeFileAtomic(path string, data []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, mode); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func randomToken(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

// randomWord returns n characters from an alphabet without look-alikes, for
// secrets a person has to type.
func randomWord(n int) string {
	const alphabet = "abcdefghjkmnpqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789"
	b := make([]byte, n)
	for i := range b {
		v, err := rand.Int(rand.Reader, big.NewInt(int64(len(alphabet))))
		if err != nil {
			panic(err)
		}
		b[i] = alphabet[v.Int64()]
	}
	return string(b)
}

// ---------------------------------------------------------------------------
// command line
// ---------------------------------------------------------------------------

func panelMain(args []string) {
	log.SetFlags(log.LstdFlags | log.Lmsgprefix)
	log.SetPrefix("panel: ")
	sub := "run"
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		sub, args = args[0], args[1:]
	}
	fs := flag.NewFlagSet("panel "+sub, flag.ExitOnError)
	cfgPath := fs.String("config", defaultPanelConfig, "panel configuration file")
	port := fs.Int("port", 0, "init: port to listen on (random when 0)")
	user := fs.String("user", "admin", "init: administrator user name")
	force := fs.Bool("force", false, "init: overwrite an existing configuration")
	noService := fs.Bool("no-service", false, "init: do not install the systemd unit")
	revoke := fs.Bool("revoke", false, "token: remove the API token instead of issuing one")
	_ = fs.Parse(args)

	var err error
	switch sub {
	case "run":
		err = runPanel(*cfgPath)
	case "init":
		err = panelInit(*cfgPath, *port, *user, *force, !*noService)
	case "passwd":
		err = panelResetPassword(*cfgPath)
	case "url":
		err = panelPrintURL(*cfgPath)
	case "token":
		err = panelAPIToken(*cfgPath, *revoke)
	default:
		err = fmt.Errorf("unknown command %q (run, init, passwd, url, token)", sub)
	}
	if err != nil {
		log.Fatal(err)
	}
}

func panelInit(path string, port int, user string, force, service bool) error {
	if _, err := os.Stat(path); err == nil && !force {
		return fmt.Errorf("%s already exists (use -force to replace it, or \"panel url\" to see the address)", path)
	}
	if port == 0 {
		p, err := rand.Int(rand.Reader, big.NewInt(40000))
		if err != nil {
			return err
		}
		port = 20000 + int(p.Int64())
	}
	pass := randomWord(16)
	hash, err := bcrypt.GenerateFromPassword([]byte(pass), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	c := &PanelConfig{
		Listen:       fmt.Sprintf("0.0.0.0:%d", port),
		BasePath:     "/" + randomWord(12),
		Username:     user,
		PasswordHash: string(hash),
		CertPEM:      filepath.Join(dir, "panel.crt"),
		KeyPEM:       filepath.Join(dir, "panel.key"),
		TunnelsDir:   filepath.Join(dir, "tunnels"),
	}
	if err := writeSelfSignedCert(c.CertPEM, c.KeyPEM); err != nil {
		return err
	}
	if err := os.MkdirAll(c.TunnelsDir, 0o700); err != nil {
		return err
	}
	if err := savePanelConfig(path, c); err != nil {
		return err
	}
	if service {
		if err := installPanelUnits(path); err != nil {
			return err
		}
	}
	fmt.Printf("\n  Panel address : %s\n  User name     : %s\n  Password      : %s\n\n", panelURL(c), user, pass)
	fmt.Printf("  The certificate is self-signed: the browser will warn once; that is expected.\n")
	fmt.Printf("  Forgot the password?  midos-tunnel panel passwd\n\n")
	return nil
}

func panelResetPassword(path string) error {
	c, err := loadPanelConfig(path)
	if err != nil {
		return err
	}
	pass := randomWord(16)
	hash, err := bcrypt.GenerateFromPassword([]byte(pass), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	c.PasswordHash = string(hash)
	if err := savePanelConfig(path, c); err != nil {
		return err
	}
	fmt.Printf("  New password for %s: %s\n  (restart the panel: systemctl restart midos-tunnel-panel)\n", c.Username, pass)
	return nil
}

func panelAPIToken(path string, revoke bool) error {
	c, err := loadPanelConfig(path)
	if err != nil {
		return err
	}
	if revoke {
		c.APITokenHash = ""
		if err := savePanelConfig(path, c); err != nil {
			return err
		}
		fmt.Printf("  API token removed.\n  (restart the panel: systemctl restart midos-tunnel-panel)\n")
		return nil
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return err
	}
	token := "mdt_" + hex.EncodeToString(raw)
	sum := sha256.Sum256([]byte(token))
	c.APITokenHash = hex.EncodeToString(sum[:])
	if err := savePanelConfig(path, c); err != nil {
		return err
	}
	fmt.Printf("\n  API token: %s\n\n  Send it as \"Authorization: Bearer <token>\" (and \"X-Midos: 1\" on changes).\n", token)
	fmt.Printf("  It is stored only as a hash and replaces any earlier token.\n  (restart the panel: systemctl restart midos-tunnel-panel)\n\n")
	return nil
}

func panelPrintURL(path string) error {
	c, err := loadPanelConfig(path)
	if err != nil {
		return err
	}
	fmt.Println(panelURL(c))
	return nil
}

func panelURL(c *PanelConfig) string {
	_, port, _ := net.SplitHostPort(c.Listen)
	return fmt.Sprintf("https://%s/%s/", net.JoinHostPort(publicHost(c), port), strings.TrimPrefix(c.BasePath, "/"))
}

// publicHost is the address other machines reach this one at: the configured
// one, or the source address of the default route.
func publicHost(c *PanelConfig) string {
	if c.PublicHost != "" {
		return c.PublicHost
	}
	conn, err := net.Dial("udp", "1.1.1.1:53") // no packet is sent: this only picks a route
	if err != nil {
		return "127.0.0.1"
	}
	defer conn.Close()
	return conn.LocalAddr().(*net.UDPAddr).IP.String()
}

func writeSelfSignedCert(certPath, keyPath string) error {
	cert, err := selfSigned("localhost")
	if err != nil {
		return err
	}
	key, ok := cert.PrivateKey.(*ecdsa.PrivateKey)
	if !ok {
		return errors.New("unexpected key type")
	}
	der, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return err
	}
	if err := writeFileAtomic(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Certificate[0]}), 0o644); err != nil {
		return err
	}
	return writeFileAtomic(keyPath, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der}), 0o600)
}

const tunnelUnitTemplate = `[Unit]
Description=Midos Tunnel relay %i
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=/usr/local/sbin/midos-tunnel -config /etc/Midos-tunnel/tunnels/%i.json
Restart=always
RestartSec=5
LimitNOFILE=1048576
NoNewPrivileges=yes
ProtectSystem=strict
ReadWritePaths=/etc/Midos-tunnel
ProtectHome=yes
PrivateTmp=yes

[Install]
WantedBy=multi-user.target
`

func panelUnitText(bin, cfgPath string) string {
	return fmt.Sprintf(`[Unit]
Description=Midos Tunnel web panel
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=%s panel -config %s
Restart=always
RestartSec=5

[Install]
WantedBy=multi-user.target
`, bin, cfgPath)
}

func installPanelUnits(cfgPath string) error {
	if err := writeFileAtomic(filepath.Join(defaultUnitDir, panelUnitName), []byte(panelUnitText(installedBinary, cfgPath)), 0o644); err != nil {
		return err
	}
	if err := writeFileAtomic(filepath.Join(defaultUnitDir, tunnelUnitName), []byte(tunnelUnitTemplate), 0o644); err != nil {
		return err
	}
	for _, args := range [][]string{{"daemon-reload"}, {"enable", "--now", panelUnitName}} {
		if out, err := exec.Command("systemctl", args...).CombinedOutput(); err != nil {
			return fmt.Errorf("systemctl %s: %v: %s", strings.Join(args, " "), err, out)
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// server
// ---------------------------------------------------------------------------

// runner executes a system command; tests replace it.
type runner func(ctx context.Context, name string, args ...string) ([]byte, error)

func execRunner(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).CombinedOutput()
}

type loginFail struct {
	count int
	until time.Time
}

type panelServer struct {
	cfgPath string
	run     runner
	unitDir string
	sys     *sysSampler

	mu       sync.Mutex
	cfg      *PanelConfig
	sessions map[string]time.Time
	fails    map[string]*loginFail
	captchas map[string]captchaEntry
	peers    map[string]peerToken
	auditMu  sync.Mutex
	usage    *usageTracker
	benchMu  sync.Mutex
	benches  map[string]*benchJob // node id -> running benchmark
	fleetState
}

func newPanelServer(cfgPath string, cfg *PanelConfig, run runner) *panelServer {
	s := &panelServer{
		cfgPath: cfgPath, cfg: cfg, run: run, unitDir: defaultUnitDir,
		sessions: map[string]time.Time{}, fails: map[string]*loginFail{},
		captchas: map[string]captchaEntry{},
		peers:    map[string]peerToken{},
	}
	s.initFleet()
	return s
}

func (s *panelServer) config() PanelConfig {
	s.mu.Lock()
	defer s.mu.Unlock()
	return *s.cfg
}

func runPanel(path string) error {
	cfg, err := loadPanelConfig(path)
	if err != nil {
		return err
	}
	tlsConf, startACME, err := panelTLS(cfg)
	if err != nil {
		return err
	}
	s := newPanelServer(path, cfg, execRunner)
	jobLend = s.lendBlob
	if m := newMigration(execRunner, s.unitDir, cfg.TunnelsDir, true); m.needed() {
		go func() {
			err := m.runAll()
			for _, l := range m.log {
				log.Printf("migrate: %s", l)
			}
			if err != nil {
				log.Printf("migrate: %v", err)
			}
		}()
	}
	s.sys = newSysSampler()
	go s.sys.loop()
	go s.tunnelWatcher()
	s.endStaleInstalls()
	s.usage = newUsageTracker(filepath.Join(filepath.Dir(path), "usage.json"))
	go s.usageLoop()
	s.audit(nil, "panel-start", version)
	s.notify("▶️ پنل Midos Tunnel روی " + publicHost(cfg) + " شروع شد")
	if startACME != nil {
		go startACME()
	}
	srv := &http.Server{
		Addr:              cfg.Listen,
		Handler:           s.routes(),
		TLSConfig:         tlsConf,
		ReadHeaderTimeout: 10 * time.Second,
		// a backup restore is the largest request; a minute is generous for it
		ReadTimeout:  60 * time.Second,
		WriteTimeout: 120 * time.Second,
		ErrorLog:     log.New(discardWriter{}, "", 0), // TLS noise from scanners
	}
	log.Printf("panel on https://%s%s/", cfg.Listen, cfg.BasePath)
	return srv.ListenAndServeTLS("", "")
}

type discardWriter struct{}

func (discardWriter) Write(p []byte) (int, error) { return len(p), nil }

func (s *panelServer) routes() http.Handler {
	b := s.config().BasePath
	mux := http.NewServeMux()
	mux.Handle("GET "+b+"/", http.StripPrefix(b, panelStatic()))
	mux.HandleFunc("GET "+b, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, b+"/", http.StatusFound)
	})

	mux.HandleFunc("GET "+b+"/api/captcha", s.handleCaptcha)
	mux.HandleFunc("POST "+b+"/api/login", s.mutating(s.handleLogin))
	mux.HandleFunc("POST "+b+"/api/logout", s.authed(s.handleLogout))
	mux.HandleFunc("GET "+b+"/api/session", s.authed(s.handleSession))
	mux.HandleFunc("GET "+b+"/api/system", s.authed(s.handleSystem))

	mux.HandleFunc("GET "+b+"/api/tunnels", s.authed(s.handleListTunnels))
	mux.HandleFunc("POST "+b+"/api/tunnels", s.authed(s.handleCreateTunnel))
	mux.HandleFunc("GET "+b+"/api/tunnels/{id}", s.authed(s.handleGetTunnel))
	mux.HandleFunc("PUT "+b+"/api/tunnels/{id}", s.authed(s.handleUpdateTunnel))
	mux.HandleFunc("DELETE "+b+"/api/tunnels/{id}", s.authed(s.handleDeleteTunnel))
	mux.HandleFunc("POST "+b+"/api/tunnels/{id}/action", s.authed(s.handleTunnelAction))
	mux.HandleFunc("GET "+b+"/api/tunnels/{id}/logs", s.authed(s.handleTunnelLogs))
	mux.HandleFunc("POST "+b+"/api/tunnels/{id}/test", s.authed(s.handleTunnelTest))
	mux.HandleFunc("POST "+b+"/api/tunnels/{id}/stats", s.authed(s.handleEnableStats))
	mux.HandleFunc("POST "+b+"/api/tunnels/{id}/peer", s.authed(s.handlePeerCommand))

	mux.HandleFunc("GET "+b+"/peer/{token}", s.handlePeerScript)
	mux.HandleFunc("GET "+b+"/peer/{token}/config", s.handlePeerConfig)
	mux.HandleFunc("GET "+b+"/peer/{token}/bin", s.handlePeerBinary)
	mux.HandleFunc("GET "+b+"/blob/{token}", s.handleBlob)

	mux.HandleFunc("GET "+b+"/api/kernel", s.authed(s.handleKernel))
	mux.HandleFunc("POST "+b+"/api/kernel/action", s.authed(s.handleKernelAction))
	mux.HandleFunc("POST "+b+"/api/kernel/create", s.authed(s.handleKernelCreate))
	mux.HandleFunc("POST "+b+"/api/kernel/teardown", s.authed(s.handleKernelTeardown))
	mux.HandleFunc("GET "+b+"/api/kernel/logs", s.authed(s.handleKernelLogs))
	mux.HandleFunc("POST "+b+"/api/kernel/ping", s.authed(s.handleKernelPing))

	mux.HandleFunc("GET "+b+"/api/backup", s.authed(s.handleBackup))
	mux.HandleFunc("POST "+b+"/api/restore", s.authed(s.handleRestore))
	mux.HandleFunc("POST "+b+"/api/settings/password", s.authed(s.handleChangePassword))
	mux.HandleFunc("POST "+b+"/api/settings/cert", s.authed(s.handleUploadCert))
	mux.HandleFunc("POST "+b+"/api/settings/tls", s.authed(s.handleTLSSettings))
	mux.HandleFunc("POST "+b+"/api/settings/telegram", s.authed(s.handleTelegramSettings))
	mux.HandleFunc("POST "+b+"/api/settings/telegram/test", s.authed(s.handleTelegramTest))
	mux.HandleFunc("POST "+b+"/api/settings/alerts", s.authed(s.handleAlertSettings))
	mux.HandleFunc("POST "+b+"/api/benchmark", s.authed(s.handleBenchmark))
	mux.HandleFunc("GET "+b+"/api/usage", s.authed(s.handleUsage))
	mux.HandleFunc("POST "+b+"/api/usage/limit", s.authed(s.handleUsageLimit))
	mux.HandleFunc("POST "+b+"/api/usage/reset", s.authed(s.handleUsageReset))
	mux.HandleFunc("GET "+b+"/api/doctor", s.authed(s.handleDoctor))
	mux.HandleFunc("POST "+b+"/api/doctor/repair", s.authed(s.handleDoctorRepair))
	mux.HandleFunc("GET "+b+"/api/topology", s.authed(s.handleTopologyGet))
	mux.HandleFunc("PUT "+b+"/api/topology", s.authed(s.handleTopologyPut))
	mux.HandleFunc("POST "+b+"/api/topology/link", s.authed(s.handleTopologyLink))
	mux.HandleFunc("GET "+b+"/api/topology/bench", s.authed(s.handleBenchList))
	mux.HandleFunc("POST "+b+"/api/topology/bench", s.authed(s.handleBenchStart))
	mux.HandleFunc("GET "+b+"/api/fleet", s.authed(s.handleFleet))
	mux.HandleFunc("POST "+b+"/api/fleet/add", s.authed(s.handleFleetAdd))
	mux.HandleFunc("POST "+b+"/api/fleet/apply", s.authed(s.handleFleetApply))
	mux.HandleFunc("POST "+b+"/api/fleet/unlink", s.authed(s.handleFleetUnlink))
	mux.HandleFunc("POST "+b+"/api/fleet/edit", s.authed(s.handleFleetEdit))
	mux.HandleFunc("POST "+b+"/api/fleet/links", s.authed(s.handleFleetLinks))
	mux.HandleFunc("POST "+b+"/api/fleet/readdress", s.authed(s.handleFleetReaddress))
	mux.HandleFunc("POST "+b+"/api/fleet/remove", s.authed(s.handleFleetRemove))
	mux.HandleFunc("GET "+b+"/api/audit", s.authed(s.handleAudit))
	mux.HandleFunc("GET "+b+"/api/update", s.authed(s.handleUpdateCheck))
	mux.HandleFunc("POST "+b+"/api/update", s.authed(s.handleUpdateApply))
	mux.HandleFunc("POST "+b+"/api/update/rollback", s.authed(s.handleRollback))

	// the sponsor banner's slides; public so it also shows on the login screen
	mux.HandleFunc("GET "+b+"/api/promo", s.handlePromo)
	mux.HandleFunc("GET "+b+"/api/promo/image/{id}", s.handlePromoImage)

	return securityHeaders(mux)
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data:; style-src 'self'; script-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Strict-Transport-Security", "max-age=31536000")
		if strings.Contains(r.URL.Path, "/api/") {
			h.Set("Cache-Control", "no-store")
		}
		next.ServeHTTP(w, r)
	})
}

// ---------------------------------------------------------------------------
// sessions
// ---------------------------------------------------------------------------

const sessionCookie = "mt_session"

func (s *panelServer) sessionID(r *http.Request) string {
	c, err := r.Cookie(sessionCookie)
	if err != nil || len(c.Value) != 64 {
		return ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	exp, ok := s.sessions[c.Value]
	if !ok || time.Now().After(exp) {
		delete(s.sessions, c.Value)
		return ""
	}
	return c.Value
}

// mutating rejects a state-changing request that a browser could have sent
// from another site. A custom header cannot be added cross-site without a CORS
// preflight this server never answers, so its presence proves the request came
// from the panel's own script; the SameSite=Strict cookie is the second lock.
func (s *panelServer) mutating(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Header.Get("X-Midos") != "1" {
			jsonError(w, http.StatusForbidden, "missing request header")
			return
		}
		next(w, r)
	}
}

func (s *panelServer) authed(next http.HandlerFunc) http.HandlerFunc {
	return s.mutating(func(w http.ResponseWriter, r *http.Request) {
		if s.sessionID(r) == "" {
			if !s.tokenOK(r) {
				jsonError(w, http.StatusUnauthorized, "login required")
				return
			}
			if r.Method != http.MethodGet {
				s.audit(r, "api-token", r.Method+" "+r.URL.Path)
			}
		}
		next(w, r)
	})
}

func (s *panelServer) tokenOK(r *http.Request) bool {
	token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok || token == "" {
		return false
	}
	s.mu.Lock()
	want := s.cfg.APITokenHash
	s.mu.Unlock()
	if want == "" {
		return false
	}
	sum := sha256.Sum256([]byte(token))
	return subtle.ConstantTimeCompare([]byte(hex.EncodeToString(sum[:])), []byte(want)) == 1
}

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func jsonOK(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func jsonError(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

func readJSON(r *http.Request, v any) error {
	dec := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 1<<20))
	dec.DisallowUnknownFields()
	return dec.Decode(v)
}

func (s *panelServer) handleLogin(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Username  string `json:"username"`
		Password  string `json:"password"`
		CaptchaID string `json:"captcha_id"`
		Captcha   string `json:"captcha"`
	}
	if err := readJSON(r, &req); err != nil {
		jsonError(w, http.StatusBadRequest, "bad request")
		return
	}
	ip := clientIP(r)
	s.mu.Lock()
	f := s.fails[ip]
	locked := f != nil && time.Now().Before(f.until)
	s.mu.Unlock()
	if locked {
		jsonError(w, http.StatusTooManyRequests, "too many failed attempts, try again later")
		return
	}
	if !s.checkCaptcha(req.CaptchaID, req.Captcha) {
		jsonError(w, http.StatusBadRequest, "captcha")
		return
	}

	cfg := s.config()
	ok := subtle.ConstantTimeCompare([]byte(req.Username), []byte(cfg.Username)) == 1
	// bcrypt runs even for a wrong user name, so the timing does not tell
	// which of the two was wrong
	if bcrypt.CompareHashAndPassword([]byte(cfg.PasswordHash), []byte(req.Password)) != nil {
		ok = false
	}
	if !ok {
		s.mu.Lock()
		if f == nil {
			f = &loginFail{}
			s.fails[ip] = f
		}
		f.count++
		if f.count >= maxLoginFail {
			f.count, f.until = 0, time.Now().Add(loginLockout)
			log.Printf("login locked for %s after %d failures", ip, maxLoginFail)
		}
		s.mu.Unlock()
		s.audit(r, "login-failed", "")
		jsonError(w, http.StatusUnauthorized, "wrong credentials")
		return
	}

	id := randomToken(32)
	s.mu.Lock()
	delete(s.fails, ip)
	now := time.Now()
	for k, exp := range s.sessions {
		if now.After(exp) {
			delete(s.sessions, k)
		}
	}
	s.sessions[id] = now.Add(sessionTTL)
	s.mu.Unlock()
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: id, Path: cfg.BasePath + "/",
		HttpOnly: true, Secure: true, SameSite: http.SameSiteStrictMode, MaxAge: int(sessionTTL.Seconds())})
	log.Printf("login from %s", ip)
	s.audit(r, "login", "")
	jsonOK(w, map[string]bool{"ok": true})
}

func (s *panelServer) handleLogout(w http.ResponseWriter, r *http.Request) {
	id := s.sessionID(r)
	s.mu.Lock()
	delete(s.sessions, id)
	s.mu.Unlock()
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "", Path: s.config().BasePath + "/", MaxAge: -1,
		HttpOnly: true, Secure: true, SameSite: http.SameSiteStrictMode})
	jsonOK(w, map[string]bool{"ok": true})
}

func (s *panelServer) handleSession(w http.ResponseWriter, r *http.Request) {
	cfg := s.config()
	host, _ := os.Hostname()
	jsonOK(w, map[string]any{
		"user": cfg.Username, "version": version,
		"hostname": host, "public_host": publicHost(&cfg),
		"telegram": cfg.TelegramToken != "", "rollback": s.rollbackAvailable(),
		"acme_domain": cfg.ACMEDomain, "acme_method": cfg.ACMEMethod, "acme_email": cfg.ACMEEmail,
		"webhook": cfg.AlertWebhook != "", "watchdog": cfg.Watchdog,
	})
}

func (s *panelServer) handleChangePassword(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Current string `json:"current"`
		New     string `json:"new"`
	}
	if err := readJSON(r, &req); err != nil {
		jsonError(w, http.StatusBadRequest, "bad request")
		return
	}
	if len(req.New) < 10 {
		jsonError(w, http.StatusBadRequest, "the new password must be at least 10 characters")
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if bcrypt.CompareHashAndPassword([]byte(s.cfg.PasswordHash), []byte(req.Current)) != nil {
		jsonError(w, http.StatusForbidden, "the current password is wrong")
		return
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(req.New), bcrypt.DefaultCost)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	next := *s.cfg
	next.PasswordHash = string(hash)
	if err := savePanelConfig(s.cfgPath, &next); err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.cfg = &next
	s.audit(r, "password", "changed")
	s.notify("🔐 رمز پنل Midos Tunnel عوض شد")
	// every other session ends: whoever knew the old password is logged out
	current := s.sessionIDLocked(r)
	for k := range s.sessions {
		if k != current {
			delete(s.sessions, k)
		}
	}
	jsonOK(w, map[string]bool{"ok": true})
}

func (s *panelServer) sessionIDLocked(r *http.Request) string {
	if c, err := r.Cookie(sessionCookie); err == nil {
		return c.Value
	}
	return ""
}
