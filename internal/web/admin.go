package web

import (
	"database/sql"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"

	"vps-panel/internal/dockermgr"
	"vps-panel/internal/fileops"
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

type adminUserEditData struct {
	Common
	TargetUser *store.User
	Servers    []*store.Server
}

func (s *Server) handleAdminUsersEditForm(w http.ResponseWriter, r *http.Request, user *store.User) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	target, err := s.store.GetUser(id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	servers, err := s.store.ListServersByOwner(target.ID)
	if err != nil {
		s.serverError(w, "list user servers", err)
		return
	}
	data := adminUserEditData{Common: s.commonFor(w, r, user), TargetUser: target, Servers: servers}
	s.render(w, r, user, "Edit "+target.Username, "admin_users", "admin_edit_user", data)
}

func (s *Server) handleAdminUsersEditSubmit(w http.ResponseWriter, r *http.Request, user *store.User) {
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
	target, err := s.store.GetUser(id)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	newPassword := r.FormValue("password")
	isAdmin := r.FormValue("is_admin") == "on"

	if newPassword != "" {
		if len(newPassword) < 8 {
			s.flash(w, r, "error", "Password must be at least 8 characters")
			http.Redirect(w, r, "/admin/users/"+itoa(target.ID)+"/edit", http.StatusFound)
			return
		}
		if err := target.SetPassword(newPassword); err != nil {
			s.serverError(w, "hash password", err)
			return
		}
		if err := s.store.UpdateUserPassword(target); err != nil {
			s.serverError(w, "update password", err)
			return
		}
	}

	if id == user.ID && !isAdmin {
		s.flash(w, r, "error", "You can't remove your own admin access -- password change (if any) was still applied")
	} else if isAdmin != target.IsAdmin {
		if err := s.store.UpdateUserAdmin(target.ID, isAdmin); err != nil {
			s.serverError(w, "update admin flag", err)
			return
		}
	}

	s.flash(w, r, "success", "Updated "+target.Username)
	http.Redirect(w, r, "/admin/users", http.StatusFound)
}

// --- Servers: friendly type/env mapping -------------------------------------

type serverTypeInfo struct {
	Image string
	Icon  string
	Label string
	Known bool
}

// knownServerTypes maps the configured image allowlist to icon+label cards
// for the type picker. Any image an admin has added beyond the three this
// panel knows how to build a friendly form for still gets a card (so it's
// still selectable), just without an icon or friendly fields -- only the
// advanced/raw boxes apply to it.
func knownServerTypes(allowed []string) []serverTypeInfo {
	out := make([]serverTypeInfo, 0, len(allowed))
	for _, img := range allowed {
		switch img {
		case "itzg/minecraft-server":
			out = append(out, serverTypeInfo{Image: img, Icon: "type-java.svg", Label: "Minecraft: Java Edition", Known: true})
		case "itzg/minecraft-bedrock-server":
			out = append(out, serverTypeInfo{Image: img, Icon: "type-bedrock.svg", Label: "Minecraft: Bedrock Edition", Known: true})
		case "itzg/mc-proxy":
			out = append(out, serverTypeInfo{Image: img, Icon: "type-proxy.svg", Label: "Proxy (Velocity/Bungee/Waterfall)", Known: true})
		default:
			out = append(out, serverTypeInfo{Image: img, Label: img})
		}
	}
	return out
}

// defaultContainerPort is the "well known" container-side port+proto each
// friendly type's port field maps to -- used both to build the port mapping
// on submit and to split a stored raw ports string back into "the friendly
// field" vs "everything else, goes to advanced" when pre-filling the edit form.
var defaultContainerPort = map[string]string{
	"itzg/minecraft-server":         "25565/tcp",
	"itzg/minecraft-bedrock-server": "19132/udp",
	"itzg/mc-proxy":                 "25577/tcp",
}

// knownEnvKeys returns the env var names the friendly form for image models
// directly, so edit-prefill knows what to route into friendly fields vs. dump
// into the advanced textarea as leftover.
func knownEnvKeys(image string) map[string]bool {
	switch image {
	case "itzg/minecraft-server":
		return map[string]bool{
			"EULA": true, "TYPE": true, "VERSION": true, "DIFFICULTY": true, "MODE": true,
			"MOTD": true, "MAX_PLAYERS": true, "ONLINE_MODE": true, "WHITELIST": true, "ENABLE_WHITELIST": true,
		}
	case "itzg/minecraft-bedrock-server":
		return map[string]bool{
			"EULA": true, "SERVER_NAME": true, "GAMEMODE": true, "DIFFICULTY": true, "MAX_PLAYERS": true,
		}
	case "itzg/mc-proxy":
		return map[string]bool{"TYPE": true}
	default:
		return nil
	}
}

// buildFriendlyEnvAndPort reads the type-specific friendly fields from a
// submitted form and turns them into the env vars + one port-mapping line
// the chosen image needs. EULA=TRUE is injected unconditionally for the two
// Minecraft images -- itzg's images refuse to start without it, and there's
// no legitimate reason to use this panel without accepting it.
func buildFriendlyEnvAndPort(image string, r *http.Request) (env map[string]string, portRaw string) {
	env = map[string]string{}
	switch image {
	case "itzg/minecraft-server":
		env["EULA"] = "TRUE"
		software := strings.ToUpper(strings.TrimSpace(r.FormValue("java_software")))
		if software == "" {
			software = "PAPER"
		}
		env["TYPE"] = software
		version := strings.TrimSpace(r.FormValue("java_version"))
		if version == "" {
			version = "LATEST"
		}
		env["VERSION"] = version
		difficulty := r.FormValue("java_difficulty")
		if difficulty == "" {
			difficulty = "easy"
		}
		env["DIFFICULTY"] = difficulty
		mode := r.FormValue("java_mode")
		if mode == "" {
			mode = "survival"
		}
		env["MODE"] = mode
		motd := strings.TrimSpace(r.FormValue("java_motd"))
		if motd == "" {
			motd = "A KingsEmpire Minecraft Server"
		}
		env["MOTD"] = motd
		maxPlayers := strings.TrimSpace(r.FormValue("java_max_players"))
		if maxPlayers == "" {
			maxPlayers = "20"
		}
		env["MAX_PLAYERS"] = maxPlayers
		if r.FormValue("java_online_mode") == "on" {
			env["ONLINE_MODE"] = "TRUE"
		} else {
			env["ONLINE_MODE"] = "FALSE"
		}
		whitelist := strings.TrimSpace(r.FormValue("java_whitelist"))
		if whitelist != "" {
			env["WHITELIST"] = whitelist
			env["ENABLE_WHITELIST"] = "TRUE"
		}
		port := strings.TrimSpace(r.FormValue("java_port"))
		if port == "" {
			port = "25565"
		}
		portRaw = port + ":25565/tcp"

	case "itzg/minecraft-bedrock-server":
		env["EULA"] = "TRUE"
		name := strings.TrimSpace(r.FormValue("bedrock_server_name"))
		if name == "" {
			name = "KingsEmpire Bedrock Server"
		}
		env["SERVER_NAME"] = name
		gamemode := r.FormValue("bedrock_gamemode")
		if gamemode == "" {
			gamemode = "survival"
		}
		env["GAMEMODE"] = gamemode
		difficulty := r.FormValue("bedrock_difficulty")
		if difficulty == "" {
			difficulty = "easy"
		}
		env["DIFFICULTY"] = difficulty
		maxPlayers := strings.TrimSpace(r.FormValue("bedrock_max_players"))
		if maxPlayers == "" {
			maxPlayers = "10"
		}
		env["MAX_PLAYERS"] = maxPlayers
		port := strings.TrimSpace(r.FormValue("bedrock_port"))
		if port == "" {
			port = "19132"
		}
		portRaw = port + ":19132/udp"

	case "itzg/mc-proxy":
		proxyType := r.FormValue("proxy_type")
		if proxyType == "" {
			proxyType = "velocity"
		}
		env["TYPE"] = proxyType
		port := strings.TrimSpace(r.FormValue("proxy_port"))
		if port == "" {
			port = "25577"
		}
		portRaw = port + ":25577/tcp"
	}
	return env, portRaw
}

// mergeEnv combines the friendly-field env with the admin's advanced raw
// textarea, advanced winning on key collision so power users can always
// override or extend what the friendly fields produced.
func mergeEnv(friendly map[string]string, advancedRaw string) (map[string]string, error) {
	combined := map[string]string{}
	for k, v := range friendly {
		combined[k] = v
	}
	adv, err := dockermgr.ParseEnv(advancedRaw)
	if err != nil {
		return combined, err
	}
	for k, v := range adv {
		combined[k] = v
	}
	return combined, nil
}

func serializeEnv(env map[string]string) string {
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	lines := make([]string, 0, len(keys))
	for _, k := range keys {
		lines = append(lines, k+"="+env[k])
	}
	return strings.Join(lines, "\n")
}

func mergePortsRaw(friendlyPortRaw, advancedRaw string) string {
	var parts []string
	if strings.TrimSpace(friendlyPortRaw) != "" {
		parts = append(parts, strings.TrimSpace(friendlyPortRaw))
	}
	if strings.TrimSpace(advancedRaw) != "" {
		parts = append(parts, strings.TrimSpace(advancedRaw))
	}
	return strings.Join(parts, "\n")
}

// splitFriendlyPort pulls the one port-mapping line matching image's known
// container port out of rawPorts (for edit-prefill), returning its host port
// plus everything else untouched for the advanced textarea.
func splitFriendlyPort(image, rawPorts string) (friendlyHostPort string, leftoverRaw string) {
	want := defaultContainerPort[image]
	if want == "" || strings.TrimSpace(rawPorts) == "" {
		return "", rawPorts
	}
	lines := strings.Split(strings.ReplaceAll(rawPorts, ",", "\n"), "\n")
	var leftover []string
	found := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		parts := strings.SplitN(trimmed, ":", 2)
		if len(parts) == 2 {
			rest := parts[1]
			if !strings.Contains(rest, "/") {
				rest += "/tcp"
			}
			if !found && rest == want {
				friendlyHostPort = parts[0]
				found = true
				continue
			}
		}
		leftover = append(leftover, trimmed)
	}
	return friendlyHostPort, strings.Join(leftover, "\n")
}

