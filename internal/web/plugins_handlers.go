package web

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"vps-panel/internal/dockermgr"
	"vps-panel/internal/fileops"
	"vps-panel/internal/plugincatalog"
	"vps-panel/internal/store"
)

// pluginsEligible reports whether a server is a Bukkit-API-compatible Java
// server (Paper/Spigot/Purpur) -- the only kind that actually loads jars out
// of a plugins/ directory. Vanilla has no plugin support at all; Forge/Fabric
// use "mods", a different and incompatible ecosystem; Bedrock and the proxy
// image don't apply either. An empty/unset TYPE is treated as eligible since
// that's the friendly create-server form's own default (PAPER) and also
// itzg/minecraft-server's own default when TYPE isn't set.
func pluginsEligible(sv *store.Server) bool {
	if sv.Image != "itzg/minecraft-server" {
		return false
	}
	envMap, _ := dockermgr.ParseEnv(sv.Env)
	switch strings.ToUpper(strings.TrimSpace(envMap["TYPE"])) {
	case "", "PAPER", "SPIGOT", "PURPUR":
		return true
	default:
		return false
	}
}

func (s *Server) requirePluginsEligible(w http.ResponseWriter, sv *store.Server) bool {
	if !pluginsEligible(sv) {
		http.Error(w, "plugins aren't available for this server's type", http.StatusForbidden)
		return false
	}
	return true
}

type pluginCardRow struct {
	ID          string
	Name        string
	Description string
	Category    string
	Installed   bool
}

type installedPluginRow struct {
	Name      string
	SizeMBStr string
	MTimeStr  string
}

type pluginsData struct {
	Common
	Server    *store.Server
	Available []pluginCardRow
	Installed []installedPluginRow
}

func (s *Server) handleServerPlugins(w http.ResponseWriter, r *http.Request, user *store.User, sv *store.Server) {
	if !s.requirePluginsEligible(w, sv) {
		return
	}

	entries, err := plugincatalog.Load(s.cfg.PluginCatalogRoot)
	if err != nil {
		s.serverError(w, "load plugin catalog", err)
		return
	}
	installed, err := fileops.ListInstalledPlugins(serverDataRoot(sv))
	if err != nil {
		s.serverError(w, "list installed plugins", err)
		return
	}

	installedSet := make(map[string]bool, len(installed))
	for _, p := range installed {
		installedSet[p.Name] = true
	}

	available := make([]pluginCardRow, 0, len(entries))
	for _, e := range entries {
		available = append(available, pluginCardRow{
			ID:          e.ID,
			Name:        e.Name,
			Description: e.Description,
			Category:    e.Category,
			Installed:   installedSet[e.Filename],
		})
	}

	installedRows := make([]installedPluginRow, 0, len(installed))
	for _, p := range installed {
		installedRows = append(installedRows, installedPluginRow{
			Name:      p.Name,
			SizeMBStr: fmt.Sprintf("%.2f", float64(p.Size)/(1024*1024)),
			MTimeStr:  p.MTime.Format("2006-01-02 15:04"),
		})
	}

	data := pluginsData{Common: s.commonFor(w, r, user), Server: sv, Available: available, Installed: installedRows}
	s.render(w, r, user, sv.Name+" · Plugins", "", "server_plugins", data)
}

