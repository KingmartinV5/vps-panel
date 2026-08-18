package web

import (
	"context"
	"net/http"

	"vps-panel/internal/store"
)

type serverRow struct {
	ID            int64
	Name          string
	Image         string
	TypeIcon      string
	Status        string
	MemoryMB      int
	HasOwner      bool
	OwnerUsername string
	URL           string
}

type dashboardData struct {
	Common
	Servers      []serverRow
	RunningCount int
}

// dashboardTypeIcon maps a server's image to the same badge icon used on the
// admin type picker (see knownServerTypes in admin.go), so the dashboard and
// the create-server form read as one consistent icon language. Falls back to
// no icon for images an admin has added beyond the three built-in ones.
func dashboardTypeIcon(image string) string {
	switch image {
	case "itzg/minecraft-server":
		return "type-java.svg"
	case "itzg/minecraft-bedrock-server":
		return "type-bedrock.svg"
	case "itzg/mc-proxy":
		return "type-proxy.svg"
	default:
		return ""
	}
}

func (s *Server) handleDashboard(w http.ResponseWriter, r *http.Request, user *store.User) {
	var servers []*store.Server
	var err error
	if user.IsAdmin {
		servers, err = s.store.ListServers()
	} else {
		servers, err = s.store.ListServersByOwner(user.ID)
	}
	if err != nil {
		s.serverError(w, "list servers", err)
		return
	}

	rows := make([]serverRow, 0, len(servers))
	running := 0
	for _, sv := range servers {
		status := s.docker.ContainerStatus(context.Background(), sv.ContainerID)
		if status == "running" {
			running++
		}
		row := serverRow{
			ID:       sv.ID,
			Name:     sv.Name,
			Image:    sv.Image,
			TypeIcon: dashboardTypeIcon(sv.Image),
			Status:   status,
			MemoryMB: sv.MemoryMB,
			URL:      serverURL(sv.ID),
		}
		if user.IsAdmin && sv.OwnerID.Valid {
			if owner, err := s.store.GetUser(sv.OwnerID.Int64); err == nil {
				row.HasOwner = true
				row.OwnerUsername = owner.Username
			}
		}
		rows = append(rows, row)
	}

	data := dashboardData{Common: s.commonFor(w, r, user), Servers: rows, RunningCount: running}
	s.render(w, r, user, "Servers", "dashboard", "dashboard", data)
}

func serverURL(id int64) string {
	return "/server/" + itoa(id)
}
