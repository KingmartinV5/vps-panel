// Command vps-panel is a small Pterodactyl-style web panel for managing
// customer game/app servers running as Docker containers. See README.md.
package main

import (
	"embed"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"

	"vps-panel/internal/auth"
	"vps-panel/internal/config"
	"vps-panel/internal/dockermgr"
	"vps-panel/internal/store"
	"vps-panel/internal/web"
)

//go:embed templates/*.html
var templatesFS embed.FS

//go:embed static
var staticEmbed embed.FS

func baseDir() string {
	exe, err := os.Executable()
	if err != nil {
		wd, _ := os.Getwd()
		return wd
	}
	resolved, err := filepath.EvalSymlinks(exe)
	if err != nil {
		resolved = exe
	}
	return filepath.Dir(resolved)
}

func main() {
	args := os.Args[1:]
	cmd := "serve"
	if len(args) > 0 && args[0] != "" && args[0][0] != '-' {
		cmd = args[0]
		args = args[1:]
	}

	switch cmd {
	case "serve":
		runServe()
	case "manage":
		runManage(args)
	case "demo-seed":
		runDemoSeed()
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n", cmd)
		fmt.Fprintln(os.Stderr, "usage: panel [serve|manage <args>|demo-seed]")
		os.Exit(1)
	}
}

func openDeps() (*config.Config, *store.Store, *dockermgr.Manager, *auth.Manager) {
	cfg, err := config.Load(baseDir())
	if err != nil {
		log.Fatalf("config: %v", err)
	}
	st, err := store.Open(cfg.DatabaseURL)
	if err != nil {
		log.Fatalf("database: %v", err)
	}
	docker := dockermgr.New()
	authMgr := auth.NewManager(cfg.SecretKey, cfg.ForceSSL)
	return cfg, st, docker, authMgr
}

func runServe() {
	cfg, st, docker, authMgr := openDeps()
	defer st.Close()
	if err := st.BootstrapAdmin(); err != nil {
		log.Fatalf("bootstrap admin: %v", err)
	}

	staticSub, err := fs.Sub(staticEmbed, "static")
	if err != nil {
		log.Fatalf("static assets: %v", err)
	}
	templatesSub, err := fs.Sub(templatesFS, "templates")
	if err != nil {
		log.Fatalf("templates: %v", err)
	}

	srv, err := web.New(cfg, st, docker, authMgr, templatesSub, staticSub)
	if err != nil {
		log.Fatalf("web server: %v", err)
	}

	host := os.Getenv("PANEL_HOST")
	if host == "" {
		host = "127.0.0.1"
	}
	port := os.Getenv("PANEL_PORT")
	if port == "" {
		port = "5000"
	}
	addr := host + ":" + port

	log.Printf("listening on http://%s", addr)
	if err := http.ListenAndServe(addr, srv); err != nil {
		log.Fatalf("server error: %v", err)
	}
}