func (s *Server) handleServerPluginsInstall(w http.ResponseWriter, r *http.Request, user *store.User, sv *store.Server) {
	if !s.requirePluginsEligible(w, sv) {
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	if !s.checkCSRF(w, r) {
		return
	}

	pluginID := r.FormValue("plugin_id")
	entries, err := plugincatalog.Load(s.cfg.PluginCatalogRoot)
	if err != nil {
		s.serverError(w, "load plugin catalog", err)
		return
	}
	var found *plugincatalog.Entry
	for i := range entries {
		if entries[i].ID == pluginID {
			found = &entries[i]
			break
		}
	}
	if found == nil {
		s.flash(w, r, "error", "Unknown plugin")
		http.Redirect(w, r, "/server/"+itoa(sv.ID)+"/plugins", http.StatusFound)
		return
	}

	srcPath := plugincatalog.JarPath(s.cfg.PluginCatalogRoot, *found)
	err = fileops.InstallPlugin(serverDataRoot(sv), srcPath, found.Filename)
	if err != nil && os.IsPermission(err) {
		// Host-side write failed because the plugins/ dir (or its parent) is
		// owned by the game image's own internal uid, not the panel's host
		// uid -- same class of issue as world-reset's docker-exec fallback.
		// Needs the container running to exec into it.
		logErr("host-side plugin install failed, falling back to docker exec", err)
		err = s.installPluginViaExec(r.Context(), sv, srcPath, found.Filename)
	}
	if err != nil {
		s.flash(w, r, "error", "Install failed: "+err.Error())
	} else {
		s.flash(w, r, "success", found.Name+" installed — restart the server for it to load")
	}
	http.Redirect(w, r, "/server/"+itoa(sv.ID)+"/plugins", http.StatusFound)
}

// installPluginViaExec is the docker-exec-as-root fallback for InstallPlugin
// when the host can't write directly into the server's plugins/ directory.
// Starts the container if it isn't already running (exec requires it), then
// writes the jar to a .tmp sibling and renames it into place from inside the
// container's own namespace, same atomicity guarantee as the host-fs path.
func (s *Server) installPluginViaExec(ctx context.Context, sv *store.Server, srcPath, filename string) error {
	if s.docker.ContainerStatus(ctx, sv.ContainerID) != "running" {
		if err := s.docker.PowerAction(ctx, sv.ContainerID, "start"); err != nil {
			return fmt.Errorf("container isn't running and couldn't be started: %w", err)
		}
	}

	src, err := os.Open(srcPath)
	if err != nil {
		return err
	}
	defer src.Close()

	if err := s.docker.ExecMkdir(ctx, sv.ContainerID, "/data/plugins"); err != nil {
		return err
	}
	tmpDest := "/data/plugins/" + filename + ".tmp"
	finalDest := "/data/plugins/" + filename
	if err := s.docker.ExecWriteFile(ctx, sv.ContainerID, tmpDest, src); err != nil {
		return err
	}
	return s.docker.ExecRename(ctx, sv.ContainerID, tmpDest, finalDest)
}

func (s *Server) handleServerPluginsRemove(w http.ResponseWriter, r *http.Request, user *store.User, sv *store.Server) {
	if !s.requirePluginsEligible(w, sv) {
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	if !s.checkCSRF(w, r) {
		return
	}

	filename := r.FormValue("filename")
	err := fileops.RemoveInstalledPlugin(serverDataRoot(sv), filename)
	if err != nil && os.IsPermission(err) {
		// Same host/container uid mismatch as the install fallback -- only
		// reachable here if the container is running (needed for exec),
		// which it must be for the file to have been unremovable by the
		// host uid in the first place (itzg only creates files as its
		// internal uid while it's actually running).
		//
		// Use filepath.Base of the *raw* filename here, not filename itself:
		// RemoveInstalledPlugin already proved a lexically-cleaned version of
		// this path resolves to a direct child of plugins/, but reusing the
		// raw string for the exec command would let the container's own `rm`
		// re-resolve it independently (symlink-aware, unlike Go's lexical
		// Clean) -- collapsing to a bare filename closes that gap regardless.
		logErr("host-side plugin removal failed, falling back to docker exec", err)
		err = s.docker.ExecRemovePaths(r.Context(), sv.ContainerID, []string{"plugins/" + filepath.Base(filename)})
	}
	if err != nil {
		s.flash(w, r, "error", "Remove failed: "+err.Error())
	} else {
		s.flash(w, r, "success", "Plugin removed")
	}
	http.Redirect(w, r, "/server/"+itoa(sv.ID)+"/plugins", http.StatusFound)
}
