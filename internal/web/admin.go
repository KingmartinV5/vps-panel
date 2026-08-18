package web

import (
	"database/sql"
	"net/http"
	"os"
	"strconv"
	"strings"

	"vps-panel/internal/dockermgr"
	"vps-panel/internal/store"
)

// --- Users -----------------------------------------------------------------

type userRow struct {
	ID          int64
	Username    string
	Role        string
	ServerCount int
	ShowDelete  bool
}

type adminUsersData struct {
	Common
	Users []userRow
}

func (s *Server) handleAdminUsers(w http.ResponseWriter, r *http.Request, user *store.User) {
	users, err := s.store.ListUsers()
	if err != nil {
		s.serverError(w, "list users", err)
		return
	}
	rows := make([]userRow, 0, len(users))
	for _, u := range users {
		role := "customer"
		if u.IsAdmin {
			role = "admin"
		}
		count, _ := s.store.CountServersByOwner(u.ID)
		rows = append(rows, userRow{
			ID: u.ID, Username: u.Username, Role: role,
			ServerCount: count, ShowDelete: u.ID != user.ID,
		})
	}
	data := adminUsersData{Common: s.commonFor(w, r, user), Users: rows}
	s.render(w, r, user, "Users", "admin_users", "admin_users", data)
}

func (s *Server) handleAdminUsersCreate(w http.ResponseWriter, r *http.Request, user *store.User) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	if !s.checkCSRF(w, r) {
		return
	}
	username := strings.TrimSpace(r.FormValue("username"))
	password := r.FormValue("password")
	isAdmin := r.FormValue("is_admin") == "on"

	if username == "" || len(password) < 8 {
		s.flash(w, r, "error", "Username required and password must be at least 8 characters")
		http.Redirect(w, r, "/admin/users", http.StatusFound)
		return
	}
	if existing, _ := s.store.GetUserByUsername(username); existing != nil {
		s.flash(w, r, "error", "Username already exists")
		http.Redirect(w, r, "/admin/users", http.StatusFound)
		return
	}
	newUser := &store.User{Username: username, IsAdmin: isAdmin}
	if err := newUser.SetPassword(password); err != nil {
		s.serverError(w, "hash password", err)
		return
	}
	if _, err := s.store.CreateUser(newUser); err != nil {
		s.serverError(w, "create user", err)
		return
	}
	s.flash(w, r, "success", "Created user "+username)
	http.Redirect(w, r, "/admin/users", http.StatusFound)
}

