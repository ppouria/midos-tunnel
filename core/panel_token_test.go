package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func tokenRequest(p *testPanel, method, path, token string, header bool, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, "/s3cretpath"+path, strings.NewReader(body))
	req.RemoteAddr = "198.51.100.9:5000"
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if header {
		req.Header.Set("X-Midos", "1")
	}
	w := httptest.NewRecorder()
	p.h.ServeHTTP(w, req)
	return w
}

func setToken(p *testPanel, token string) {
	sum := sha256.Sum256([]byte(token))
	p.s.mu.Lock()
	p.s.cfg.APITokenHash = hex.EncodeToString(sum[:])
	p.s.mu.Unlock()
}

func TestAPITokenAuthenticatesWithoutSession(t *testing.T) {
	p := newTestPanel(t)
	if w := tokenRequest(p, "GET", "/api/audit", "anything", false, ""); w.Code != http.StatusUnauthorized {
		t.Fatalf("no token configured must not authenticate, got %d", w.Code)
	}
	setToken(p, "mdt_secret")
	if w := tokenRequest(p, "GET", "/api/audit", "mdt_secret", false, ""); w.Code != http.StatusOK {
		t.Fatalf("valid token must be accepted, got %d", w.Code)
	}
	if w := tokenRequest(p, "GET", "/api/audit", "mdt_wrong", false, ""); w.Code != http.StatusUnauthorized {
		t.Fatalf("wrong token must be rejected, got %d", w.Code)
	}
	if w := tokenRequest(p, "GET", "/api/audit", "", false, ""); w.Code != http.StatusUnauthorized {
		t.Fatalf("no credentials must be rejected, got %d", w.Code)
	}
}

func TestAPITokenChangeNeedsHeaderAndIsAudited(t *testing.T) {
	p := newTestPanel(t)
	setToken(p, "mdt_secret")
	body := `{"name":"a1","config":{"mode":"client","psk":"` + testPSK + `","endpoints":[{"transport":"tcp","server":"1.2.3.4:443"}],"ports":["34567"]}}`
	if w := tokenRequest(p, "POST", "/api/tunnels", "mdt_secret", false, body); w.Code != http.StatusForbidden {
		t.Fatalf("a change without the custom header must be refused, got %d", w.Code)
	}
	if w := tokenRequest(p, "POST", "/api/tunnels", "mdt_secret", true, body); w.Code != http.StatusOK {
		t.Fatalf("a change with token and header must work, got %d: %s", w.Code, w.Body.String())
	}
	log, err := os.ReadFile(p.s.auditPath())
	if err != nil || !bytes.Contains(log, []byte(`"api-token"`)) {
		t.Fatalf("a change made with the token must be audited: %v %s", err, log)
	}
}

func TestPanelTokenCommandStoresOnlyAHash(t *testing.T) {
	p := newTestPanel(t)
	if err := panelAPIToken(p.s.cfgPath, false); err != nil {
		t.Fatal(err)
	}
	cfg, err := loadPanelConfig(p.s.cfgPath)
	if err != nil || len(cfg.APITokenHash) != 64 {
		t.Fatalf("token hash not stored: %v %q", err, cfg.APITokenHash)
	}
	raw, _ := os.ReadFile(p.s.cfgPath)
	if bytes.Contains(raw, []byte("mdt_")) {
		t.Fatal("the token itself must never be written to the config")
	}
	if err := panelAPIToken(p.s.cfgPath, true); err != nil {
		t.Fatal(err)
	}
	cfg, _ = loadPanelConfig(p.s.cfgPath)
	if cfg.APITokenHash != "" {
		t.Fatal("revoke must clear the token")
	}
}
