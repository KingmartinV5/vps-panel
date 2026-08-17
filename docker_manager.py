"""
Thin wrapper around the Docker SDK for everything the panel needs:
create/start/stop/restart/kill/remove containers, pull stats, tail logs,
and write commands to a container's stdin (for game server consoles).

NOTE: whatever process runs this app needs access to the Docker socket
(usually membership in the `docker` group, or root). That is effectively
root-equivalent power on this host, so this app's own account/session
security matters a lot -- see README.md.
"""
import re
import threading

import docker
from docker.errors import APIError, NotFound

_client = None
_client_lock = threading.Lock()

# One attach socket per container_id, kept open for writing console input.
_attach_sockets = {}
_attach_lock = threading.Lock()


def get_client():
    global _client
    if _client is None:
        with _client_lock:
            if _client is None:
                _client = docker.from_env()
    return _client


def slugify(name):
    slug = re.sub(r"[^a-zA-Z0-9_-]+", "-", name.strip().lower()).strip("-")
    return slug or "server"


def parse_port_mappings(raw):
    """
    raw: newline/comma separated "hostport:containerport/proto" entries,
    e.g. "25565:25565/tcp". Returns a docker-py ports dict.
    Raises ValueError on malformed input.
    """
    ports = {}
    if not raw:
        return ports
    entries = [e.strip() for e in re.split(r"[,\n]", raw) if e.strip()]
    for entry in entries:
        m = re.match(r"^(\d{1,5}):(\d{1,5})(/(tcp|udp))?$", entry)
        if not m:
            raise ValueError(f"Invalid port mapping: {entry!r} (expected hostport:containerport[/tcp|udp])")
        host_port, container_port, _, proto = m.groups()
        proto = proto or "tcp"
        for p in (host_port, container_port):
            if not (0 < int(p) < 65536):
                raise ValueError(f"Port out of range in mapping: {entry!r}")
        ports[f"{container_port}/{proto}"] = int(host_port)
    return ports


def parse_env(raw):
    """raw: newline separated KEY=VALUE lines."""
    env = {}
    if not raw:
        return env
    for line in raw.splitlines():
        line = line.strip()
        if not line or line.startswith("#"):
            continue
        if "=" not in line:
            raise ValueError(f"Invalid env line (expected KEY=VALUE): {line!r}")
        key, value = line.split("=", 1)
        key = key.strip()
        if not re.match(r"^[A-Za-z_][A-Za-z0-9_]*$", key):
            raise ValueError(f"Invalid env var name: {key!r}")
        env[key] = value.strip()
    return env


def create_container(container_name, image, data_path, memory_mb, ports, env):
    client = get_client()
    return client.containers.run(
        image,
        name=container_name,
        detach=True,
        stdin_open=True,
        tty=True,
        mem_limit=f"{memory_mb}m",
        volumes={str(data_path): {"bind": "/data", "mode": "rw"}},
        ports=ports,
        environment=env,
        restart_policy={"Name": "unless-stopped"},
    )


def get_container(container_id):
    return get_client().containers.get(container_id)


def container_status(container_id):
    try:
        return get_container(container_id).status
    except NotFound:
        return "missing"


def power_action(container_id, action):
    container = get_container(container_id)
    if action == "start":
        container.start()
    elif action == "stop":
        container.stop(timeout=30)
    elif action == "restart":
        container.restart(timeout=30)
    elif action == "kill":
        container.kill()
    else:
        raise ValueError(f"Unknown power action: {action}")


def remove_container(container_id, force=True):
    try:
        get_container(container_id).remove(force=force)
    except NotFound:
        pass


def stream_logs(container_id, tail=200):
    """Generator yielding decoded log lines as they arrive. Blocks between lines."""
    container = get_container(container_id)
    for chunk in container.logs(stream=True, follow=True, tail=tail):
        yield chunk.decode("utf-8", errors="replace")


def get_stats(container_id):
    container = get_container(container_id)
    raw = container.stats(stream=False)

    cpu_percent = 0.0
    try:
        cpu_delta = raw["cpu_stats"]["cpu_usage"]["total_usage"] - raw["precpu_stats"]["cpu_usage"]["total_usage"]
        system_delta = raw["cpu_stats"]["system_cpu_usage"] - raw["precpu_stats"]["system_cpu_usage"]
        online_cpus = raw["cpu_stats"].get("online_cpus") or len(
            raw["cpu_stats"]["cpu_usage"].get("percpu_usage", [1])
        )
        if system_delta > 0 and cpu_delta >= 0:
            cpu_percent = (cpu_delta / system_delta) * online_cpus * 100.0
    except (KeyError, TypeError, ZeroDivisionError):
        pass

    mem_usage = raw.get("memory_stats", {}).get("usage", 0)
    mem_limit = raw.get("memory_stats", {}).get("limit", 1)
    # cgroup mem accounting includes page cache; subtract it if present for a truer figure
    cache = raw.get("memory_stats", {}).get("stats", {}).get("cache", 0)
    mem_usage_adjusted = max(mem_usage - cache, 0)

    net_rx = net_tx = 0
    for iface in raw.get("networks", {}).values():
        net_rx += iface.get("rx_bytes", 0)
        net_tx += iface.get("tx_bytes", 0)

    return {
        "status": container.status,
        "cpu_percent": round(cpu_percent, 1),
        "mem_usage_mb": round(mem_usage_adjusted / (1024 * 1024), 1),
        "mem_limit_mb": round(mem_limit / (1024 * 1024), 1),
        "mem_percent": round((mem_usage_adjusted / mem_limit) * 100, 1) if mem_limit else 0.0,
        "net_rx_mb": round(net_rx / (1024 * 1024), 2),
        "net_tx_mb": round(net_tx / (1024 * 1024), 2),
    }


def send_command(container_id, command):
    """
    Write a line to the container's stdin over a persistent attach socket.
    Requires the container to have been created with stdin_open=True.
    """
    with _attach_lock:
        sock = _attach_sockets.get(container_id)
        if sock is None:
            client = get_client()
            raw = client.api.attach_socket(container_id, params={"stdin": 1, "stream": 1})
            sock = getattr(raw, "_sock", raw)
            _attach_sockets[container_id] = sock

    data = (command.rstrip("\n") + "\n").encode("utf-8", errors="replace")
    try:
        sock.send(data)
    except (OSError, AttributeError) as exc:
        # Socket died (container restarted, etc.) -- drop it and let the next call reopen it.
        with _attach_lock:
            _attach_sockets.pop(container_id, None)
        raise APIError(f"Failed to send command, console link reset -- try again: {exc}")


def drop_attach_socket(container_id):
    with _attach_lock:
        sock = _attach_sockets.pop(container_id, None)
    if sock is not None:
        try:
            sock.close()
        except OSError:
            pass
