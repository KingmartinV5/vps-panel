// Package store is the sqlite data access layer, replacing models.py +
// Flask-SQLAlchemy. Schema mirrors the Python models exactly.
package store

import (
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
	_ "modernc.org/sqlite"
)

type User struct {
	ID           int64
	Username     string
	PasswordHash string
	IsAdmin      bool
	CreatedAt    time.Time
}

func (u *User) SetPassword(password string) error {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	u.PasswordHash = string(hash)
	return nil
}

func (u *User) CheckPassword(password string) bool {
	return bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(password)) == nil
}

type Server struct {
	ID             int64
	Name           string
	Slug           string
	Image          string
	ContainerID    string
	ContainerName  string
	DataPath       string
	MemoryMB       int
	PortMappings   string
	Env            string
	OwnerID        sql.NullInt64
	CreatedAt      time.Time
}

func (s *Server) CanBeManagedBy(u *User) bool {
	return u.IsAdmin || (s.OwnerID.Valid && s.OwnerID.Int64 == u.ID)
}

type Store struct {
	db *sql.DB
}

// dsnPath extracts a filesystem path out of a sqlite DSN of the form
// "sqlite:///path" (SQLAlchemy style, used by PANEL_DATABASE_URL) or a bare path.
func dsnPath(url string) string {
	if strings.HasPrefix(url, "sqlite:///") {
		return "/" + strings.TrimPrefix(url, "sqlite:///")
	}
	if strings.HasPrefix(url, "sqlite://") {
		return strings.TrimPrefix(url, "sqlite://")
	}
	return url
}

func Open(databaseURL string) (*Store, error) {
	path := dsnPath(databaseURL)
	db, err := sql.Open("sqlite", path+"?_pragma=foreign_keys(1)")
	if err != nil {
		return nil, err
	}
	// sqlite handles one writer at a time; keep a single connection to avoid
	// "database is locked" errors under concurrent requests.
	db.SetMaxOpenConns(1)
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Store) migrate() error {
	schema := `
	CREATE TABLE IF NOT EXISTS user (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		username VARCHAR(64) NOT NULL UNIQUE,
		password_hash VARCHAR(255) NOT NULL,
		is_admin BOOLEAN NOT NULL DEFAULT 0,
		created_at DATETIME NOT NULL
	);
	CREATE INDEX IF NOT EXISTS ix_user_username ON user(username);

	CREATE TABLE IF NOT EXISTS server (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		name VARCHAR(64) NOT NULL,
		slug VARCHAR(64) NOT NULL UNIQUE,
		image VARCHAR(255) NOT NULL,
		container_id VARCHAR(64) NOT NULL,
		container_name VARCHAR(128) NOT NULL,
		data_path VARCHAR(512) NOT NULL,
		memory_mb INTEGER NOT NULL,
		port_mappings VARCHAR(255) DEFAULT '',
		owner_id INTEGER REFERENCES user(id),
		created_at DATETIME NOT NULL
	);
	`
	if _, err := s.db.Exec(schema); err != nil {
		return err
	}
	return s.migrateServerEnvColumn()
}

// migrateServerEnvColumn adds the server.env column for installs that
// predate it (the raw KEY=VALUE env text a server was created/last edited
// with -- needed so the admin edit form can round-trip it). SQLite's
// "ALTER TABLE ... ADD COLUMN IF NOT EXISTS" isn't something we rely on
// working everywhere, so check first via PRAGMA table_info.
func (s *Store) migrateServerEnvColumn() error {
	rows, err := s.db.Query(`PRAGMA table_info(server)`)
	if err != nil {
		return err
	}
	defer rows.Close()
	hasEnv := false
	for rows.Next() {
		var cid int
		var name, ctype string
		var notnull int
		var dflt sql.NullString
		var pk int
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			return err
		}
		if name == "env" {
			hasEnv = true
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if hasEnv {
		return nil
	}
	_, err = s.db.Exec(`ALTER TABLE server ADD COLUMN env TEXT DEFAULT ''`)
	return err
}

func (s *Store) BootstrapAdmin() error {
	var count int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM user`).Scan(&count); err != nil {
		return err
	}
	if count > 0 {
		return nil
	}
	password := randomToken(12)
	admin := &User{Username: "admin", IsAdmin: true}
	if err := admin.SetPassword(password); err != nil {
		return err
	}
	if _, err := s.CreateUser(admin); err != nil {
		return err
	}
	log.Println(strings.Repeat("=", 60))
	log.Println("No users existed yet -- created initial admin account:")
	log.Println("  username: admin")
	log.Printf("  password: %s\n", password)
	log.Println("Log in and create additional accounts, then consider")
	log.Println("changing this password.")
	log.Println(strings.Repeat("=", 60))
	return nil
}

var ErrNotFound = errors.New("not found")

func (s *Store) CreateUser(u *User) (int64, error) {
	res, err := s.db.Exec(
		`INSERT INTO user (username, password_hash, is_admin, created_at) VALUES (?, ?, ?, ?)`,
		u.Username, u.PasswordHash, u.IsAdmin, time.Now().UTC(),
	)
	if err != nil {
		return 0, err
	}
	id, err := res.LastInsertId()
	u.ID = id
	return id, err
}

func scanUser(row interface{ Scan(...any) error }) (*User, error) {
	u := &User{}
	var isAdmin int
	if err := row.Scan(&u.ID, &u.Username, &u.PasswordHash, &isAdmin, &u.CreatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	u.IsAdmin = isAdmin != 0
	return u, nil
}

func (s *Store) GetUser(id int64) (*User, error) {
	row := s.db.QueryRow(`SELECT id, username, password_hash, is_admin, created_at FROM user WHERE id = ?`, id)
	return scanUser(row)
}

func (s *Store) GetUserByUsername(username string) (*User, error) {
	row := s.db.QueryRow(`SELECT id, username, password_hash, is_admin, created_at FROM user WHERE username = ?`, username)
	return scanUser(row)
}

func (s *Store) ListUsers() ([]*User, error) {
	rows, err := s.db.Query(`SELECT id, username, password_hash, is_admin, created_at FROM user ORDER BY username`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var users []*User
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		users = append(users, u)
	}
	return users, rows.Err()
}

func (s *Store) UpdateUserPassword(u *User) error {
	_, err := s.db.Exec(`UPDATE user SET password_hash = ? WHERE id = ?`, u.PasswordHash, u.ID)
	return err
}

func (s *Store) UpdateUserAdmin(id int64, isAdmin bool) error {
	_, err := s.db.Exec(`UPDATE user SET is_admin = ? WHERE id = ?`, isAdmin, id)
	return err
}

func (s *Store) DeleteUser(id int64) error {
	_, err := s.db.Exec(`DELETE FROM user WHERE id = ?`, id)
	return err
}

func (s *Store) CountServersByOwner(ownerID int64) (int, error) {
	var count int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM server WHERE owner_id = ?`, ownerID).Scan(&count)
	return count, err
}

