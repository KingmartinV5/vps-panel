package main

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"

	"vps-panel/internal/dockermgr"
	"vps-panel/internal/fileops"
	"vps-panel/internal/store"
)

const demoPassword = "demo1408"

var demoFakeFiles = map[string]string{
	"eula.txt": "#By changing the setting below to TRUE you are indicating your agreement to our EULA\n" +
		"eula=true\n",
	"server.properties": "server-port=25565\nlevel-name=world\ngamemode=survival\ndifficulty=normal\n" +
		"max-players=20\nmotd=A Minecraft Server\nonline-mode=true\nview-distance=10\n",
	"whitelist.json":          "[]\n",
	"ops.json":                "[]\n",
	"banned-players.json":     "[]\n",
	"logs/latest.log":         "[18:02:14] [Server thread/INFO]: Done (2.841s)! For help, type \"help\"\n",
	"world/level.dat":         "",
	"world/session.lock":      "",
	"world_nether/level.dat":  "",
	"plugins/EssentialsX.jar": "",
	"plugins/WorldEdit.jar":   "",
	"plugins/Vault.jar":       "",
	"plugins/LuckPerms.jar":   "",
}

const demoConsoleLoopTemplate = `
echo '[18:02:11] [Server thread/INFO]: Starting minecraft server version 1.21.1'
echo '[18:02:11] [Server thread/INFO]: Loading properties'
echo '[18:02:11] [Server thread/INFO]: Default game type: SURVIVAL'
sleep 1
echo '[18:02:12] [Server thread/INFO]: Preparing level "%s"'
sleep 1
echo '[18:02:14] [Server thread/INFO]: Done (2.841s)! For help, type "help"'
i=0
names="Steve Alex Herobrine Notch KingMartin Builder99 CraftyCat Nova"
while true; do
  i=$((i + 1))
  n=$(echo $names | cut -d' ' -f$(( (i %% 8) + 1 )))
  case $((i %% 6)) in
    0) echo "[Server thread/INFO]: $n joined the game" ;;
    1) echo "[Server thread/INFO]: $n left the game" ;;
    2) echo "[Server thread/INFO]: <$n> %s" ;;
    3) echo "[Server thread/INFO]: $n fell from a high place" ;;
    4) echo "[Server thread/INFO]: Saved the game" ;;
    5) echo "[Server thread/INFO]: $n moved too quickly!" ;;
  esac
  sleep $((3 + (i %% 4)))
done
`

type demoServerSpec struct {
	name    string
	running bool
	world   string
	chat    string
}

var demoServerSpecs = []demoServerSpec{
	{name: "Survival SMP", running: true, world: "world", chat: "anyone got spare diamonds?"},
	{name: "Creative Build", running: true, world: "creative_world", chat: "check out my build!"},
	{name: "Skyblock", running: false, world: "skyblock", chat: "need more cobble"},
}

func seedDemoFiles(dataDir string) error {
	for rel, content := range demoFakeFiles {
		target := filepath.Join(dataDir, rel)
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(target, []byte(content), 0o644); err != nil {
			return err
		}
	}
	return nil
}

// runDemoSeed ports demo_seed.py: idempotent seeding of fake demo users and
// lightweight alpine containers that print believable console output, kept
// entirely separate from real customer data via the PANEL_DATABASE_URL /
// PANEL_SERVERS_ROOT / PANEL_BACKUPS_ROOT env vars wbdash points at the demo
// instance before invoking this.
func runDemoSeed() {
	cfg, st, docker, _ := openDeps()
	defer st.Close()
	ctx := context.Background()

	demoAdmin, err := st.GetUserByUsername("demo-admin")
	if err != nil {
		demoAdmin = &store.User{Username: "demo-admin", IsAdmin: true}
		_ = demoAdmin.SetPassword(demoPassword)
		if _, err := st.CreateUser(demoAdmin); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	}
	demoCustomer, err := st.GetUserByUsername("demo")
	if err != nil {
		demoCustomer = &store.User{Username: "demo", IsAdmin: false}
		_ = demoCustomer.SetPassword(demoPassword)
		if _, err := st.CreateUser(demoCustomer); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	}

	existing, err := st.ListServers()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if len(existing) > 0 {
		fmt.Println("Demo already seeded -- making sure containers are up...")
		for _, sv := range existing {
			_ = docker.PowerAction(ctx, sv.ContainerID, "start")
		}
		fmt.Printf("Log in as 'demo-admin' or 'demo', password: %s\n", demoPassword)
		return
	}

	if err := docker.PullImage(ctx, "alpine:latest"); err != nil {
		fmt.Printf("warning: could not pull alpine:latest (%v), continuing with local image if present\n", err)
	}

	if err := os.MkdirAll(cfg.ServersRoot, 0o755); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := os.MkdirAll(cfg.BackupsRoot, 0o755); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	type created struct {
		slug    string
		dataDir string
	}
	var madeServers []created

	for _, spec := range demoServerSpecs {
		slug := dockermgr.Slugify(spec.name)
		dataDir := filepath.Join(cfg.ServersRoot, slug)
		if err := os.MkdirAll(dataDir, 0o755); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		if err := seedDemoFiles(dataDir); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}

		containerName := "panel-demo-" + slug
		_ = docker.RemoveContainer(ctx, containerName)

		script := fmt.Sprintf(demoConsoleLoopTemplate, spec.world, spec.chat)
		containerID, err := docker.CreateContainerWithCommand(ctx, containerName, "alpine:latest", dataDir, 64, []string{"/bin/sh", "-c", script})
		if err != nil {
			fmt.Fprintf(os.Stderr, "failed to create demo container %s: %v\n", containerName, err)
			os.Exit(1)
		}
		if !spec.running {
			_ = docker.PowerAction(ctx, containerID, "stop")
		}

		sv := &store.Server{
			Name: spec.name, Slug: slug, Image: "alpine:latest (demo)",
			ContainerID: containerID, ContainerName: containerName,
			DataPath: dataDir, MemoryMB: 64, PortMappings: "",
			OwnerID: sql.NullInt64{Int64: demoCustomer.ID, Valid: true},
		}
		if _, err := st.CreateServer(sv); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		madeServers = append(madeServers, created{slug: slug, dataDir: dataDir})
	}

	if len(madeServers) > 0 {
		first := madeServers[0]
		if _, err := fileops.CreateBackup(cfg.BackupsRoot, first.slug, first.dataDir); err != nil {
			fmt.Printf("warning: could not create demo backup (%v)\n", err)
		}
	}

	fmt.Printf("Demo seeded. Log in as 'demo-admin' or 'demo', password: %s\n", demoPassword)
}
