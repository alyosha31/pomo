package main

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	cookieName = "pomo_session"
	sessionTTL = 30 * 24 * time.Hour
)

// Auth is a single shared password with signed, expiring session cookies.
// With no password configured every request is allowed.
type Auth struct {
	password string
	key      []byte
}

// NewAuth signs sessions with a random secret kept next to the data file, so
// logins survive restarts. The key also depends on the password, so changing
// the password logs every session out.
func NewAuth(password, dir string) (*Auth, error) {
	if password == "" {
		return &Auth{}, nil
	}
	path := filepath.Join(dir, "secret")
	secret, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) || len(secret) < 32 {
		secret = make([]byte, 32)
		if _, err := rand.Read(secret); err != nil {
			return nil, err
		}
		if err := os.WriteFile(path, secret, 0o600); err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, err
	}
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(password))
	return &Auth{password: password, key: mac.Sum(nil)}, nil
}

func (a *Auth) Enabled() bool { return a.password != "" }

func (a *Auth) CheckPassword(pw string) bool {
	return subtle.ConstantTimeCompare([]byte(pw), []byte(a.password)) == 1
}

func (a *Auth) sign(msg string) string {
	mac := hmac.New(sha256.New, a.key)
	mac.Write([]byte(msg))
	return hex.EncodeToString(mac.Sum(nil))
}

func (a *Auth) newToken(now time.Time) string {
	exp := strconv.FormatInt(now.Add(sessionTTL).Unix(), 10)
	return exp + "." + a.sign(exp)
}

func (a *Auth) validToken(tok string, now time.Time) bool {
	exp, sig, ok := strings.Cut(tok, ".")
	if !ok {
		return false
	}
	unix, err := strconv.ParseInt(exp, 10, 64)
	if err != nil || now.Unix() > unix {
		return false
	}
	return hmac.Equal([]byte(sig), []byte(a.sign(exp)))
}

func (a *Auth) Authed(r *http.Request) bool {
	if !a.Enabled() {
		return true
	}
	c, err := r.Cookie(cookieName)
	return err == nil && a.validToken(c.Value, time.Now())
}

func (a *Auth) SetSession(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name:     cookieName,
		Value:    a.newToken(time.Now()),
		Path:     "/",
		MaxAge:   int(sessionTTL.Seconds()),
		HttpOnly: true,
		Secure:   isHTTPS(r),
		SameSite: http.SameSiteLaxMode,
	})
}

func (a *Auth) ClearSession(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{Name: cookieName, Value: "", Path: "/", MaxAge: -1, HttpOnly: true})
}

func isHTTPS(r *http.Request) bool {
	return r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https"
}
