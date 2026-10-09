// Package settings manages administrator drafts and orderly configuration reloads.
package settings

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"

	"sharefile/backend/internal/config"
	"sharefile/backend/internal/files"
	"sharefile/backend/internal/metadata"
)

type Snapshot struct {
	Active    config.Config  `json:"active"`
	Pending   *config.Config `json:"pending"`
	Revision  string         `json:"revision"`
	Applying  bool           `json:"applying"`
	LastError string         `json:"last_error"`
}

type Manager struct {
	mu        sync.Mutex
	path      string
	active    config.Config
	pending   *config.Config
	applying  bool
	lastError string
	requests  chan struct{}
}

func New(path string, active config.Config) (*Manager, error) {
	path, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	m := &Manager{path: path, active: active, requests: make(chan struct{}, 1)}
	c, err := config.Load(path + ".pending")
	if err == nil {
		m.pending = &c
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("read settings draft: %w", err)
	}
	return m, nil
}

func clone(c config.Config) config.Config {
	c.ImportDirs = append([]string{}, c.ImportDirs...)
	return c
}
func (m *Manager) snapshot() Snapshot {
	s := Snapshot{Active: clone(m.active), Applying: m.applying, LastError: m.lastError}
	if m.pending != nil {
		c := clone(*m.pending)
		s.Pending = &c
	}
	data, _ := json.Marshal([]any{s.Active, s.Pending})
	s.Revision = fmt.Sprintf("%x", sha256.Sum256(data))
	return s
}
func (m *Manager) Snapshot() Snapshot        { m.mu.Lock(); defer m.mu.Unlock(); return m.snapshot() }
func (m *Manager) Requests() <-chan struct{} { return m.requests }

func (m *Manager) checkRevision(revision string) error {
	if m.applying || revision != m.snapshot().Revision {
		return metadata.ErrConflict
	}
	// Do not overwrite manual edits made since this process loaded configuration.
	c, err := config.Load(m.path)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(clone(c), clone(m.active)) {
		return metadata.ErrConflict
	}
	return nil
}

func (m *Manager) Save(c config.Config, revision string) (Snapshot, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.checkRevision(revision); err != nil {
		return Snapshot{}, err
	}
	if err := ValidateResources(m.active, c, m.path); err != nil {
		return Snapshot{}, err
	}
	if err := config.Save(m.path+".pending", c); err != nil {
		return Snapshot{}, err
	}
	m.pending = &c
	m.lastError = ""
	return m.snapshot(), nil
}
func (m *Manager) Discard(revision string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.checkRevision(revision); err != nil {
		return err
	}
	if err := os.Remove(m.path + ".pending"); err != nil && !os.IsNotExist(err) {
		return err
	}
	m.pending, m.lastError = nil, ""
	return nil
}
func (m *Manager) RequestApply(revision string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.checkRevision(revision); err != nil {
		return err
	}
	if m.pending == nil {
		return metadata.ErrInvalid
	}
	if err := ValidateResources(m.active, *m.pending, m.path); err != nil {
		return err
	}
	m.applying = true
	m.lastError = ""
	m.requests <- struct{}{}
	return nil
}

// Commit runs with drained HTTP handlers, closed SQLite, and the source lock held.
func (m *Manager) Commit(ctx context.Context, disk *files.Disk) (config.Config, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.applying || m.pending == nil {
		return m.active, metadata.ErrInvalid
	}
	next := *m.pending
	current, err := config.Load(m.path)
	if err != nil {
		return m.active, err
	}
	if !reflect.DeepEqual(clone(current), clone(m.active)) {
		return m.active, metadata.ErrConflict
	}
	if err := ValidateResources(m.active, next, m.path); err != nil {
		return m.active, err
	}
	if err := CheckListeners(next); err != nil {
		return m.active, err
	}
	if next.DataDir != m.active.DataDir {
		if err := files.Migrate(ctx, disk, next.DataDir, next.VolumeID, next.ReserveBytes); err != nil {
			return m.active, err
		}
	}
	if err := config.Save(m.path, next); err != nil {
		return m.active, err
	}
	return next, nil
}

