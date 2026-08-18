package web

import (
	"net/http"

	"vps-panel/internal/store"
)

type loginData struct {
	Common
}

func (s *Server) handleLoginForm(w http.ResponseWriter, r *http.Request) {
	if user := s.currentUser(r); user != nil {
		http.Redirect(w, r, "/", http.StatusFound)
		return
	}
	data := loginData{Common: s.commonFor(w, r, nil)}
	s.render(w, r, nil, "Log in", "", "login", data)
}

func (s *Server) handleLoginSubmit(w http.ResponseWriter, r *http.Request) {
	if user := s.currentUser(r); user != nil {
		http.Redirect(w, r, "/", http.StatusFound)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	if !s.checkCSRF(w, r) {
		return
	}
	username := r.FormValue("username")
	password := r.FormValue("password")

	user, err := s.store.GetUserByUsername(username)
	if err == nil && user.CheckPassword(password) {
		s.auth.SetSession(w, user.ID)
		next := r.URL.Query().Get("next")
		if next == "" {
			next = "/"
		}
		http.Redirect(w, r, next, http.StatusFound)
		return
	}

	s.flash(w, r, "error", "Invalid username or password")
	http.Redirect(w, r, "/login", http.StatusFound)
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request, user *store.User) {
	s.auth.ClearSession(w)
	http.Redirect(w, r, "/login", http.StatusFound)
}
