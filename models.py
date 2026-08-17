import datetime

from flask_login import UserMixin
from flask_sqlalchemy import SQLAlchemy
from werkzeug.security import check_password_hash, generate_password_hash

db = SQLAlchemy()


class User(UserMixin, db.Model):
    id = db.Column(db.Integer, primary_key=True)
    username = db.Column(db.String(64), unique=True, nullable=False, index=True)
    password_hash = db.Column(db.String(255), nullable=False)
    is_admin = db.Column(db.Boolean, default=False, nullable=False)
    created_at = db.Column(db.DateTime, default=datetime.datetime.utcnow)

    servers = db.relationship("Server", backref="owner", lazy=True)

    def set_password(self, password):
        self.password_hash = generate_password_hash(password)

    def check_password(self, password):
        return check_password_hash(self.password_hash, password)


class Server(db.Model):
    id = db.Column(db.Integer, primary_key=True)
    name = db.Column(db.String(64), nullable=False)
    slug = db.Column(db.String(64), unique=True, nullable=False)
    image = db.Column(db.String(255), nullable=False)
    container_id = db.Column(db.String(64), nullable=False)
    container_name = db.Column(db.String(128), nullable=False)
    data_path = db.Column(db.String(512), nullable=False)
    memory_mb = db.Column(db.Integer, nullable=False)
    port_mappings = db.Column(db.String(255), default="")  # "25565:25565/tcp,..."
    owner_id = db.Column(db.Integer, db.ForeignKey("user.id"), nullable=True)
    created_at = db.Column(db.DateTime, default=datetime.datetime.utcnow)

    def can_be_managed_by(self, user):
        return user.is_admin or self.owner_id == user.id
