# VPS Panel

A small Pterodactyl-style web panel for managing game/app servers that run
as **Docker containers on this VPS**. Lets you (admin) provision containers
and hand customers a login where they can only see and control their own
server: power controls, live console, file manager, and backups.

This is unrelated to the minikube/Kubernetes stack described in the repo's
`CLAUDE.md` — it's meant to run on a separate VPS that hosts customer
containers directly via the Docker Engine.

## How it works

- Each "server" in the panel is one Docker container, created with a bind
  mount at `/data` pointing at `data/servers/<slug>/` on the host. The file
  manager and backups operate on that host directory directly.
- Containers are created with `stdin_open=True, tty=True` so the panel can
  attach to the container's stdin and send console commands, and stream its
  logs/stdout back as the "console" (same idea as `docker attach`).
- Auth is username/password (signed session cookies, bcrypt-hashed
  passwords). Customers only ever see servers assigned to them (`owner_id`);
  admins see everything and get a "New Server" / "Users" section.
- Implemented in Go, compiled to a single static binary (`panel`) with no
  runtime dependencies beyond Docker itself — `wbdash install` builds it in
  place. Go's native concurrency handles many simultaneous console/SSE
  connections without the tuning a scripting-language dev server needs.

## ⚠️ Security notes — read before exposing this to the internet

- **Whatever OS user runs this app needs access to the Docker socket**
  (`docker` group membership, or root). That is root-equivalent power on the
  host. Treat this app's account, its `SECRET_KEY`, and its session cookies
  with the same care as root credentials. Don't run it as an unnecessarily
  privileged user beyond what's needed for the docker group.