func scanServer(row interface{ Scan(...any) error }) (*Server, error) {
	s := &Server{}
	if err := row.Scan(&s.ID, &s.Name, &s.Slug, &s.Image, &s.ContainerID, &s.ContainerName,
		&s.DataPath, &s.MemoryMB, &s.PortMappings, &s.Env, &s.OwnerID, &s.CreatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return s, nil
}

const serverCols = `id, name, slug, image, container_id, container_name, data_path, memory_mb, port_mappings, env, owner_id, created_at`

func (s *Store) GetServer(id int64) (*Server, error) {
	row := s.db.QueryRow(fmt.Sprintf(`SELECT %s FROM server WHERE id = ?`, serverCols), id)
	return scanServer(row)
}

func (s *Store) GetServerBySlug(slug string) (*Server, error) {
	row := s.db.QueryRow(fmt.Sprintf(`SELECT %s FROM server WHERE slug = ?`, serverCols), slug)
	return scanServer(row)
}

func (s *Store) ListServers() ([]*Server, error) {
	rows, err := s.db.Query(fmt.Sprintf(`SELECT %s FROM server ORDER BY name`, serverCols))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Server
	for rows.Next() {
		sv, err := scanServer(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, sv)
	}
	return out, rows.Err()
}

func (s *Store) ListServersByOwner(ownerID int64) ([]*Server, error) {
	rows, err := s.db.Query(fmt.Sprintf(`SELECT %s FROM server WHERE owner_id = ? ORDER BY name`, serverCols), ownerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Server
	for rows.Next() {
		sv, err := scanServer(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, sv)
	}
	return out, rows.Err()
}

func (s *Store) CreateServer(sv *Server) (int64, error) {
	res, err := s.db.Exec(
		`INSERT INTO server (name, slug, image, container_id, container_name, data_path, memory_mb, port_mappings, env, owner_id, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		sv.Name, sv.Slug, sv.Image, sv.ContainerID, sv.ContainerName, sv.DataPath, sv.MemoryMB, sv.PortMappings, sv.Env, sv.OwnerID, time.Now().UTC(),
	)
	if err != nil {
		return 0, err
	}
	id, err := res.LastInsertId()
	sv.ID = id
	return id, err
}

// UpdateServer updates the mutable fields of an existing server row (used
// after recreating its container with new settings). ID/Slug/CreatedAt/
// DataPath are not touched.
func (s *Store) UpdateServer(sv *Server) error {
	_, err := s.db.Exec(
		`UPDATE server SET name = ?, image = ?, container_id = ?, container_name = ?, memory_mb = ?, port_mappings = ?, env = ?, owner_id = ? WHERE id = ?`,
		sv.Name, sv.Image, sv.ContainerID, sv.ContainerName, sv.MemoryMB, sv.PortMappings, sv.Env, sv.OwnerID, sv.ID,
	)
	return err
}

func (s *Store) DeleteServer(id int64) error {
	_, err := s.db.Exec(`DELETE FROM server WHERE id = ?`, id)
	return err
}

func (s *Store) Close() error {
	return s.db.Close()
}

func randomToken(n int) string {
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_"
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		// extremely unlikely; fall back to a fixed-length weaker token rather than panic
		return "changeme-please-rotate"
	}
	out := make([]byte, n)
	for i, c := range b {
		out[i] = alphabet[int(c)%len(alphabet)]
	}
	return string(out)
}
