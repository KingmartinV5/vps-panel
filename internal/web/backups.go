package web

import (
	"fmt"
	"net/http"

	"vps-panel/internal/fileops"
	"vps-panel/internal/store"
)

type backupRow struct {
	Filename  string
	SizeMBStr string
	MTimeStr  string
}

type backupsData struct {
	Common
	Server  *store.Server
	Backups []backupRow
}

func (s *Server) handleBackupsList(w http.ResponseWriter, r *http.Request, user *store.User, sv *store.Server) {
	backups, err := fileops.ListBackups(s.cfg.BackupsRoot, sv.Slug)
	if err != nil {
		s.serverError(w, "list backups", err)
		return
	}
	rows := make([]backupRow, 0, len(backups))
	for _, b := range backups {
		rows = append(rows, backupRow{
			Filename:  b.Filename,
			SizeMBStr: fmt.Sprintf("%.2f", b.SizeMB),
			MTimeStr:  b.MTime.Format("2006-01-02 15:04"),
		})
	}
	data := backupsData{Common: s.commonFor(w, r, user), Server: sv, Backups: rows}
	s.render(w, r, user, sv.Name+" · Backups", "", "backups", data)
}

func (s *Server) handleBackupsCreate(w http.ResponseWriter, r *http.Request, user *store.User, sv *store.Server) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	if !s.checkCSRF(w, r) {
		return
	}
	if _, err := fileops.CreateBackup(s.cfg.BackupsRoot, sv.Slug, serverDataRoot(sv)); err != nil {
		s.flash(w, r, "error", "Backup failed: "+err.Error())
	} else {
		s.flash(w, r, "success", "Backup created")
	}
	http.Redirect(w, r, "/server/"+itoa(sv.ID)+"/backups", http.StatusFound)
}

func (s *Server) handleBackupsDownload(w http.ResponseWriter, r *http.Request, user *store.User, sv *store.Server) {
	filename := r.PathValue("filename")
	target, err := fileops.ResolveBackup(s.cfg.BackupsRoot, sv.Slug, filename)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename=%q`, filename))
	http.ServeFile(w, r, target)
}

func (s *Server) handleBackupsDelete(w http.ResponseWriter, r *http.Request, user *store.User, sv *store.Server) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	if !s.checkCSRF(w, r) {
		return
	}
	filename := r.PathValue("filename")
	if err := fileops.DeleteBackup(s.cfg.BackupsRoot, sv.Slug, filename); err != nil {
		s.flash(w, r, "error", "Backup not found")
	} else {
		s.flash(w, r, "success", "Backup deleted")
	}
	http.Redirect(w, r, "/server/"+itoa(sv.ID)+"/backups", http.StatusFound)
}
