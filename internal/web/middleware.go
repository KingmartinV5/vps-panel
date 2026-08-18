package web

import (
	"net/http"
	"strconv"

	"vps-panel/internal/store"
)

// currentUser loads the User for the request's session cookie, or nil.
func (s *Server) currentUser(r *http.Request) *store.User {
	uid := s.auth.CurrentUserID(r)
	if uid == 0 {
		return nil
	}
	user, err := s.store.GetUser(uid)
	if err != nil {
		return nil
	}
	return user
}

func (s *Server) loginRedirect(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, "/login?next="+r.URL.Path, http.StatusFound)
}

// requireLogin mirrors @login_required.
func (s *Server) requireLogin(next func(w http.ResponseWriter, r *http.Request, user *store.User)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user := s.currentUser(r)
		if user == nil {
			s.loginRedirect(w, r)
			return
		}
		next(w, r, user)
	}
}

// requireAdmin mirrors @admin_required (login_required + is_admin check).
func (s *Server) requireAdmin(next func(w http.ResponseWriter, r *http.Request, user *store.User)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user := s.currentUser(r)
		if user == nil {
			s.loginRedirect(w, r)
			return
		}
		if !user.IsAdmin {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		next(w, r, user)
	}
}

// withServer mirrors @server_view: loads the Server for {id}, 404s if
// missing, 403s if the current user can't manage it.
func (s *Server) withServer(next func(w http.ResponseWriter, r *http.Request, user *store.User, sv *store.Server)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user := s.currentUser(r)
		if user == nil {
			s.loginRedirect(w, r)
			return
		}
		idStr := r.PathValue("id")
		id, err := strconv.ParseInt(idStr, 10, 64)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		sv, err := s.store.GetServer(id)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		if !sv.CanBeManagedBy(user) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		next(w, r, user, sv)
	}
}

// checkCSRF validates the CSRF token on a state-changing request. Call after
// parsing the form. Writes a 403 and returns false on failure.
func (s *Server) checkCSRF(w http.ResponseWriter, r *http.Request) bool {
	if !s.auth.CheckCSRF(r) {
		http.Error(w, "CSRF token missing or invalid", http.StatusForbidden)
		return false
	}
	return true
}
