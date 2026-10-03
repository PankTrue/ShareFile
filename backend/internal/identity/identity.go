package identity

import (
	"context"
	"database/sql"
	"errors"
	"regexp"
	"strings"
	"time"

	"sharefile/backend/internal/access"
	"sharefile/backend/internal/metadata"
	"sharefile/backend/internal/security"
)

type User struct {
	ID       string `json:"id"`
	Username string `json:"username"`
	Admin    bool   `json:"admin"`
	Active   bool   `json:"active"`
	HomeID   string `json:"home_id"`
}
type Actor struct {
	User
	CSRF      string
	TokenHash string
	Device    bool
}
type Service struct {
	Store     *metadata.Store
	Passwords *security.Passwords
	dummy     string
}

var usernamePattern = regexp.MustCompile("^[a-z0-9][a-z0-9._-]{0,63}$")
var ErrPending = errors.New("authorization pending")

func New(store *metadata.Store, p *security.Passwords) (*Service, error) {
	dummy, err := p.Hash(context.Background(), security.Token())
	return &Service{store, p, dummy}, err
}
func (s *Service) CreateUser(ctx context.Context, name, password string, admin bool) (User, error) {
	name = strings.ToLower(name)
	u := User{ID: security.Token(), Username: name, Admin: admin, Active: true, HomeID: security.Token()}
	if !usernamePattern.MatchString(name) {
		return u, metadata.ErrInvalid
	}
	hash, err := s.Passwords.Hash(ctx, password)
	if err != nil {
		return u, metadata.ErrInvalid
	}
	err = s.Store.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "INSERT INTO users(id,username,password,admin,home_id,created_at) VALUES(?,?,?,?,?,?)", u.ID, name, hash, admin, u.HomeID, time.Now().Unix())
		if err != nil {
			if metadata.IsUnique(err) {
				return metadata.ErrConflict
			}
			return err
		}
		homeName, err := metadata.UniqueName(ctx, tx, "", name, "")
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, "INSERT INTO nodes(id,owner_id,name,kind,created_at) VALUES(?,?,?,'folder',?)", u.HomeID, u.ID, homeName, time.Now().Unix())
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, "INSERT INTO nodes(id,parent_id,owner_id,name,kind,created_at) VALUES(?,?,?,'Uploads','folder',?)", security.Token(), u.HomeID, u.ID, time.Now().Unix())
		return err
	})
	return u, err
}
func scanUser(row interface{ Scan(...any) error }) (User, error) {
	var u User
	err := row.Scan(&u.ID, &u.Username, &u.Admin, &u.Active, &u.HomeID)
	if errors.Is(err, sql.ErrNoRows) {
		err = metadata.ErrNotFound
	}
	return u, err
}
func (s *Service) Users(ctx context.Context) ([]User, error) {
	rows, err := s.Store.DB.QueryContext(ctx, "SELECT id,username,admin,active,home_id FROM users ORDER BY username")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []User{}
	for rows.Next() {
		u, e := scanUser(rows)
		if e != nil {
			return nil, e
		}
		out = append(out, u)
	}
	return out, rows.Err()
}
func (s *Service) Login(ctx context.Context, name, password string) (User, string, string, error) {
	var id, hash string
	var active bool
	err := s.Store.DB.QueryRowContext(ctx, "SELECT id,password,active FROM users WHERE username=?", strings.ToLower(name)).Scan(&id, &hash, &active)
	if err != nil {
		hash = s.dummy
	}
	valid := s.Passwords.Verify(ctx, password, hash)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return User{}, "", "", err
	}
	if !valid || !active {
		return User{}, "", "", metadata.ErrForbidden
	}
	token, csrf := security.Token(), security.Token()
	var u User
	err = s.Store.Write(ctx, func(tx *sql.Tx) error {
		var e error
		u, e = scanUser(tx.QueryRowContext(ctx, "SELECT id,username,admin,active,home_id FROM users WHERE id=?", id))
		if e != nil {
			return e
		}
		if !u.Active {
			return metadata.ErrForbidden
		}
		var current string
		if e = tx.QueryRowContext(ctx, "SELECT password FROM users WHERE id=?", id).Scan(&current); e != nil {
			return e
		}
		if current != hash {
			return metadata.ErrForbidden
		}
		_, e = tx.ExecContext(ctx, "INSERT INTO sessions VALUES(?,?,?,?)", security.Digest(token), id, csrf, time.Now().Add(24*time.Hour).Unix())
		return e
	})
	return u, token, csrf, err
}
func (s *Service) Authenticate(ctx context.Context, token string, device bool) (Actor, error) {
	var a Actor
	if len(token) != 43 {
		return a, metadata.ErrForbidden
	}
	query := "SELECT u.id,u.username,u.admin,u.active,u.home_id,s.csrf FROM sessions s JOIN users u ON u.id=s.user_id WHERE s.hash=? AND s.expires_at>? AND u.active=1"
	if device {
		query = "SELECT u.id,u.username,u.admin,u.active,u.home_id,'' FROM devices s JOIN users u ON u.id=s.user_id WHERE s.hash=? AND s.expires_at>? AND s.revoked=0 AND u.active=1"
	}
	a.TokenHash = security.Digest(token)
	a.Device = device
	err := s.Store.DB.QueryRowContext(ctx, query, a.TokenHash, time.Now().Unix()).Scan(&a.ID, &a.Username, &a.Admin, &a.Active, &a.HomeID, &a.CSRF)
	if errors.Is(err, sql.ErrNoRows) {
		err = metadata.ErrForbidden
	}
	return a, err
}
func (s *Service) Logout(ctx context.Context, a Actor) error {
	_, err := s.Store.DB.ExecContext(ctx, "DELETE FROM sessions WHERE hash=?", a.TokenHash)
	return err
}
func (s *Service) UpdateUser(ctx context.Context, id string, active *bool, password string) error {
	hash := ""
	if password != "" {
		var err error
		hash, err = s.Passwords.Hash(ctx, password)
		if err != nil {
			return metadata.ErrInvalid
		}
	}
	return s.Store.Write(ctx, func(tx *sql.Tx) error {
		u, err := scanUser(tx.QueryRowContext(ctx, "SELECT id,username,admin,active,home_id FROM users WHERE id=?", id))
		if err != nil {
			return err
		}
		if active != nil && !*active && u.Admin && u.Active {
			var count int
			if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM users WHERE admin=1 AND active=1").Scan(&count); err != nil {
				return err
			}
			if count <= 1 {
				return metadata.ErrConflict
			}
		}
		if active != nil {
			if _, err = tx.ExecContext(ctx, "UPDATE users SET active=? WHERE id=?", *active, id); err != nil {
				return err
			}
		}
		if hash != "" {
			if _, err = tx.ExecContext(ctx, "UPDATE users SET password=? WHERE id=?", hash, id); err != nil {
				return err
			}
		}
		if hash != "" || (active != nil && !*active) {
			if _, err = tx.ExecContext(ctx, "DELETE FROM sessions WHERE user_id=?", id); err != nil {
				return err
			}
			if _, err = tx.ExecContext(ctx, "UPDATE devices SET revoked=1 WHERE user_id=?", id); err != nil {
				return err
			}
			if _, err = tx.ExecContext(ctx, "DELETE FROM device_requests WHERE user_id=?", id); err != nil {
				return err
			}
		}
		return access.RevokeInvalid(ctx, tx)
	})
}

