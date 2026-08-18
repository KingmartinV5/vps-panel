package web

import (
	"bytes"
	"html/template"
	"log"
	"net/http"

	"vps-panel/internal/auth"
	"vps-panel/internal/store"
)

// Common is embedded into every page-specific data struct so content
// templates can reference the logged-in user / CSRF token directly, mirroring
// Flask-Login's current_user + Flask-WTF's csrf_token() being globally
// available inside Jinja templates.
type Common struct {
	CurrentUser *store.User
	CSRFToken   string
}

type BaseData struct {
	Title       string
	CurrentUser *store.User
	ActiveNav   string
	Flashes     []auth.Flash
	Content     template.HTML
}

func (s *Server) commonFor(w http.ResponseWriter, r *http.Request, user *store.User) Common {
	return Common{CurrentUser: user, CSRFToken: s.auth.EnsureCSRFToken(w, r)}
}

func (s *Server) render(w http.ResponseWriter, r *http.Request, currentUser *store.User, title, activeNav, tmplName string, data any) {
	var buf bytes.Buffer
	if err := s.templates.ExecuteTemplate(&buf, tmplName, data); err != nil {
		log.Printf("template render error (%s): %v", tmplName, err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	base := BaseData{
		Title:       title,
		CurrentUser: currentUser,
		ActiveNav:   activeNav,
		Flashes:     s.auth.PopFlashes(w, r),
		Content:     template.HTML(buf.String()),
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.templates.ExecuteTemplate(w, "base.html", base); err != nil {
		log.Printf("template render error (base.html): %v", err)
	}
}

func (s *Server) flash(w http.ResponseWriter, r *http.Request, category, message string) {
	s.auth.AddFlash(w, r, category, message)
}
