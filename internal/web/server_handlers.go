package web

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"vps-panel/internal/store"
)

type serverDetailData struct {
	Common
	Server *store.Server
	Status string
}

func (s *Server) handleServerDetail(w http.ResponseWriter, r *http.Request, user *store.User, sv *store.Server) {
	status := s.docker.ContainerStatus(r.Context(), sv.ContainerID)
	data := serverDetailData{Common: s.commonFor(w, r, user), Server: sv, Status: status}
	s.render(w, r, user, sv.Name, "", "server", data)
}

func (s *Server) handleServerPower(w http.ResponseWriter, r *http.Request, user *store.User, sv *store.Server) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	if !s.checkCSRF(w, r) {
		return
	}
	action := r.FormValue("action")
	switch action {
	case "start", "stop", "restart", "kill":
	default:
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	if err := s.docker.PowerAction(r.Context(), sv.ContainerID, action); err != nil {
		s.flash(w, r, "error", "Power action failed: "+err.Error())
	} else {
		if action == "stop" || action == "restart" || action == "kill" {
			s.docker.DropAttachSocket(sv.ContainerID)
		}
		s.flash(w, r, "success", "Server "+action+" requested")
	}
	http.Redirect(w, r, serverURL(sv.ID), http.StatusFound)
}

func (s *Server) handleServerStats(w http.ResponseWriter, r *http.Request, user *store.User, sv *store.Server) {
	w.Header().Set("Content-Type", "application/json")
	stats, err := s.docker.GetStats(r.Context(), sv.ContainerID)
	if err != nil {
		json.NewEncoder(w).Encode(map[string]string{"status": "unavailable"})
		return
	}
	json.NewEncoder(w).Encode(map[string]any{
		"status":       stats.Status,
		"cpu_percent":  stats.CPUPercent,
		"mem_usage_mb": stats.MemUsageMB,
		"mem_limit_mb": stats.MemLimitMB,
		"mem_percent":  stats.MemPercent,
		"net_rx_mb":    stats.NetRxMB,
		"net_tx_mb":    stats.NetTxMB,
	})
}

func (s *Server) handleConsoleStream(w http.ResponseWriter, r *http.Request, user *store.User, sv *store.Server) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	err := s.docker.StreamLogs(ctx, sv.ContainerID, func(line string) {
		w.Write([]byte("data: " + line + "\n\n"))
		flusher.Flush()
	})
	if err != nil {
		w.Write([]byte("data: [console stream ended: " + err.Error() + "]\n\n"))
		flusher.Flush()
	}
}

func (s *Server) handleConsoleSend(w http.ResponseWriter, r *http.Request, user *store.User, sv *store.Server) {
	w.Header().Set("Content-Type", "application/json")
	if err := r.ParseForm(); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": "bad form"})
		return
	}
	if !s.auth.CheckCSRF(r) {
		w.WriteHeader(http.StatusForbidden)
		json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": "CSRF token missing or invalid"})
		return
	}
	command := r.FormValue("command")
	if strings.TrimSpace(command) == "" {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": "empty command"})
		return
	}
	if err := s.docker.SendCommand(r.Context(), sv.ContainerID, command); err != nil {
		w.WriteHeader(http.StatusBadGateway)
		json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": err.Error()})
		return
	}
	json.NewEncoder(w).Encode(map[string]any{"ok": true})
}