// --- Servers: create/edit form data -----------------------------------------

type serverFormData struct {
	Common
	Mode       string // "create" or "edit"
	FormAction string
	ServerID   int64

	Customers []*store.User
	Types     []serverTypeInfo

	FormName    string
	FormImage   string
	FormMemory  string
	FormOwnerID string

	JavaSoftware   string
	JavaVersion    string
	JavaDifficulty string
	JavaMode       string
	JavaMOTD       string
	JavaMaxPlayers string
	JavaOnlineMode bool
	JavaWhitelist  string
	JavaPort       string

	BedrockServerName string
	BedrockGamemode   string
	BedrockDifficulty string
	BedrockMaxPlayers string
	BedrockPort       string

	ProxyType string
	ProxyPort string

	AdvEnv   string
	AdvPorts string
}

func defaultServerFormData() serverFormData {
	return serverFormData{
		FormMemory:        "2048",
		JavaSoftware:      "PAPER",
		JavaVersion:       "LATEST",
		JavaDifficulty:    "easy",
		JavaMode:          "survival",
		JavaMOTD:          "A KingsEmpire Minecraft Server",
		JavaMaxPlayers:    "20",
		JavaOnlineMode:    true,
		JavaPort:          "25565",
		BedrockServerName: "KingsEmpire Bedrock Server",
		BedrockGamemode:   "survival",
		BedrockDifficulty: "easy",
		BedrockMaxPlayers: "10",
		BedrockPort:       "19132",
		ProxyType:         "velocity",
		ProxyPort:         "25577",
	}
}