func (s *Server) handleAdminUsersDelete(w http.ResponseWriter, r *http.Request, user *store.User) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	if !s.checkCSRF(w, r) {
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if id == user.ID {
		s.flash(w, r, "error", "You can't delete your own account")
		http.Redirect(w, r, "/admin/users", http.StatusFound)
		return
	}
	target, err := s.store.GetUser(id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	count, _ := s.store.CountServersByOwner(target.ID)
	if count > 0 {
		s.flash(w, r, "error", "Reassign or delete this user's servers before deleting the account")
		http.Redirect(w, r, "/admin/users", http.StatusFound)
		return
	}
	if err := s.store.DeleteUser(id); err != nil {
		s.serverError(w, "delete user", err)
		return
	}
	s.flash(w, r, "success", "User deleted")
	http.Redirect(w, r, "/admin/users", http.StatusFound)
}

// --- Servers -----------------------------------------------------------------

type adminCreateServerData struct {
	Common
	Customers     []*store.User
	AllowedImages []string
	FormName      string
	FormImage     string
	FormMemory    string
	FormPorts     string
	FormEnv       string
	FormOwnerID   string
}

func (s *Server) handleAdminServersCreateForm(w http.ResponseWriter, r *http.Request, user *store.User) {
	customers, err := s.store.ListUsers()
	if err != nil {
		s.serverError(w, "list users", err)
		return
	}
	data := adminCreateServerData{
		Common:        s.commonFor(w, r, user),
		Customers:     customers,
		AllowedImages: s.cfg.AllowedImages,
		FormMemory:    "2048",
	}
	s.render(w, r, user, "New Server", "admin_servers_create", "admin_create_server", data)
}

func (s *Server) handleAdminServersCreateSubmit(w http.ResponseWriter, r *http.Request, user *store.User) {
	customers, err := s.store.ListUsers()
	if err != nil {
		s.serverError(w, "list users", err)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	if !s.checkCSRF(w, r) {
		return
	}

	name := strings.TrimSpace(r.FormValue("name"))
	image := strings.TrimSpace(r.FormValue("image"))
	memoryRaw := strings.TrimSpace(r.FormValue("memory_mb"))
	ownerIDRaw := r.FormValue("owner_id")
	rawPorts := r.FormValue("ports")
	rawEnv := r.FormValue("env")

	rerender := func() {
		data := adminCreateServerData{
			Common:        s.commonFor(w, r, user),
			Customers:     customers,
			AllowedImages: s.cfg.AllowedImages,
			FormName:      name,
			FormImage:     image,
			FormMemory:    memoryRaw,
			FormPorts:     rawPorts,
			FormEnv:       rawEnv,
			FormOwnerID:   ownerIDRaw,
		}
		s.render(w, r, user, "New Server", "admin_servers_create", "admin_create_server", data)
	}

	var errs []string
	if name == "" {
		errs = append(errs, "Name is required")
	}
	imageAllowed := false
	for _, img := range s.cfg.AllowedImages {
		if img == image {
			imageAllowed = true
			break
		}
	}
	if !imageAllowed {
		errs = append(errs, "Image is not in the allowed list")
	}
	memoryMB, memErr := strconv.Atoi(memoryRaw)
	if memErr != nil {
		errs = append(errs, "Memory limit must be a number")
	} else if memoryMB < 256 {
		errs = append(errs, "Memory limit must be at least 256 MB")
	}

	ports, portsErr := dockermgr.ParsePortMappings(rawPorts)
	if portsErr != nil {
		errs = append(errs, portsErr.Error())
	}
	env, envErr := dockermgr.ParseEnv(rawEnv)
	if envErr != nil {
		errs = append(errs, envErr.Error())
	}

	slug := dockermgr.Slugify(name)
	if existing, _ := s.store.GetServerBySlug(slug); existing != nil {
		errs = append(errs, "A server with a similar name already exists")
	}

	if len(errs) > 0 {
		for _, e := range errs {
			s.flash(w, r, "error", e)
		}
		rerender()
		return
	}

	dataDir := s.cfg.ServersRoot + "/" + slug
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		s.serverError(w, "mkdir server data dir", err)
		return
	}
	containerName := "panel-" + slug

	containerID, err := s.docker.CreateContainer(r.Context(), containerName, image, dataDir, memoryMB, ports, env)
	if err != nil {
		s.flash(w, r, "error", "Docker error creating container: "+err.Error())
		rerender()
		return
	}

	var ownerID sql.NullInt64
	if ownerIDRaw != "" {
		if id, err := strconv.ParseInt(ownerIDRaw, 10, 64); err == nil {
			ownerID = sql.NullInt64{Int64: id, Valid: true}
		}
	}

	sv := &store.Server{
		Name: name, Slug: slug, Image: image,
		ContainerID: containerID, ContainerName: containerName,
		DataPath: dataDir, MemoryMB: memoryMB,
		PortMappings: rawPorts, OwnerID: ownerID,
	}
	if _, err := s.store.CreateServer(sv); err != nil {
		s.serverError(w, "create server row", err)
		return
	}
	s.flash(w, r, "success", "Server '"+name+"' created")
	http.Redirect(w, r, serverURL(sv.ID), http.StatusFound)
}

func (s *Server) handleAdminServersDelete(w http.ResponseWriter, r *http.Request, user *store.User) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	if !s.checkCSRF(w, r) {
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	sv, err := s.store.GetServer(id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	s.docker.DropAttachSocket(sv.ContainerID)
	if err := s.docker.RemoveContainer(r.Context(), sv.ContainerID); err != nil {
		logErr("remove container", err)
	}
	if r.FormValue("delete_data") == "on" {
		_ = os.RemoveAll(sv.DataPath)
	}
	if err := s.store.DeleteServer(id); err != nil {
		s.serverError(w, "delete server row", err)
		return
	}
	s.flash(w, r, "success", "Server '"+sv.Name+"' deleted")
	http.Redirect(w, r, "/", http.StatusFound)
}