type DeviceRequest struct {
	ID        string `json:"id"`
	Secret    string `json:"secret"`
	Name      string `json:"name"`
	ExpiresAt int64  `json:"expires_at"`
}

func (s *Service) StartDevice(ctx context.Context, name string) (DeviceRequest, error) {
	r := DeviceRequest{security.Token(), security.Token(), name, time.Now().Add(10 * time.Minute).Unix()}
	if !metadata.ValidName(name) {
		return r, metadata.ErrInvalid
	}
	_, err := s.Store.DB.ExecContext(ctx, "INSERT INTO device_requests(id,secret_hash,name,expires_at) VALUES(?,?,?,?)", r.ID, security.Digest(r.Secret), name, r.ExpiresAt)
	return r, err
}
func (s *Service) DeviceRequest(ctx context.Context, id string) (DeviceRequest, error) {
	var r DeviceRequest
	err := s.Store.DB.QueryRowContext(ctx, "SELECT id,name,expires_at FROM device_requests WHERE id=? AND expires_at>? AND consumed=0", id, time.Now().Unix()).Scan(&r.ID, &r.Name, &r.ExpiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		err = metadata.ErrNotFound
	}
	return r, err
}
func (s *Service) ApproveDevice(ctx context.Context, user, id string) error {
	result, err := s.Store.DB.ExecContext(ctx, "UPDATE device_requests SET user_id=? WHERE id=? AND expires_at>? AND consumed=0 AND user_id IS NULL", user, id, time.Now().Unix())
	if err != nil {
		return err
	}
	n, _ := result.RowsAffected()
	if n != 1 {
		return metadata.ErrConflict
	}
	return nil
}
func (s *Service) PollDevice(ctx context.Context, id, secret string) (string, error) {
	token := security.Token()
	err := s.Store.Write(ctx, func(tx *sql.Tx) error {
		var user sql.NullString
		var name string
		err := tx.QueryRowContext(ctx, "SELECT user_id,name FROM device_requests WHERE id=? AND secret_hash=? AND expires_at>? AND consumed=0", id, security.Digest(secret), time.Now().Unix()).Scan(&user, &name)
		if errors.Is(err, sql.ErrNoRows) {
			return metadata.ErrNotFound
		}
		if err != nil {
			return err
		}
		if !user.Valid {
			return ErrPending
		}
		var active bool
		if err = tx.QueryRowContext(ctx, "SELECT active FROM users WHERE id=?", user.String).Scan(&active); err != nil {
			return err
		}
		if !active {
			return metadata.ErrForbidden
		}
		if _, err = tx.ExecContext(ctx, "INSERT INTO devices(id,user_id,name,hash,created_at,expires_at) VALUES(?,?,?,?,?,?)", security.Token(), user.String, name, security.Digest(token), time.Now().Unix(), time.Now().Add(180*24*time.Hour).Unix()); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, "UPDATE device_requests SET consumed=1 WHERE id=?", id)
		return err
	})
	return token, err
}

type Device struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	CreatedAt int64  `json:"created_at"`
	ExpiresAt int64  `json:"expires_at"`
	Revoked   bool   `json:"revoked"`
}

func (s *Service) Devices(ctx context.Context, user string) ([]Device, error) {
	rows, err := s.Store.DB.QueryContext(ctx, "SELECT id,name,created_at,expires_at,revoked FROM devices WHERE user_id=? ORDER BY created_at", user)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Device{}
	for rows.Next() {
		var d Device
		if err = rows.Scan(&d.ID, &d.Name, &d.CreatedAt, &d.ExpiresAt, &d.Revoked); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}
func (s *Service) RevokeDevice(ctx context.Context, user, id string) error {
	r, err := s.Store.DB.ExecContext(ctx, "UPDATE devices SET revoked=1 WHERE id=? AND user_id=?", id, user)
	if err != nil {
		return err
	}
	n, _ := r.RowsAffected()
	if n == 0 {
		return metadata.ErrNotFound
	}
	return nil
}