// formDataFromRequest captures every field as submitted, so a validation
// error can re-render the form with what the admin actually typed instead of
// resetting to defaults.
func formDataFromRequest(r *http.Request) serverFormData {
	return serverFormData{
		FormName:    strings.TrimSpace(r.FormValue("name")),
		FormImage:   strings.TrimSpace(r.FormValue("image")),
		FormMemory:  strings.TrimSpace(r.FormValue("memory_mb")),
		FormOwnerID: r.FormValue("owner_id"),

		JavaSoftware:   r.FormValue("java_software"),
		JavaVersion:    r.FormValue("java_version"),
		JavaDifficulty: r.FormValue("java_difficulty"),
		JavaMode:       r.FormValue("java_mode"),
		JavaMOTD:       r.FormValue("java_motd"),
		JavaMaxPlayers: r.FormValue("java_max_players"),
		JavaOnlineMode: r.FormValue("java_online_mode") == "on",
		JavaWhitelist:  r.FormValue("java_whitelist"),
		JavaPort:       r.FormValue("java_port"),

		BedrockServerName: r.FormValue("bedrock_server_name"),
		BedrockGamemode:   r.FormValue("bedrock_gamemode"),
		BedrockDifficulty: r.FormValue("bedrock_difficulty"),
		BedrockMaxPlayers: r.FormValue("bedrock_max_players"),
		BedrockPort:       r.FormValue("bedrock_port"),

		ProxyType: r.FormValue("proxy_type"),
		ProxyPort: r.FormValue("proxy_port"),

		AdvEnv:   r.FormValue("adv_env"),
		AdvPorts: r.FormValue("adv_ports"),
	}
}

