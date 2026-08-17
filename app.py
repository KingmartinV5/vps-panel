import functools
import secrets
import shutil
from pathlib import Path

from docker.errors import APIError, NotFound
from flask import (
    Flask,
    Response,
    abort,
    flash,
    redirect,
    render_template,
    request,
    send_file,
    stream_with_context,
    url_for,
)
from flask_login import (
    LoginManager,
    current_user,
    login_required,
    login_user,
    logout_user,
)
from flask_wtf import CSRFProtect
from werkzeug.utils import secure_filename

import docker_manager
import fileops
from config import Config
from models import Server, User, db

app = Flask(__name__)
app.config.from_object(Config)

db.init_app(app)
csrf = CSRFProtect(app)

login_manager = LoginManager(app)
login_manager.login_view = "login"
login_manager.login_message_category = "error"


@login_manager.user_loader
def load_user(user_id):
    return db.session.get(User, int(user_id))


# --- Decorators ----------------------------------------------------------

def admin_required(view):
    @functools.wraps(view)
    @login_required
    def wrapped(*args, **kwargs):
        if not current_user.is_admin:
            abort(403)
        return view(*args, **kwargs)

    return wrapped


def server_view(view):
    """Loads the Server for server_id, checks ownership, passes it in as `server`."""

    @functools.wraps(view)
    @login_required
    def wrapped(server_id, *args, **kwargs):
        server = db.session.get(Server, server_id)
        if server is None:
            abort(404)
        if not server.can_be_managed_by(current_user):
            abort(403)
        return view(server, *args, **kwargs)

    return wrapped


# --- Auth ------------------------------------------------------------------

@app.route("/login", methods=["GET", "POST"])
def login():
    if current_user.is_authenticated:
        return redirect(url_for("dashboard"))
    if request.method == "POST":
        username = request.form.get("username", "").strip()
        password = request.form.get("password", "")
        user = User.query.filter_by(username=username).first()
        if user and user.check_password(password):
            login_user(user)
            return redirect(request.args.get("next") or url_for("dashboard"))
        flash("Invalid username or password", "error")
    return render_template("login.html")


@app.route("/logout")
@login_required
def logout():
    logout_user()
    return redirect(url_for("login"))


# --- Dashboard ---------------------------------------------------------

@app.route("/")
@login_required
def dashboard():
    if current_user.is_admin:
        servers = Server.query.order_by(Server.name).all()
    else:
        servers = Server.query.filter_by(owner_id=current_user.id).order_by(Server.name).all()

    statuses = {}
    for s in servers:
        statuses[s.id] = docker_manager.container_status(s.container_id)
    return render_template("dashboard.html", servers=servers, statuses=statuses)


# --- Server detail / console / power ------------------------------------

@app.route("/server/<int:server_id>")
@server_view
def server_detail(server):
    status = docker_manager.container_status(server.container_id)
    return render_template("server.html", server=server, status=status)


@app.route("/server/<int:server_id>/power", methods=["POST"])
@server_view
def server_power(server):
    action = request.form.get("action")
    if action not in ("start", "stop", "restart", "kill"):
        abort(400)
    try:
        docker_manager.power_action(server.container_id, action)
        if action in ("stop", "restart", "kill"):
            docker_manager.drop_attach_socket(server.container_id)
        flash(f"Server {action} requested", "success")
    except (APIError, NotFound) as exc:
        flash(f"Power action failed: {exc}", "error")
    return redirect(url_for("server_detail", server_id=server.id))


@app.route("/server/<int:server_id>/stats")
@server_view
def server_stats(server):
    try:
        return docker_manager.get_stats(server.container_id)
    except (APIError, NotFound):
        return {"status": "unavailable"}


@app.route("/server/<int:server_id>/console/stream")
@server_view
def server_console_stream(server):
    def generate():
        try:
            for line in docker_manager.stream_logs(server.container_id):
                yield f"data: {line.rstrip(chr(10))}\n\n"
        except (APIError, NotFound) as exc:
            yield f"data: [console stream ended: {exc}]\n\n"

    return Response(stream_with_context(generate()), mimetype="text/event-stream")


@app.route("/server/<int:server_id>/console/send", methods=["POST"])
@server_view
def server_console_send(server):
    command = request.form.get("command", "")
    if not command.strip():
        return {"ok": False, "error": "empty command"}, 400
    try:
        docker_manager.send_command(server.container_id, command)
        return {"ok": True}
    except (APIError, NotFound) as exc:
        return {"ok": False, "error": str(exc)}, 502


# --- File manager --------------------------------------------------------

@app.route("/server/<int:server_id>/files")
@server_view
def server_files(server):
    path = request.args.get("path", "")
    try:
        entries = fileops.list_dir(server_data_root(server), path)
    except (FileNotFoundError, fileops.PathSecurityError):
        abort(404)
    parent = "/".join(path.strip("/").split("/")[:-1]) if path.strip("/") else None
    return render_template("files.html", server=server, path=path.strip("/"), entries=entries, parent=parent)


