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
	Status        string
	MemoryMB      int
	HasOwner      bool
	OwnerUsername string
	URL           string
}

type dashboardData struct {
	Common
	Servers []serverRow
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
	for _, sv := range servers {
		row := serverRow{
			ID:       sv.ID,
			Name:     sv.Name,
			Image:    sv.Image,
			Status:   s.docker.ContainerStatus(context.Background(), sv.ContainerID),
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

	data := dashboardData{Common: s.commonFor(w, r, user), Servers: rows}
	s.render(w, r, user, "Servers", "dashboard", "dashboard", data)
}

func serverURL(id int64) string {
	return "/server/" + itoa(id)
}
