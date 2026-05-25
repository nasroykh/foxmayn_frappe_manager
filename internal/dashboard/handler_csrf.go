package dashboard

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"net/http"
	"time"
)

const (
	csrfCookieName = "ffm_csrf"
	csrfFormField  = "csrf_token"
	csrfMaxAge     = 24 * time.Hour
)

func (h *Handler) ensureCSRF(w http.ResponseWriter, r *http.Request) string {
	if c, err := r.Cookie(csrfCookieName); err == nil && c.Value != "" {
		if token := h.csrfTokenFromCookie(c.Value); token != "" {
			return token
		}
	}
	nonce := make([]byte, 16)
	_, _ = rand.Read(nonce)
	cookieVal := hex.EncodeToString(nonce)
	token := h.signCSRF(cookieVal)
	http.SetCookie(w, &http.Cookie{
		Name:     csrfCookieName,
		Value:    cookieVal,
		Path:     "/admin",
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   int(csrfMaxAge.Seconds()),
	})
	return token
}

func (h *Handler) signCSRF(nonce string) string {
	mac := hmac.New(sha256.New, []byte(h.Password))
	mac.Write([]byte(nonce))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func (h *Handler) csrfTokenFromCookie(cookieVal string) string {
	if cookieVal == "" {
		return ""
	}
	return h.signCSRF(cookieVal)
}

func (h *Handler) validateCSRF(r *http.Request) bool {
	token := r.FormValue(csrfFormField)
	if token == "" {
		return false
	}
	c, err := r.Cookie(csrfCookieName)
	if err != nil {
		return false
	}
	expected := h.csrfTokenFromCookie(c.Value)
	return subtle.ConstantTimeCompare([]byte(token), []byte(expected)) == 1
}