@app.route("/server/<int:server_id>/files/edit", methods=["GET", "POST"])
@server_view
def server_files_edit(server):
    path = request.args.get("path") or request.form.get("path")
    if not path:
        abort(400)
    root = server_data_root(server)
    if request.method == "POST":
        try:
            fileops.write_text_file(root, path, request.form.get("content", ""))
            flash("File saved", "success")
        except fileops.PathSecurityError:
            abort(403)
        return redirect(url_for("server_files_edit", server_id=server.id, path=path))
    try:
        content = fileops.read_text_file(root, path, app.config["MAX_EDIT_MB"] * 1024 * 1024)
    except FileNotFoundError:
        abort(404)
    except fileops.PathSecurityError:
        abort(403)
    except ValueError as exc:
        flash(str(exc), "error")
        return redirect(url_for("server_files", server_id=server.id))
    return render_template("edit_file.html", server=server, path=path, content=content)


@app.route("/server/<int:server_id>/files/upload", methods=["POST"])
@server_view
def server_files_upload(server):
    path = request.form.get("path", "")
    file_storage = request.files.get("file")
    if not file_storage or not file_storage.filename:
        flash("No file selected", "error")
        return redirect(url_for("server_files", server_id=server.id, path=path))
    try:
        fileops.save_upload(server_data_root(server), path, file_storage)
        flash("File uploaded", "success")
    except (fileops.PathSecurityError, ValueError) as exc:
        flash(f"Upload rejected: {exc}", "error")
    return redirect(url_for("server_files", server_id=server.id, path=path))


@app.route("/server/<int:server_id>/files/mkdir", methods=["POST"])
@server_view
def server_files_mkdir(server):
    path = request.form.get("path", "")
    name = secure_filename(request.form.get("name", ""))
    if not name:
        flash("Invalid folder name", "error")
        return redirect(url_for("server_files", server_id=server.id, path=path))
    try:
        fileops.make_dir(server_data_root(server), f"{path}/{name}")
    except fileops.PathSecurityError:
        abort(403)
    return redirect(url_for("server_files", server_id=server.id, path=path))


@app.route("/server/<int:server_id>/files/delete", methods=["POST"])
@server_view
def server_files_delete(server):
    path = request.form.get("path", "")
    try:
        fileops.delete_path(server_data_root(server), path)
        flash("Deleted", "success")
    except fileops.PathSecurityError as exc:
        flash(str(exc), "error")
    parent = "/".join(path.strip("/").split("/")[:-1])
    return redirect(url_for("server_files", server_id=server.id, path=parent))


@app.route("/server/<int:server_id>/files/download")
@server_view
def server_files_download(server):
    path = request.args.get("path", "")
    try:
        target = fileops.resolve_download(server_data_root(server), path)
    except (FileNotFoundError, fileops.PathSecurityError):
        abort(404)
    return send_file(target, as_attachment=True)


# --- Backups ---------------------------------------------------------------

@app.route("/server/<int:server_id>/backups")
@server_view
def server_backups(server):
    backups = fileops.list_backups(Config.BACKUPS_ROOT, server.slug)
    return render_template("backups.html", server=server, backups=backups)


@app.route("/server/<int:server_id>/backups/create", methods=["POST"])
@server_view
def server_backups_create(server):
    try:
        fileops.create_backup(Config.BACKUPS_ROOT, server.slug, server_data_root(server))
        flash("Backup created", "success")
    except OSError as exc:
        flash(f"Backup failed: {exc}", "error")
    return redirect(url_for("server_backups", server_id=server.id))


@app.route("/server/<int:server_id>/backups/<filename>/download")
@server_view
def server_backups_download(server, filename):
    try:
        target = fileops.resolve_backup(Config.BACKUPS_ROOT, server.slug, filename)
    except (FileNotFoundError, fileops.PathSecurityError):
        abort(404)
    return send_file(target, as_attachment=True)


@app.route("/server/<int:server_id>/backups/<filename>/delete", methods=["POST"])
@server_view
def server_backups_delete(server, filename):
    try:
        fileops.delete_backup(Config.BACKUPS_ROOT, server.slug, filename)
        flash("Backup deleted", "success")
    except (FileNotFoundError, fileops.PathSecurityError):
        flash("Backup not found", "error")
    return redirect(url_for("server_backups", server_id=server.id))


# --- Admin: users ------------------------------------------------------

@app.route("/admin/users")
@admin_required
def admin_users():
    users = User.query.order_by(User.username).all()
    return render_template("admin_users.html", users=users)


@app.route("/admin/users/create", methods=["POST"])
@admin_required
def admin_users_create():
    username = request.form.get("username", "").strip()
    password = request.form.get("password", "")
    is_admin = request.form.get("is_admin") == "on"
    if not username or len(password) < 8:
        flash("Username required and password must be at least 8 characters", "error")
        return redirect(url_for("admin_users"))
    if User.query.filter_by(username=username).first():
        flash("Username already exists", "error")
        return redirect(url_for("admin_users"))
    user = User(username=username, is_admin=is_admin)
    user.set_password(password)
    db.session.add(user)
    db.session.commit()
    flash(f"Created user {username}", "success")
    return redirect(url_for("admin_users"))


