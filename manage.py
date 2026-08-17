"""
Small CLI for user management, since the panel intentionally has no
self-service signup.

    python manage.py create-admin <username>
    python manage.py create-user <username>
    python manage.py set-password <username>
    python manage.py list-users
    python manage.py delete-user <username>
"""
import argparse
import getpass
import sys

from app import app
from models import Server, User, db


def prompt_password():
    while True:
        pw = getpass.getpass("Password: ")
        if len(pw) < 8:
            print("Password must be at least 8 characters.")
            continue
        confirm = getpass.getpass("Confirm password: ")
        if pw != confirm:
            print("Passwords did not match.")
            continue
        return pw


def create_user(username, is_admin):
    if User.query.filter_by(username=username).first():
        print(f"User {username!r} already exists.")
        sys.exit(1)
    password = prompt_password()
    user = User(username=username, is_admin=is_admin)
    user.set_password(password)
    db.session.add(user)
    db.session.commit()
    print(f"Created {'admin' if is_admin else 'customer'} user {username!r}.")


def set_password(username):
    user = User.query.filter_by(username=username).first()
    if not user:
        print(f"No such user: {username!r}")
        sys.exit(1)
    user.set_password(prompt_password())
    db.session.commit()
    print(f"Password updated for {username!r}.")


def list_users():
    for u in User.query.order_by(User.username).all():
        role = "admin" if u.is_admin else "customer"
        print(f"{u.id:>4}  {u.username:<20} {role:<10} {len(u.servers)} server(s)")


def delete_user(username):
    user = User.query.filter_by(username=username).first()
    if not user:
        print(f"No such user: {username!r}")
        sys.exit(1)
    if Server.query.filter_by(owner_id=user.id).count() > 0:
        print("Reassign or delete this user's servers first.")
        sys.exit(1)
    db.session.delete(user)
    db.session.commit()
    print(f"Deleted user {username!r}.")


def main():
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    sub = parser.add_subparsers(dest="cmd", required=True)

    p = sub.add_parser("create-admin")
    p.add_argument("username")

    p = sub.add_parser("create-user")
    p.add_argument("username")

    p = sub.add_parser("set-password")
    p.add_argument("username")

    sub.add_parser("list-users")

    p = sub.add_parser("delete-user")
    p.add_argument("username")

    args = parser.parse_args()

    with app.app_context():
        if args.cmd == "create-admin":
            create_user(args.username, is_admin=True)
        elif args.cmd == "create-user":
            create_user(args.username, is_admin=False)
        elif args.cmd == "set-password":
            set_password(args.username)
        elif args.cmd == "list-users":
            list_users()
        elif args.cmd == "delete-user":
            delete_user(args.username)


if __name__ == "__main__":
    main()
