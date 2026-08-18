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

type accountData struct {
	Common
}

// handleAccountForm serves the self-service "My Account" page (any logged-in
// user, not just admins) -- currently just the password-change form, but a
// natural home for future self-service account settings.
func (s *Server) handleAccountForm(w http.ResponseWriter, r *http.Request, user *store.User) {
	data := accountData{Common: s.commonFor(w, r, user)}
	s.render(w, r, user, "My Account", "account", "account", data)
}

func (s *Server) handleAccountPasswordSubmit(w http.ResponseWriter, r *http.Request, user *store.User) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	if !s.checkCSRF(w, r) {
		return
	}
	current := r.FormValue("current_password")
	newPassword := r.FormValue("new_password")
	confirm := r.FormValue("confirm_password")

	if !user.CheckPassword(current) {
		s.flash(w, r, "error", "Current password is incorrect")
		http.Redirect(w, r, "/account", http.StatusFound)
		return
	}
	if len(newPassword) < 8 {
		s.flash(w, r, "error", "New password must be at least 8 characters")
		http.Redirect(w, r, "/account", http.StatusFound)
		return
	}
	if newPassword != confirm {
		s.flash(w, r, "error", "New passwords don't match")
		http.Redirect(w, r, "/account", http.StatusFound)
		return
	}

	if err := user.SetPassword(newPassword); err != nil {
		s.serverError(w, "hash new password", err)
		return
	}
	if err := s.store.UpdateUserPassword(user); err != nil {
		s.serverError(w, "update password", err)
		return
	}
	s.flash(w, r, "success", "Password changed")
	http.Redirect(w, r, "/account", http.StatusFound)
}
