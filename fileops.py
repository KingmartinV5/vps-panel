"""
File manager + backup helpers. Every function here takes a `root` (a server's
data directory, controlled by us, never by the caller) and a `rel_path`
(user-supplied). safe_join() is the single chokepoint that prevents path
traversal -- nothing in this file should touch the filesystem with a path
that didn't come out of it.
"""
import datetime
import os
import shutil
import tarfile
from pathlib import Path

from werkzeug.utils import secure_filename


class PathSecurityError(Exception):
    pass


def safe_join(root: Path, rel_path: str) -> Path:
    root = root.resolve()
    rel_path = (rel_path or "").lstrip("/")
    candidate = (root / rel_path).resolve()
    try:
        candidate.relative_to(root)
    except ValueError:
        raise PathSecurityError(f"Path escapes server data directory: {rel_path!r}")
    return candidate


def list_dir(root: Path, rel_path: str):
    target = safe_join(root, rel_path)
    if not target.exists() or not target.is_dir():
        raise FileNotFoundError(rel_path)
    entries = []
    for entry in sorted(target.iterdir(), key=lambda p: (p.is_file(), p.name.lower())):
        try:
            stat = entry.stat()
            entries.append(
                {
                    "name": entry.name,
                    "is_dir": entry.is_dir(),
                    "size": stat.st_size if entry.is_file() else None,
                    "mtime": datetime.datetime.fromtimestamp(stat.st_mtime),
                }
            )
        except OSError:
            continue
    return entries


def read_text_file(root: Path, rel_path: str, max_bytes: int):
    target = safe_join(root, rel_path)
    if not target.is_file():
        raise FileNotFoundError(rel_path)
    if target.stat().st_size > max_bytes:
        raise ValueError("File too large to edit in the browser")
    return target.read_text(encoding="utf-8", errors="replace")


def write_text_file(root: Path, rel_path: str, content: str):
    target = safe_join(root, rel_path)
    target.parent.mkdir(parents=True, exist_ok=True)
    target.write_text(content, encoding="utf-8")


def save_upload(root: Path, rel_dir: str, file_storage):
    filename = secure_filename(file_storage.filename)
    if not filename:
        raise ValueError("Invalid filename")
    target_dir = safe_join(root, rel_dir)
    target_dir.mkdir(parents=True, exist_ok=True)
    target = safe_join(root, os.path.join(rel_dir, filename))
    file_storage.save(target)
    return target


def delete_path(root: Path, rel_path: str):
    target = safe_join(root, rel_path)
    if target == root.resolve():
        raise PathSecurityError("Refusing to delete the server's root data directory")
    if target.is_dir():
        shutil.rmtree(target)
    elif target.exists():
        target.unlink()


def make_dir(root: Path, rel_path: str):
    target = safe_join(root, rel_path)
    target.mkdir(parents=True, exist_ok=True)


def resolve_download(root: Path, rel_path: str) -> Path:
    target = safe_join(root, rel_path)
    if not target.is_file():
        raise FileNotFoundError(rel_path)
    return target


# --- Backups -----------------------------------------------------------

def create_backup(backups_root: Path, server_slug: str, data_dir: Path) -> Path:
    dest_dir = backups_root / server_slug
    dest_dir.mkdir(parents=True, exist_ok=True)
    timestamp = datetime.datetime.utcnow().strftime("%Y%m%d-%H%M%S")
    archive_path = dest_dir / f"{server_slug}-{timestamp}.tar.gz"
    with tarfile.open(archive_path, "w:gz") as tar:
        tar.add(data_dir, arcname=".")
    return archive_path


def list_backups(backups_root: Path, server_slug: str):
    dest_dir = backups_root / server_slug
    if not dest_dir.exists():
        return []
    backups = []
    for entry in sorted(dest_dir.glob("*.tar.gz"), reverse=True):
        stat = entry.stat()
        backups.append(
            {
                "filename": entry.name,
                "size_mb": round(stat.st_size / (1024 * 1024), 2),
                "mtime": datetime.datetime.fromtimestamp(stat.st_mtime),
            }
        )
    return backups


def resolve_backup(backups_root: Path, server_slug: str, filename: str) -> Path:
    filename = secure_filename(filename)
    target = safe_join(backups_root / server_slug, filename)
    if not target.is_file():
        raise FileNotFoundError(filename)
    return target


def delete_backup(backups_root: Path, server_slug: str, filename: str):
    target = resolve_backup(backups_root, server_slug, filename)
    target.unlink()