func (m *Manager) Activated(c config.Config) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.applying {
		return nil
	}
	m.active = c
	m.pending = nil
	m.applying = false
	if err := os.Remove(m.path + ".pending"); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func (m *Manager) Failed(c config.Config, err error) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.applying = false
	m.lastError = "Настройки не применены: " + err.Error()
	current, loadErr := config.Load(m.path)
	if loadErr != nil {
		return loadErr
	}
	if reflect.DeepEqual(clone(current), clone(c)) {
		return nil
	}
	if m.pending == nil || !reflect.DeepEqual(clone(current), clone(*m.pending)) {
		return metadata.ErrConflict
	}
	return config.Save(m.path, c)
}

func ValidateResources(active, c config.Config, configPath string) error {
	if err := c.Validate(); err != nil {
		return fmt.Errorf("Проверьте параметры конфигурации: %w", err)
	}
	for _, dir := range append(append([]string{}, c.ImportDirs...), filepath.Dir(c.DataDir)) {
		info, err := os.Stat(dir)
		if err != nil || !info.IsDir() {
			return fmt.Errorf("Директория должна существовать: %s", dir)
		}
	}
	if c.DataDir != active.DataDir {
		old, err := filepath.EvalSymlinks(active.DataDir)
		if err != nil {
			return err
		}
		parent, err := filepath.EvalSymlinks(filepath.Dir(c.DataDir))
		if err != nil {
			return err
		}
		next := filepath.Join(parent, filepath.Base(c.DataDir))
		if within(old, next) || within(next, old) {
			return fmt.Errorf("Новая директория не должна совпадать с текущей или находиться внутри неё")
		}
		abs, err := filepath.EvalSymlinks(configPath)
		if err != nil {
			return err
		}
		if within(old, abs) || within(next, abs) {
			return fmt.Errorf("Конфигурационный файл должен находиться вне переносимого хранилища")
		}
		if err := files.EmptyDestination(c.DataDir); err != nil {
			return fmt.Errorf("Новая директория хранения должна быть пустой: %w", err)
		}
	} else if c.VolumeID != active.VolumeID {
		return fmt.Errorf("Для новой метки диска выберите новую директорию хранения")
	}
	if c.WebDir != "" {
		info, err := os.Stat(filepath.Join(c.WebDir, "index.html"))
		if err != nil || !info.Mode().IsRegular() {
			return fmt.Errorf("Каталог Web UI должен содержать index.html")
		}
	}
	if !c.InsecureLocal {
		if _, err := tls.LoadX509KeyPair(c.AdminCert, c.AdminKey); err != nil {
			return fmt.Errorf("Проверьте сертификат TLS и приватный ключ: %w", err)
		}
	}
	for _, path := range []string{c.DataDir, c.LogFile} {
		if path == "" {
			continue
		}
		if path == c.LogFile {
			if info, err := os.Stat(path); err == nil && !info.Mode().IsRegular() {
				return fmt.Errorf("Путь журнала должен указывать на обычный файл")
			}
		}
		probe, err := os.CreateTemp(filepath.Dir(path), ".sharefile-write-test-*")
		if err != nil {
			return fmt.Errorf("Нет доступа на запись к директории: %s", filepath.Dir(path))
		}
		probe.Close()
		os.Remove(probe.Name())
	}
	return nil
}
func within(parent, child string) bool {
	r, err := filepath.Rel(parent, child)
	return err == nil && (r == "." || (!strings.HasPrefix(r, ".."+string(filepath.Separator)) && r != ".."))
}
func CheckListeners(c config.Config) error {
	var listeners []net.Listener
	defer func() {
		for _, l := range listeners {
			l.Close()
		}
	}()
	for _, addr := range []string{c.PublicAddr, c.AdminAddr} {
		l, err := net.Listen("tcp", addr)
		if err != nil {
			return fmt.Errorf("Не удалось открыть адрес %s: %w", addr, err)
		}
		listeners = append(listeners, l)
	}
	return nil
}