@app.route("/admin/users/<int:user_id>/delete", methods=["POST"])
@admin_required
def admin_users_delete(user_id):
    if user_id == current_user.id:
        flash("You can't delete your own account", "error")
        return redirect(url_for("admin_users"))
    user = db.session.get(User, user_id)
    if user is None:
        abort(404)
    if Server.query.filter_by(owner_id=user.id).count() > 0:
        flash("Reassign or delete this user's servers before deleting the account", "error")
        return redirect(url_for("admin_users"))
    db.session.delete(user)
    db.session.commit()
    flash("User deleted", "success")
    return redirect(url_for("admin_users"))


# --- Admin: servers ------------------------------------------------------

@app.route("/admin/servers/create", methods=["GET", "POST"])
@admin_required
def admin_servers_create():
    customers = User.query.order_by(User.username).all()
    if request.method == "POST":
        name = request.form.get("name", "").strip()
        image = request.form.get("image", "").strip()
        memory_mb = request.form.get("memory_mb", "1024").strip()
        owner_id = request.form.get("owner_id") or None
        raw_ports = request.form.get("ports", "")
        raw_env = request.form.get("env", "")

        errors = []
        if not name:
            errors.append("Name is required")
        if image not in Config.DOCKER_ALLOWED_IMAGES:
            errors.append("Image is not in the allowed list")
        try:
            memory_mb = int(memory_mb)
            if memory_mb < 256:
                errors.append("Memory limit must be at least 256 MB")
        except ValueError:
            errors.append("Memory limit must be a number")

        try:
            ports = docker_manager.parse_port_mappings(raw_ports)
        except ValueError as exc:
            errors.append(str(exc))
            ports = {}
        try:
            env = docker_manager.parse_env(raw_env)
        except ValueError as exc:
            errors.append(str(exc))
            env = {}

        slug = docker_manager.slugify(name)
        if Server.query.filter_by(slug=slug).first():
            errors.append("A server with a similar name already exists")

        if errors:
            for e in errors:
                flash(e, "error")
            return render_template(
                "admin_create_server.html",
                customers=customers,
                allowed_images=Config.DOCKER_ALLOWED_IMAGES,
                form=request.form,
            )

        data_dir = Config.SERVERS_ROOT / slug
        data_dir.mkdir(parents=True, exist_ok=True)
        container_name = f"panel-{slug}"

        try:
            container = docker_manager.create_container(
                container_name, image, data_dir, memory_mb, ports, env
            )
        except APIError as exc:
            flash(f"Docker error creating container: {exc}", "error")
            return render_template(
                "admin_create_server.html",
                customers=customers,
                allowed_images=Config.DOCKER_ALLOWED_IMAGES,
                form=request.form,
            )

        server = Server(
            name=name,
            slug=slug,
            image=image,
            container_id=container.id,
            container_name=container_name,
            data_path=str(data_dir),
            memory_mb=memory_mb,
            port_mappings=raw_ports,
            owner_id=int(owner_id) if owner_id else None,
        )
        db.session.add(server)
        db.session.commit()
        flash(f"Server '{name}' created", "success")
        return redirect(url_for("server_detail", server_id=server.id))

    return render_template(
        "admin_create_server.html", customers=customers, allowed_images=Config.DOCKER_ALLOWED_IMAGES, form={}
    )


@app.route("/server/<int:server_id>/delete", methods=["POST"])
@admin_required
def admin_servers_delete(server_id):
    server = db.session.get(Server, server_id)
    if server is None:
        abort(404)
    docker_manager.drop_attach_socket(server.container_id)
    docker_manager.remove_container(server.container_id)
    if request.form.get("delete_data") == "on":
        shutil.rmtree(server.data_path, ignore_errors=True)
    db.session.delete(server)
    db.session.commit()
    flash(f"Server '{server.name}' deleted", "success")
    return redirect(url_for("dashboard"))


# --- Helpers ---------------------------------------------------------------

def server_data_root(server):
    return Path(server.data_path)


def bootstrap_admin():
    if User.query.count() > 0:
        return
    password = secrets.token_urlsafe(12)
    admin = User(username="admin", is_admin=True)
    admin.set_password(password)
    db.session.add(admin)
    db.session.commit()
    print("=" * 60)
    print("No users existed yet -- created initial admin account:")
    print("  username: admin")
    print(f"  password: {password}")
    print("Log in and create additional accounts, then consider")
    print("changing this password.")
    print("=" * 60)


with app.app_context():
    db.create_all()
    bootstrap_admin()


if __name__ == "__main__":
    import os

    host = os.environ.get("PANEL_HOST", "127.0.0.1")
    port = int(os.environ.get("PANEL_PORT", "5000"))
    app.run(host=host, port=port, debug=False, threaded=True)
