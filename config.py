import os
import secrets
from pathlib import Path

BASE_DIR = Path(__file__).resolve().parent
INSTANCE_DIR = BASE_DIR / "instance"
INSTANCE_DIR.mkdir(exist_ok=True)


def _secret_key():
    env_key = os.environ.get("PANEL_SECRET_KEY")
    if env_key:
        return env_key
    # Persist a generated key across restarts instead of rotating it every run
    # (rotating it would log every logged-in user out on every restart).
    key_file = INSTANCE_DIR / "secret_key"
    if key_file.exists():
        return key_file.read_text().strip()
    key = secrets.token_hex(32)
    key_file.write_text(key)
    key_file.chmod(0o600)
    return key


class Config:
    SECRET_KEY = _secret_key()

    SQLALCHEMY_DATABASE_URI = os.environ.get(
        "PANEL_DATABASE_URL", f"sqlite:///{INSTANCE_DIR / 'panel.db'}"
    )
    SQLALCHEMY_TRACK_MODIFICATIONS = False

    SERVERS_ROOT = Path(os.environ.get("PANEL_SERVERS_ROOT", BASE_DIR / "data" / "servers"))
    BACKUPS_ROOT = Path(os.environ.get("PANEL_BACKUPS_ROOT", BASE_DIR / "data" / "backups"))

    # Only these images can be used when creating a server from the admin panel.
    # Keeps customers (and admin typos) from launching arbitrary images on the host.
    DOCKER_ALLOWED_IMAGES = [
        img.strip()
        for img in os.environ.get(
            "PANEL_ALLOWED_IMAGES",
            "itzg/minecraft-server,itzg/minecraft-bedrock-server,itzg/mc-proxy",
        ).split(",")
        if img.strip()
    ]

    MAX_UPLOAD_MB = int(os.environ.get("PANEL_MAX_UPLOAD_MB", "250"))
    MAX_EDIT_MB = int(os.environ.get("PANEL_MAX_EDIT_MB", "2"))
    MAX_CONTENT_LENGTH = MAX_UPLOAD_MB * 1024 * 1024

    SESSION_COOKIE_HTTPONLY = True
    SESSION_COOKIE_SAMESITE = "Lax"
    # Set PANEL_FORCE_SSL=1 once the panel is served over HTTPS (recommended: always).
    SESSION_COOKIE_SECURE = os.environ.get("PANEL_FORCE_SSL", "0") == "1"