// prefillFormFromServer reconstructs a serverFormData for the edit form from
// a stored Server row, routing known env/port values into their friendly
// fields and dumping anything the friendly fields don't model into the
// advanced raw boxes -- so editing never silently drops custom settings.
func prefillFormFromServer(sv *store.Server) serverFormData {
	data := defaultServerFormData()
	data.FormName = sv.Name
	data.FormImage = sv.Image
	data.FormMemory = strconv.Itoa(sv.MemoryMB)
	if sv.OwnerID.Valid {
		data.FormOwnerID = strconv.FormatInt(sv.OwnerID.Int64, 10)
	}

	envMap, _ := dockermgr.ParseEnv(sv.Env)
	known := knownEnvKeys(sv.Image)
	leftoverEnv := map[string]string{}
	for k, v := range envMap {
		if known[k] {
			continue
		}
		leftoverEnv[k] = v
	}

	switch sv.Image {
	case "itzg/minecraft-server":
		if v, ok := envMap["TYPE"]; ok {
			data.JavaSoftware = v
		}
		if v, ok := envMap["VERSION"]; ok {
			data.JavaVersion = v
		}
		if v, ok := envMap["DIFFICULTY"]; ok {
			data.JavaDifficulty = v
		}
		if v, ok := envMap["MODE"]; ok {
			data.JavaMode = v
		}
		if v, ok := envMap["MOTD"]; ok {
			data.JavaMOTD = v
		}
		if v, ok := envMap["MAX_PLAYERS"]; ok {
			data.JavaMaxPlayers = v
		}
		data.JavaOnlineMode = envMap["ONLINE_MODE"] != "FALSE"
		if v, ok := envMap["WHITELIST"]; ok {
			data.JavaWhitelist = v
		}
	case "itzg/minecraft-bedrock-server":
		if v, ok := envMap["SERVER_NAME"]; ok {
			data.BedrockServerName = v
		}
		if v, ok := envMap["GAMEMODE"]; ok {
			data.BedrockGamemode = v
		}
		if v, ok := envMap["DIFFICULTY"]; ok {
			data.BedrockDifficulty = v
		}
		if v, ok := envMap["MAX_PLAYERS"]; ok {
			data.BedrockMaxPlayers = v
		}
	case "itzg/mc-proxy":
		if v, ok := envMap["TYPE"]; ok {
			data.ProxyType = v
		}
	}
	data.AdvEnv = serializeEnv(leftoverEnv)

	friendlyPort, leftoverPorts := splitFriendlyPort(sv.Image, sv.PortMappings)
	switch sv.Image {
	case "itzg/minecraft-server":
		if friendlyPort != "" {
			data.JavaPort = friendlyPort
		}
	case "itzg/minecraft-bedrock-server":
		if friendlyPort != "" {
			data.BedrockPort = friendlyPort
		}
	case "itzg/mc-proxy":
		if friendlyPort != "" {
			data.ProxyPort = friendlyPort
		}
	}
	data.AdvPorts = leftoverPorts

	return data
}