- Only admin accounts can create/delete servers/containers or manage users.
  Customer accounts are scoped to their own container's power actions,
  console, files (under their server's `/data` dir only), and backups.
- The file manager resolves every path through `fileops.Join()`, which
  rejects anything that escapes the server's data directory — but it's still
  new code; if you extend it, keep all filesystem access going through that
  function.
- Run this behind a reverse proxy (Caddy/nginx) terminating TLS, and set
  `PANEL_FORCE_SSL=1` so session cookies get `Secure`.
- The Docker images a server can be created from are restricted to an
  allowlist (`PANEL_ALLOWED_IMAGES`, see below) — this stops arbitrary image
  names being launched on your host from the admin form.

## Setup (recommended: the `wbdash` installer)

The repo is at `https://github.com/KingmartinV5/vps-panel` (**private**). To
`git clone` it on a customer VPS, you need a token — GitHub doesn't accept
plain username/password over HTTPS anymore:

1. Generate a fine-grained PAT at
   https://github.com/settings/personal-access-tokens/new, scoped to
   **only** the `vps-panel` repo, with **Contents: Read-only** permission.
   That's the minimum needed to clone — if it ever leaks, the blast radius
   is "someone can read this repo," nothing else.
2. Clone with it embedded in the URL:

```bash
git clone https://<TOKEN>@github.com/KingmartinV5/vps-panel.git vps-panel
cd vps-panel
```

(The token then sits in that box's `vps-panel/.git/config` — fine for an
admin-controlled customer VPS, but don't commit that file or paste it
anywhere public. Revoke + regenerate the token if a VPS is ever compromised.)

Once cloned, `wbdash` bootstraps everything else — it doesn't need Python,
pip, or Docker preinstalled. It figures out the distro's package manager
(apt/dnf/yum/pacman/apk/zypper) and installs whatever's missing, including
Docker itself if it's not there.

```bash
sudo ./wbdash install      # one-time: installs system deps + venv + symlinks
                            # itself to /usr/local/bin/wbdash
wbdash                      # starts the panel in the foreground
```

From then on `wbdash` is just a command, from any directory:

```bash
wbdash start --host 0.0.0.0 --port 8080 --daemon   # background, remembers host/port
wbdash status
wbdash stop
wbdash manage create-user alice                     # forwards to manage.py
wbdash help
```

`sudo wbdash install -y` skips the interactive "install Docker?" prompt, for
unattended/scripted installs.

On first run it auto-creates an `admin` account with a random password
printed to the console (and saved in `panel.log` if run with `--daemon`) —
copy it down, log in, and create real accounts (`Users` in the sidebar, or
`wbdash manage create-user <name>` / `create-admin <name>`).

### Demo mode (for screenshots/marketing)

```bash
wbdash --debug   # or: wbdash debug
```

Asks for a password (`1408`), then:
- seeds a few fake servers (`panel demo-seed`) into a **throwaway demo
  database** (`instance/demo.db`, `data/demo-servers/`) — completely
  separate from real customer data, safe to run on a box with real
  customers on it
- starts the panel on `0.0.0.0:7114` (LAN-reachable, in case the tunnel is flaky)
- downloads a `cloudflared` static binary the first time (into `bin/`, not
  installed system-wide) and opens a free Cloudflare **quick tunnel**, so
  you get a fresh public `https://*.trycloudflare.com` link every run

Log in on that link as `demo-admin` / `demo` (both password `demo1408`) to
see the admin and customer views. Ctrl+C closes the tunnel link; the demo
panel process itself keeps running in the background so the next `--debug`
run is instant (no reseeding) — `wbdash debug stop` shuts it down fully.

The tunnel URL is unguessable but public for as long as it's open, so don't
leave it running unattended longer than it takes to grab screenshots.

### Manual setup (no installer)

If you'd rather build it yourself:

```bash
cd vps-panel
CGO_ENABLED=0 go build -o panel .
docker ps   # should not error / require sudo
./panel serve
```

## Configuration (environment variables)

| Variable | Default | Purpose |
|---|---|---|
| `PANEL_SECRET_KEY` | auto-generated, persisted in `instance/secret_key` | session signing key |
| `PANEL_DATABASE_URL` | sqlite in `instance/panel.db` | database URI |
| `PANEL_SERVERS_ROOT` | `data/servers` | where each server's `/data` bind mount lives |
| `PANEL_BACKUPS_ROOT` | `data/backups` | where `.tar.gz` backups are written |
| `PANEL_ALLOWED_IMAGES` | `itzg/minecraft-server,itzg/minecraft-bedrock-server,itzg/mc-proxy` | comma-separated image allowlist for server creation |
| `PANEL_MAX_UPLOAD_MB` | `250` | max file manager upload size |
| `PANEL_MAX_EDIT_MB` | `2` | max file size editable in the browser (bigger files: download only) |
| `PANEL_FORCE_SSL` | `0` | set `1` once served over HTTPS, to mark cookies `Secure` |

## Running in production

`wbdash start --daemon` is fine for quick/LAN use, but it's still a `nohup`
wrapper — no auto-restart on crash or reboot. For anything internet-facing,
run the binary directly under a systemd unit (below) and put Caddy/nginx in
front for TLS.

The panel's `net/http` server handles many concurrent long-lived connections
(the console uses Server-Sent Events) natively — no extra process manager or
worker-count tuning needed, unlike a scripting-language dev server.

Example systemd unit:

```ini
[Unit]
Description=VPS Panel
After=network.target docker.service

[Service]
User=panel
Group=docker
WorkingDirectory=/opt/vps-panel
ExecStart=/opt/vps-panel/panel serve
Restart=on-failure
Environment=PANEL_FORCE_SSL=1

[Install]
WantedBy=multi-user.target
```

(`User=panel` in the `docker` group — not root — is the minimum privilege
that still allows Docker socket access.)

## What's not included (roadmap ideas, not built)

- Billing/subscription integration
- Per-customer CPU quota beyond the container's own `mem_limit`
  (add `nano_cpus`/`cpu_quota` in `docker_manager.create_container` if you
  want hard CPU caps too)
- Multi-server-per-container-image templates / one-click installers
- Audit log of admin actions
