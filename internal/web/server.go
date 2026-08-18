// Package web is the HTTP layer: routes, middleware, templates. Replaces
// app.py's Flask routes.
package web

import (
	"html/template"
	"io/fs"
	"log"
	"net/http"
	"strconv"

	"vps-panel/internal/auth"
	"vps-panel/internal/config"
	"vps-panel/internal/dockermgr"
	"vps-panel/internal/store"
)

type Server struct {
	cfg       *config.Config
	store     *store.Store
	docker    *dockermgr.Manager
	auth      *auth.Manager
	templates *template.Template
	mux       *http.ServeMux
}

func New(cfg *config.Config, st *store.Store, docker *dockermgr.Manager, authMgr *auth.Manager, templatesFS, staticFS fs.FS) (*Server, error) {
	funcs := template.FuncMap{
		"idstr": func(id int64) string { return strconv.FormatInt(id, 10) },
	}
	tmpl, err := template.New("").Funcs(funcs).ParseFS(templatesFS, "*.html")
	if err != nil {
		return nil, err
	}

	s := &Server{
		cfg:       cfg,
		store:     st,
		docker:    docker,
		auth:      authMgr,
		templates: tmpl,
		mux:       http.NewServeMux(),
	}
	s.routes(staticFS)
	return s, nil
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mux.ServeHTTP(w, r)
}

func (s *Server) routes(staticFS fs.FS) {
	s.mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServer(http.FS(staticFS))))

	s.mux.HandleFunc("GET /login", s.handleLoginForm)
	s.mux.HandleFunc("POST /login", s.handleLoginSubmit)
	s.mux.HandleFunc("GET /logout", s.requireLogin(s.handleLogout))

	s.mux.HandleFunc("GET /{$}", s.requireLogin(s.handleDashboard))

	s.mux.HandleFunc("GET /server/{id}", s.withServer(s.handleServerDetail))
	s.mux.HandleFunc("POST /server/{id}/power", s.withServer(s.handleServerPower))
	s.mux.HandleFunc("GET /server/{id}/stats", s.withServer(s.handleServerStats))
	s.mux.HandleFunc("GET /server/{id}/console/stream", s.withServer(s.handleConsoleStream))
	s.mux.HandleFunc("POST /server/{id}/console/send", s.withServer(s.handleConsoleSend))

	s.mux.HandleFunc("GET /server/{id}/files", s.withServer(s.handleFilesList))
	s.mux.HandleFunc("GET /server/{id}/files/edit", s.withServer(s.handleFilesEditForm))
	s.mux.HandleFunc("POST /server/{id}/files/edit", s.withServer(s.handleFilesEditSubmit))
	s.mux.HandleFunc("POST /server/{id}/files/upload", s.withServer(s.handleFilesUpload))
	s.mux.HandleFunc("POST /server/{id}/files/mkdir", s.withServer(s.handleFilesMkdir))
	s.mux.HandleFunc("POST /server/{id}/files/delete", s.withServer(s.handleFilesDelete))
	s.mux.HandleFunc("GET /server/{id}/files/download", s.withServer(s.handleFilesDownload))

	s.mux.HandleFunc("GET /server/{id}/backups", s.withServer(s.handleBackupsList))
	s.mux.HandleFunc("POST /server/{id}/backups/create", s.withServer(s.handleBackupsCreate))
	s.mux.HandleFunc("GET /server/{id}/backups/{filename}/download", s.withServer(s.handleBackupsDownload))
	s.mux.HandleFunc("POST /server/{id}/backups/{filename}/delete", s.withServer(s.handleBackupsDelete))

	s.mux.HandleFunc("GET /admin/users", s.requireAdmin(s.handleAdminUsers))
	s.mux.HandleFunc("POST /admin/users/create", s.requireAdmin(s.handleAdminUsersCreate))
	s.mux.HandleFunc("POST /admin/users/{id}/delete", s.requireAdmin(s.handleAdminUsersDelete))

	s.mux.HandleFunc("GET /admin/servers/create", s.requireAdmin(s.handleAdminServersCreateForm))
	s.mux.HandleFunc("POST /admin/servers/create", s.requireAdmin(s.handleAdminServersCreateSubmit))
	s.mux.HandleFunc("POST /server/{id}/delete", s.requireAdmin(s.handleAdminServersDelete))
}

func logErr(context string, err error) {
	if err != nil {
		log.Printf("%s: %v", context, err)
	}
}

// serverError logs the real error (never shown to the client) and writes a
// generic 500, so failures are diagnosable from the panel's own log without
// leaking internals to whoever triggered them.
func (s *Server) serverError(w http.ResponseWriter, context string, err error) {
	log.Printf("%s: %v", context, err)
	http.Error(w, "internal server error", http.StatusInternalServerError)
}