// --- Servers: create -----------------------------------------------------

func (s *Server) handleAdminServersCreateForm(w http.ResponseWriter, r *http.Request, user *store.User) {
	customers, err := s.store.ListUsers()
	if err != nil {
		s.serverError(w, "list users", err)
		return
	}
	data := defaultServerFormData()
	data.Common = s.commonFor(w, r, user)
	data.Mode = "create"
	data.FormAction = "/admin/servers/create"
	data.Customers = customers
	data.Types = knownServerTypes(s.cfg.AllowedImages)
	if len(s.cfg.AllowedImages) > 0 {
		data.FormImage = s.cfg.AllowedImages[0]
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
	advPorts := r.FormValue("adv_ports")
	advEnv := r.FormValue("adv_env")

	rerender := func() {
		data := formDataFromRequest(r)
		data.Common = s.commonFor(w, r, user)
		data.Mode = "create"
		data.FormAction = "/admin/servers/create"
		data.Customers = customers
		data.Types = knownServerTypes(s.cfg.AllowedImages)
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

	var friendlyEnv map[string]string
	var friendlyPort string
	if imageAllowed {
		friendlyEnv, friendlyPort = buildFriendlyEnvAndPort(image, r)
	}
	combinedEnv, envErr := mergeEnv(friendlyEnv, advEnv)
	if envErr != nil {
		errs = append(errs, envErr.Error())
	}
	combinedPortsRaw := mergePortsRaw(friendlyPort, advPorts)
	ports, portsErr := dockermgr.ParsePortMappings(combinedPortsRaw)
	if portsErr != nil {
		errs = append(errs, portsErr.Error())
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

	containerID, err := s.docker.CreateContainer(r.Context(), containerName, image, dataDir, memoryMB, ports, combinedEnv)
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
		PortMappings: combinedPortsRaw, Env: serializeEnv(combinedEnv),
		OwnerID: ownerID,
	}
	if _, err := s.store.CreateServer(sv); err != nil {
		s.serverError(w, "create server row", err)
		return
	}
	s.flash(w, r, "success", "Server '"+name+"' created")
	http.Redirect(w, r, serverURL(sv.ID), http.StatusFound)
}

// --- Servers: edit ---------------------------------------------------------

func (s *Server) handleAdminServersEditForm(w http.ResponseWriter, r *http.Request, user *store.User) {
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
	customers, err := s.store.ListUsers()
	if err != nil {
		s.serverError(w, "list users", err)
		return
	}
	data := prefillFormFromServer(sv)
	data.Common = s.commonFor(w, r, user)
	data.Mode = "edit"
	data.ServerID = sv.ID
	data.FormAction = "/server/" + itoa(sv.ID) + "/edit"
	data.Customers = customers
	data.Types = knownServerTypes(s.cfg.AllowedImages)
	s.render(w, r, user, "Edit "+sv.Name, "", "admin_edit_server", data)
}

func (s *Server) handleAdminServersEditSubmit(w http.ResponseWriter, r *http.Request, user *store.User) {
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
	advPorts := r.FormValue("adv_ports")
	advEnv := r.FormValue("adv_env")

	rerender := func() {
		data := formDataFromRequest(r)
		data.Common = s.commonFor(w, r, user)
		data.Mode = "edit"
		data.ServerID = sv.ID
		data.FormAction = "/server/" + itoa(sv.ID) + "/edit"
		data.Customers = customers
		data.Types = knownServerTypes(s.cfg.AllowedImages)
		s.render(w, r, user, "Edit "+sv.Name, "", "admin_edit_server", data)
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

	var friendlyEnv map[string]string
	var friendlyPort string
	if imageAllowed {
		friendlyEnv, friendlyPort = buildFriendlyEnvAndPort(image, r)
	}
	combinedEnv, envErr := mergeEnv(friendlyEnv, advEnv)
	if envErr != nil {
		errs = append(errs, envErr.Error())
	}
	combinedPortsRaw := mergePortsRaw(friendlyPort, advPorts)
	ports, portsErr := dockermgr.ParsePortMappings(combinedPortsRaw)
	if portsErr != nil {
		errs = append(errs, portsErr.Error())
	}

	if len(errs) > 0 {
		for _, e := range errs {
			s.flash(w, r, "error", e)
		}
		rerender()
		return
	}

	var ownerID sql.NullInt64
	if ownerIDRaw != "" {
		if oid, err := strconv.ParseInt(ownerIDRaw, 10, 64); err == nil {
			ownerID = sql.NullInt64{Int64: oid, Valid: true}
		}
	}

	// Docker env vars are baked in at container creation -- there's no
	// live-edit, so recreate it: stop + remove the old container, create a
	// fresh one with the same container_name/data dir (so the bind-mounted
	// world persists), and point the DB row at the new container ID.
	if err := s.docker.PowerAction(r.Context(), sv.ContainerID, "stop"); err != nil {
		logErr("stop old container before recreate", err)
	}
	s.docker.DropAttachSocket(sv.ContainerID)
	if err := s.docker.RemoveContainer(r.Context(), sv.ContainerID); err != nil {
		logErr("remove old container before recreate", err)
	}

	containerID, err := s.docker.CreateContainer(r.Context(), sv.ContainerName, image, sv.DataPath, memoryMB, ports, combinedEnv)
	if err != nil {
		s.flash(w, r, "error", "Docker error recreating container: "+err.Error())
		rerender()
		return
	}

	sv.Name = name
	sv.Image = image
	sv.ContainerID = containerID
	sv.MemoryMB = memoryMB
	sv.PortMappings = combinedPortsRaw
	sv.Env = serializeEnv(combinedEnv)
	sv.OwnerID = ownerID

	if err := s.store.UpdateServer(sv); err != nil {
		s.serverError(w, "update server row", err)
		return
	}
	s.flash(w, r, "success", "Server '"+name+"' updated -- container recreated and restarting. World/data files were preserved.")
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

// --- Servers: reset world data ---------------------------------------------

func (s *Server) handleAdminServersResetWorld(w http.ResponseWriter, r *http.Request, user *store.User) {
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

	if err := s.docker.PowerAction(r.Context(), sv.ContainerID, "stop"); err != nil {
		logErr("stop container before world reset", err)
	}

	if resetErr := fileops.ResetWorld(sv.DataPath); resetErr != nil {
		// Host-side deletion failed -- most likely a UID mismatch between the
		// panel process and the game image's internal user (itzg images
		// default to uid 1000). Fall back to deleting from inside the
		// container's own namespace via docker exec, which needs it running.
		logErr("host-side world reset failed, falling back to docker exec", resetErr)
		if startErr := s.docker.PowerAction(r.Context(), sv.ContainerID, "start"); startErr != nil {
			s.flash(w, r, "error", "World reset failed: "+resetErr.Error())
			http.Redirect(w, r, serverURL(sv.ID), http.StatusFound)
			return
		}
		if execErr := s.docker.ExecRemovePaths(r.Context(), sv.ContainerID, fileops.KnownWorldDirs); execErr != nil {
			s.flash(w, r, "error", "World reset failed: "+execErr.Error())
			http.Redirect(w, r, serverURL(sv.ID), http.StatusFound)
			return
		}
	}

	if err := s.docker.PowerAction(r.Context(), sv.ContainerID, "restart"); err != nil {
		logErr("restart container after world reset", err)
	}
	s.flash(w, r, "success", "World data reset -- server restarting with a fresh world. Configs, plugins, and other files were left alone.")
	http.Redirect(w, r, serverURL(sv.ID), http.StatusFound)
}
