// Package auth implements signed session cookies, CSRF tokens, and flash
// messages, replacing Flask-Login + Flask-WTF's CSRFProtect + flask.flash.
package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const (
	SessionCookieName = "panel_session"
	FlashCookieName   = "panel_flash"
	CSRFSeedCookie    = "panel_csrf_seed"
	sessionTTL        = 7 * 24 * time.Hour
	csrfSeedTTL       = 365 * 24 * time.Hour
)

type Manager struct {
	secret   []byte
	forceSSL bool
}

func NewManager(secret []byte, forceSSL bool) *Manager {
	return &Manager{secret: secret, forceSSL: forceSSL}
}

type sessionPayload struct {
	UserID int64 `json:"uid"`
	Exp    int64 `json:"exp"`
}

func (m *Manager) sign(data []byte) string {
	mac := hmac.New(sha256.New, m.secret)
	mac.Write(data)
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// SetSession writes a signed session cookie for the given user id.
func (m *Manager) SetSession(w http.ResponseWriter, userID int64) {
	payload := sessionPayload{UserID: userID, Exp: time.Now().Add(sessionTTL).Unix()}
	body, _ := json.Marshal(payload)
	encoded := base64.RawURLEncoding.EncodeToString(body)
	sig := m.sign([]byte(encoded))
	value := encoded + "." + sig

	http.SetCookie(w, &http.Cookie{
		Name:     SessionCookieName,
		Value:    value,
		Path:     "/",
		HttpOnly: true,
		Secure:   m.forceSSL,
		SameSite: http.SameSiteLaxMode,
		Expires:  time.Now().Add(sessionTTL),
	})
}

func (m *Manager) ClearSession(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     SessionCookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		Secure:   m.forceSSL,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
	})
}

// CurrentUserID returns the authenticated user id from the request's session
// cookie, or 0 if there is none / it's invalid / expired.
func (m *Manager) CurrentUserID(r *http.Request) int64 {
	c, err := r.Cookie(SessionCookieName)
	if err != nil || c.Value == "" {
		return 0
	}
	parts := strings.SplitN(c.Value, ".", 2)
	if len(parts) != 2 {
		return 0
	}
	encoded, sig := parts[0], parts[1]
	expected := m.sign([]byte(encoded))
	if subtle.ConstantTimeCompare([]byte(sig), []byte(expected)) != 1 {
		return 0
	}
	body, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return 0
	}
	var payload sessionPayload
	if err := json.Unmarshal(body, &payload); err != nil {
		return 0
	}
	if time.Now().Unix() > payload.Exp {
		return 0
	}
	return payload.UserID
}

// CSRF tokens are derived from a dedicated random seed cookie that exists
// independent of login state (mirrors Flask's session cookie existing as
// soon as csrf_token() is first called in a template, before any login).

func (m *Manager) tokenForSeed(seed string) string {
	mac := hmac.New(sha256.New, m.secret)
	mac.Write([]byte("csrf:" + seed))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// EnsureCSRFToken returns this browser's CSRF token, creating and setting a
// seed cookie on w if one doesn't exist yet on r. Call this whenever
// rendering a page that contains a form.
func (m *Manager) EnsureCSRFToken(w http.ResponseWriter, r *http.Request) string {
	seed := ""
	if c, err := r.Cookie(CSRFSeedCookie); err == nil && c.Value != "" {
		seed = c.Value
	}
	if seed == "" {
		raw := make([]byte, 32)
		_, _ = rand.Read(raw)
		seed = base64.RawURLEncoding.EncodeToString(raw)
		http.SetCookie(w, &http.Cookie{
			Name:     CSRFSeedCookie,
			Value:    seed,
			Path:     "/",
			HttpOnly: true,
			Secure:   m.forceSSL,
			SameSite: http.SameSiteLaxMode,
			Expires:  time.Now().Add(csrfSeedTTL),
		})
	}
	return m.tokenForSeed(seed)
}

// CheckCSRF validates the csrf_token form field against the token derived
// from this request's existing seed cookie. Call after
// r.ParseForm()/ParseMultipartForm(). A request with no seed cookie yet
// (never rendered a form) always fails, which is the safe default.
func (m *Manager) CheckCSRF(r *http.Request) bool {
	c, err := r.Cookie(CSRFSeedCookie)
	if err != nil || c.Value == "" {
		return false
	}
	expected := m.tokenForSeed(c.Value)
	got := r.FormValue("csrf_token")
	return subtle.ConstantTimeCompare([]byte(got), []byte(expected)) == 1
}

// --- Flash messages ------------------------------------------------------

type Flash struct {
	Category string `json:"c"`
	Message  string `json:"m"`
}

// AddFlash appends a flash message, persisted in a short-lived cookie
// (mirrors Flask's session-backed flash() closely enough: it survives one
// redirect and is cleared on next read).
func (m *Manager) AddFlash(w http.ResponseWriter, r *http.Request, category, message string) {
	existing := m.ReadFlashesNoClear(r)
	existing = append(existing, Flash{Category: category, Message: message})
	body, _ := json.Marshal(existing)
	http.SetCookie(w, &http.Cookie{
		Name:     FlashCookieName,
		Value:    base64.RawURLEncoding.EncodeToString(body),
		Path:     "/",
		HttpOnly: true,
		Secure:   m.forceSSL,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   60,
	})
}

func (m *Manager) ReadFlashesNoClear(r *http.Request) []Flash {
	c, err := r.Cookie(FlashCookieName)
	if err != nil || c.Value == "" {
		return nil
	}
	body, err := base64.RawURLEncoding.DecodeString(c.Value)
	if err != nil {
		return nil
	}
	var flashes []Flash
	_ = json.Unmarshal(body, &flashes)
	return flashes
}

// PopFlashes returns pending flashes and clears the cookie (call once per
// page render, from the handler that will actually display them).
func (m *Manager) PopFlashes(w http.ResponseWriter, r *http.Request) []Flash {
	flashes := m.ReadFlashesNoClear(r)
	if len(flashes) > 0 {
		http.SetCookie(w, &http.Cookie{
			Name:     FlashCookieName,
			Value:    "",
			Path:     "/",
			HttpOnly: true,
			Secure:   m.forceSSL,
			SameSite: http.SameSiteLaxMode,
			MaxAge:   -1,
		})
	}
	return flashes
}

func FormatID(id int64) string {
	return strconv.FormatInt(id, 10)
}
