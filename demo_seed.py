"""
Seeds fake-but-realistic demo data for screenshots: a couple of demo users
and a few lightweight containers standing in for game servers, each printing
believable console output in a loop. Everything lives in whatever
PANEL_DATABASE_URL / PANEL_SERVERS_ROOT / PANEL_BACKUPS_ROOT are set to by
the caller (wbdash points these at a separate demo.db / demo-servers /
demo-backups, so this never touches real customer data).

Idempotent: re-running it just makes sure the demo containers are up.
"""
import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))

import docker_manager  # noqa: E402
import fileops  # noqa: E402
from app import app, db  # noqa: E402
from config import Config  # noqa: E402
from models import Server, User  # noqa: E402

DEMO_PASSWORD = "demo1408"

FAKE_FILES = {
    "eula.txt": (
        "#By changing the setting below to TRUE you are indicating your agreement to our EULA\n"
        "eula=true\n"
    ),
    "server.properties": (
        "server-port=25565\nlevel-name=world\ngamemode=survival\ndifficulty=normal\n"
        "max-players=20\nmotd=A Minecraft Server\nonline-mode=true\nview-distance=10\n"
    ),
    "whitelist.json": "[]\n",
    "ops.json": "[]\n",
    "banned-players.json": "[]\n",
    "logs/latest.log": '[18:02:14] [Server thread/INFO]: Done (2.841s)! For help, type "help"\n',
    "world/level.dat": "",
    "world/session.lock": "",
    "world_nether/level.dat": "",
    "plugins/EssentialsX.jar": "",
    "plugins/WorldEdit.jar": "",
    "plugins/Vault.jar": "",
    "plugins/LuckPerms.jar": "",
}

CONSOLE_LOOP_TEMPLATE = r"""
echo '[18:02:11] [Server thread/INFO]: Starting minecraft server version 1.21.1'
echo '[18:02:11] [Server thread/INFO]: Loading properties'
echo '[18:02:11] [Server thread/INFO]: Default game type: SURVIVAL'
sleep 1
echo '[18:02:12] [Server thread/INFO]: Preparing level "{world}"'
sleep 1
echo '[18:02:14] [Server thread/INFO]: Done (2.841s)! For help, type "help"'
i=0
names="Steve Alex Herobrine Notch KingMartin Builder99 CraftyCat Nova"
while true; do
  i=$((i + 1))
  n=$(echo $names | cut -d' ' -f$(( (i % 8) + 1 )))
  case $((i % 6)) in
    0) echo "[Server thread/INFO]: $n joined the game" ;;
    1) echo "[Server thread/INFO]: $n left the game" ;;
    2) echo "[Server thread/INFO]: <$n> {chat}" ;;
    3) echo "[Server thread/INFO]: $n fell from a high place" ;;
    4) echo "[Server thread/INFO]: Saved the game" ;;
    5) echo "[Server thread/INFO]: $n moved too quickly!" ;;
  esac
  sleep $((3 + (i % 4)))
done
"""

DEMO_SERVERS = [
    {
        "name": "Survival SMP",
        "running": True,
        "script": CONSOLE_LOOP_TEMPLATE.format(world="world", chat="anyone got spare diamonds?"),
    },
    {
        "name": "Creative Build",
        "running": True,
        "script": CONSOLE_LOOP_TEMPLATE.format(world="creative_world", chat="check out my build!"),
    },
    {
        "name": "Skyblock",
        "running": False,
        "script": CONSOLE_LOOP_TEMPLATE.format(world="skyblock", chat="need more cobble"),
    },
]


def seed_files(data_dir: Path):
    for rel_path, content in FAKE_FILES.items():
        target = data_dir / rel_path
        target.parent.mkdir(parents=True, exist_ok=True)
        target.write_text(content)


def main():
    with app.app_context():
        db.create_all()

        demo_admin = User.query.filter_by(username="demo-admin").first()
        if not demo_admin:
            demo_admin = User(username="demo-admin", is_admin=True)
            demo_admin.set_password(DEMO_PASSWORD)
            db.session.add(demo_admin)

        demo_customer = User.query.filter_by(username="demo").first()
        if not demo_customer:
            demo_customer = User(username="demo", is_admin=False)
            demo_customer.set_password(DEMO_PASSWORD)
            db.session.add(demo_customer)
        db.session.commit()

        if Server.query.count() > 0:
            print("Demo already seeded -- making sure containers are up...")
            for s in Server.query.all():
                try:
                    docker_manager.get_container(s.container_id).start()
                except Exception:
                    pass
            print(f"Log in as 'demo-admin' or 'demo', password: {DEMO_PASSWORD}")
            return

        client = docker_manager.get_client()
        try:
            client.images.pull("alpine", tag="latest")
        except Exception as exc:
            print(f"warning: could not pull alpine:latest ({exc}), continuing with local image if present")

        Config.SERVERS_ROOT.mkdir(parents=True, exist_ok=True)
        Config.BACKUPS_ROOT.mkdir(parents=True, exist_ok=True)

        created = []
        for spec in DEMO_SERVERS:
            slug = docker_manager.slugify(spec["name"])
            data_dir = Config.SERVERS_ROOT / slug
            data_dir.mkdir(parents=True, exist_ok=True)
            seed_files(data_dir)

            container_name = f"panel-demo-{slug}"
            try:
                client.containers.get(container_name).remove(force=True)
            except docker_manager.NotFound:
                pass

            container = client.containers.run(
                "alpine:latest",
                name=container_name,
                command=["/bin/sh", "-c", spec["script"]],
                detach=True,
                stdin_open=True,
                tty=True,
                mem_limit="64m",
                volumes={str(data_dir): {"bind": "/data", "mode": "rw"}},
                restart_policy={"Name": "unless-stopped"},
            )
            if not spec["running"]:
                container.stop()

            server = Server(
                name=spec["name"],
                slug=slug,
                image="alpine:latest (demo)",
                container_id=container.id,
                container_name=container_name,
                data_path=str(data_dir),
                memory_mb=64,
                port_mappings="",
                owner_id=demo_customer.id,
            )
            db.session.add(server)
            created.append((slug, data_dir))

        db.session.commit()

        if created:
            first_slug, first_dir = created[0]
            try:
                fileops.create_backup(Config.BACKUPS_ROOT, first_slug, first_dir)
            except Exception as exc:
                print(f"warning: could not create demo backup ({exc})")

        print(f"Demo seeded. Log in as 'demo-admin' or 'demo', password: {DEMO_PASSWORD}")


if __name__ == "__main__":
    main()
