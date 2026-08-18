package web

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"vps-panel/internal/fileops"
	"vps-panel/internal/store"
)

func serverDataRoot(sv *store.Server) string {
	return sv.DataPath
}

type fileRow struct {
	Name        string
	IsDir       bool
	SizeDisplay string
	MTimeStr    string
	RelPath     string
}

type filesData struct {
	Common
	Server     *store.Server
	Path       string
	PathParts  []string
	HasParent  bool
	ParentPath string
	Entries    []fileRow
}

func joinRel(path, name string) string {
	if path == "" {
		return name
	}
	return path + "/" + name
}

func (s *Server) handleFilesList(w http.ResponseWriter, r *http.Request, user *store.User, sv *store.Server) {
	rawPath := r.URL.Query().Get("path")
	path := strings.Trim(rawPath, "/")

	entries, err := fileops.ListDir(serverDataRoot(sv), path)
	if err != nil {
		if errors.Is(err, fileops.ErrPathSecurity) || errors.Is(err, os.ErrNotExist) {
			http.NotFound(w, r)
			return
		}
		s.serverError(w, "list dir", err)
		return
	}

	rows := make([]fileRow, 0, len(entries))
	for _, e := range entries {
		sizeDisplay := ""
		if !e.IsDir {
			sizeDisplay = strconv.FormatInt(e.Size, 10)
		}
		rows = append(rows, fileRow{
			Name:        e.Name,
			IsDir:       e.IsDir,
			SizeDisplay: sizeDisplay,
			MTimeStr:    e.MTime.Format("2006-01-02 15:04"),
			RelPath:     joinRel(path, e.Name),
		})
	}

	var pathParts []string
	var parent string
	hasParent := path != ""
	if hasParent {
		pathParts = strings.Split(path, "/")
		parts := pathParts[:len(pathParts)-1]
		parent = strings.Join(parts, "/")
	}

	data := filesData{
		Common:     s.commonFor(w, r, user),
		Server:     sv,
		Path:       path,
		PathParts:  pathParts,
		HasParent:  hasParent,
		ParentPath: url.QueryEscape(parent),
		Entries:    rows,
	}
	s.render(w, r, user, sv.Name+" · Files", "", "files", data)
}

type editFileData struct {
	Common
	Server   *store.Server
	Path     string
	Content  string
	BackPath string
}

func (s *Server) handleFilesEditForm(w http.ResponseWriter, r *http.Request, user *store.User, sv *store.Server) {
	path := r.URL.Query().Get("path")
	if path == "" {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	maxBytes := int64(s.cfg.MaxEditMB) * 1024 * 1024
	content, err := fileops.ReadTextFile(serverDataRoot(sv), path, maxBytes)
	if err != nil {
		if errors.Is(err, fileops.ErrPathSecurity) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		if errors.Is(err, os.ErrNotExist) {
			http.NotFound(w, r)
			return
		}
		s.flash(w, r, "error", err.Error())
		http.Redirect(w, r, "/server/"+itoa(sv.ID)+"/files", http.StatusFound)
		return
	}

	backPath := ""
	if idx := strings.LastIndex(path, "/"); idx >= 0 {
		backPath = path[:idx]
	}

	data := editFileData{
		Common:   s.commonFor(w, r, user),
		Server:   sv,
		Path:     path,
		Content:  content,
		BackPath: url.QueryEscape(backPath),
	}
	s.render(w, r, user, "Edit "+path, "", "edit_file", data)
}

func (s *Server) handleFilesEditSubmit(w http.ResponseWriter, r *http.Request, user *store.User, sv *store.Server) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	if !s.checkCSRF(w, r) {
		return
	}
	path := r.FormValue("path")
	if path == "" {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if err := fileops.WriteTextFile(serverDataRoot(sv), path, r.FormValue("content")); err != nil {
		if errors.Is(err, fileops.ErrPathSecurity) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		s.serverError(w, "write file", err)
		return
	}
	s.flash(w, r, "success", "File saved")
	http.Redirect(w, r, "/server/"+itoa(sv.ID)+"/files/edit?path="+url.QueryEscape(path), http.StatusFound)
}

func (s *Server) handleFilesUpload(w http.ResponseWriter, r *http.Request, user *store.User, sv *store.Server) {
	maxUpload := int64(s.cfg.MaxUploadMB) * 1024 * 1024
	r.Body = http.MaxBytesReader(w, r.Body, maxUpload)
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		s.flash(w, r, "error", "Upload rejected: file too large or malformed request")
		http.Redirect(w, r, "/server/"+itoa(sv.ID)+"/files", http.StatusFound)
		return
	}
	if !s.checkCSRF(w, r) {
		return
	}
	path := r.FormValue("path")
	file, header, err := r.FormFile("file")
	if err != nil || header.Filename == "" {
		s.flash(w, r, "error", "No file selected")
		http.Redirect(w, r, "/server/"+itoa(sv.ID)+"/files?path="+url.QueryEscape(path), http.StatusFound)
		return
	}
	file.Close()

	if _, err := fileops.SaveUpload(serverDataRoot(sv), path, header); err != nil {
		s.flash(w, r, "error", "Upload rejected: "+err.Error())
	} else {
		s.flash(w, r, "success", "File uploaded")
	}
	http.Redirect(w, r, "/server/"+itoa(sv.ID)+"/files?path="+url.QueryEscape(path), http.StatusFound)
}

func (s *Server) handleFilesMkdir(w http.ResponseWriter, r *http.Request, user *store.User, sv *store.Server) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	if !s.checkCSRF(w, r) {
		return
	}
	path := r.FormValue("path")
	name := fileops.SecureFilename(r.FormValue("name"))
	if name == "" {
		s.flash(w, r, "error", "Invalid folder name")
		http.Redirect(w, r, "/server/"+itoa(sv.ID)+"/files?path="+url.QueryEscape(path), http.StatusFound)
		return
	}
	if err := fileops.MakeDir(serverDataRoot(sv), joinRel(path, name)); err != nil {
		if errors.Is(err, fileops.ErrPathSecurity) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		s.serverError(w, "mkdir", err)
		return
	}
	http.Redirect(w, r, "/server/"+itoa(sv.ID)+"/files?path="+url.QueryEscape(path), http.StatusFound)
}

func (s *Server) handleFilesDelete(w http.ResponseWriter, r *http.Request, user *store.User, sv *store.Server) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	if !s.checkCSRF(w, r) {
		return
	}
	path := r.FormValue("path")
	if err := fileops.DeletePath(serverDataRoot(sv), path); err != nil {
		s.flash(w, r, "error", err.Error())
	} else {
		s.flash(w, r, "success", "Deleted")
	}
	parent := ""
	trimmed := strings.Trim(path, "/")
	if idx := strings.LastIndex(trimmed, "/"); idx >= 0 {
		parent = trimmed[:idx]
	}
	http.Redirect(w, r, "/server/"+itoa(sv.ID)+"/files?path="+url.QueryEscape(parent), http.StatusFound)
}

func (s *Server) handleFilesDownload(w http.ResponseWriter, r *http.Request, user *store.User, sv *store.Server) {
	path := r.URL.Query().Get("path")
	target, err := fileops.ResolveDownload(serverDataRoot(sv), path)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename=%q`, filepath.Base(target)))
	http.ServeFile(w, r, target)
}
