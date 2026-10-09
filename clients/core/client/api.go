// Package client implements the desktop protocol independently of the operating system.
// Credentials belong to the platform adapter; Client never persists them.
package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

type Client struct {
	origin string
	token  string
	http   *http.Client
}

func NormalizeOrigin(raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" || u.User != nil || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" {
		return "", errors.New("Введите адрес сервера без пути, логина и параметров")
	}
	ip := net.ParseIP(u.Hostname())
	loopback := u.Hostname() == "localhost" || (ip != nil && ip.IsLoopback())
	if u.Scheme != "https" && !(u.Scheme == "http" && loopback) {
		return "", errors.New("Нужен HTTPS; HTTP разрешён только для локального тестирования")
	}
	u.Path, u.RawPath = "", ""
	return strings.TrimRight(u.String(), "/"), nil
}

func New(origin, token string) (*Client, error) {
	origin, err := NormalizeOrigin(origin)
	if err != nil {
		return nil, err
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.ResponseHeaderTimeout = 15 * time.Minute
	transport.IdleConnTimeout = 30 * time.Second
	return &Client{origin: origin, token: token, http: &http.Client{
		Transport: transport, Timeout: 15 * time.Minute,
		// Never forward a device credential through a redirect, including same-host redirects.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}, nil
}

type HTTPError struct {
	Status     int
	RetryAfter time.Duration
}

type requestTimeout struct{}

func (requestTimeout) Error() string {
	return "Сервер не ответил вовремя. Проверьте сеть и попробуйте снова"
}
func (requestTimeout) Unwrap() error { return context.DeadlineExceeded }

func (e *HTTPError) Error() string {
	switch e.Status {
	case 401:
		return "Подключение отозвано или истекло. Подключите устройство заново"
	case 403:
		return "Недостаточно прав для этой операции"
	case 404:
		return "Объект не найден или срок загрузки истёк"
	case 409:
		return "Состояние файла изменилось; попробуйте продолжить загрузку"
	case 429:
		return "Сервер занят. Повторите операцию позже"
	case 507:
		return "В хранилище недостаточно свободного места"
	case 503:
		return "Хранилище сейчас недоступно"
	default:
		return fmt.Sprintf("Сервер вернул HTTP %d", e.Status)
	}
}
func retryable(err error) bool {
	var h *HTTPError
	if errors.As(err, &h) {
		return h.Status == 429 || h.Status >= 500 && h.Status != 507
	}
	return err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded)
}
func (c *Client) request(ctx context.Context, method, path string, body io.Reader, size int64, headers map[string]string) (*http.Response, error) {
	if !strings.HasPrefix(path, "/api/v1/") || strings.ContainsAny(path, "\r\n") {
		return nil, errors.New("Некорректный API-путь")
	}
	r, err := http.NewRequestWithContext(ctx, method, c.origin+path, body)
	if err != nil {
		return nil, err
	}
	if size >= 0 {
		r.ContentLength = size
	}
	if c.token != "" {
		r.Header.Set("Authorization", "Bearer "+c.token)
	}
	for k, v := range headers {
		r.Header.Set(k, v)
	}
	resp, err := c.http.Do(r)
	if err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return nil, requestTimeout{}
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		// Do not include request URLs or transport errors that might contain capabilities.
		return nil, errors.New("Не удалось связаться с сервером. Проверьте сеть и сертификат HTTPS")
	}
	return resp, nil
}
func failure(resp *http.Response) error {
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 65536))
	seconds, _ := strconv.Atoi(resp.Header.Get("Retry-After"))
	return &HTTPError{resp.StatusCode, time.Duration(min(max(seconds, 0), 60)) * time.Second}
}
func (c *Client) JSON(ctx context.Context, method, path string, input, output any) error {
	return c.jsonHeaders(ctx, method, path, input, output, map[string]string{"Content-Type": "application/json"})
}
func (c *Client) jsonHeaders(ctx context.Context, method, path string, input, output any, headers map[string]string) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	var body io.Reader
	if input != nil {
		b, err := json.Marshal(input)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	resp, err := c.request(ctx, method, path, body, -1, headers)
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return failure(resp)
	}
	defer resp.Body.Close()
	if output == nil {
		return nil
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(output); err != nil {
		return errors.New("Некорректный ответ сервера")
	}
	return nil
}

type User struct {
	ID       string `json:"id"`
	Username string `json:"username"`
	HomeID   string `json:"home_id"`
}
type Session struct {
	User User `json:"user"`
}

func (c *Client) Me(ctx context.Context) (Session, error) {
	var s Session
	err := c.JSON(ctx, "GET", "/api/v1/me", nil, &s)
	return s, err
}

type Node struct {
	ID       string `json:"id"`
	ParentID string `json:"parent_id"`
	Name     string `json:"name"`
	Kind     string `json:"kind"`
	Role     int    `json:"effective_role"`
}

func (c *Client) Folders(ctx context.Context, parent string) ([]Node, error) {
	result := []Node{}
	for offset := 0; ; offset += 100 {
		var nodes []Node
		err := c.JSON(ctx, "GET", "/api/v1/folders?parent_id="+url.QueryEscape(parent)+"&limit=100&offset="+strconv.Itoa(offset), nil, &nodes)
		if err != nil {
			return nil, err
		}
		for _, n := range nodes {
			if n.Kind == "folder" {
				result = append(result, n)
			}
		}
		if len(nodes) < 100 {
			return result, nil
		}
		if offset >= 99900 {
			return nil, errors.New("Слишком много объектов в папке; выберите другую папку в Web UI")
		}
	}
}
func (c *Client) Folder(ctx context.Context, id string) (Node, error) {
	var n Node
	err := c.JSON(ctx, "GET", "/api/v1/nodes/"+url.PathEscape(id), nil, &n)
	return n, err
}

type Pairing struct {
	Request struct {
		ID      string `json:"id"`
		Secret  string `json:"secret"`
		Expires int64  `json:"expires_at"`
	} `json:"request"`
	URL      string `json:"verification_url"`
	Interval int    `json:"interval"`
}

func (c *Client) StartPairing(ctx context.Context, name string) (Pairing, error) {
	var p Pairing
	err := c.JSON(ctx, "POST", "/api/v1/device-requests", map[string]string{"name": name}, &p)
	if err != nil {
		return p, err
	}
	u, e := url.Parse(p.URL)
	if e != nil || u.Scheme+"://"+u.Host != c.origin || u.User != nil || u.Path != "/device/connect" || u.Fragment != "" || u.Query().Get("request") != p.Request.ID || !identifier.MatchString(p.Request.ID) || p.Request.Secret == "" {
		return Pairing{}, errors.New("Сервер вернул некорректный адрес подключения")
	}
	p.Interval = max(p.Interval, 12)
	return p, nil
}
func (c *Client) PollPairing(ctx context.Context, id, secret string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if !identifier.MatchString(id) {
		return "", errors.New("Некорректный запрос подключения")
	}
	b, _ := json.Marshal(map[string]string{"secret": secret})
	r, err := c.request(ctx, "POST", "/api/v1/device-requests/"+id+"/token", bytes.NewReader(b), int64(len(b)), map[string]string{"Content-Type": "application/json"})
	if err != nil {
		return "", err
	}
	if r.StatusCode == 202 {
		r.Body.Close()
		return "", nil
	}
	if r.StatusCode != 200 {
		return "", failure(r)
	}
	defer r.Body.Close()
	var out struct {
		Token string `json:"token"`
	}
	if json.NewDecoder(io.LimitReader(r.Body, 65536)).Decode(&out) != nil || out.Token == "" {
		return "", errors.New("Некорректный ответ подключения")
	}
	return out.Token, nil
}

var identifier = regexp.MustCompile(`^[A-Za-z0-9_-]{43}$`)
var uploadPattern = regexp.MustCompile(`^/api/v1/uploads/[A-Za-z0-9_-]{43}$`)

func UploadPath(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.RawPath != "" || !uploadPattern.MatchString(u.Path) {
		return "", errors.New("Сервер вернул некорректный адрес загрузки")
	}
	// The backend advertises its public origin. Only take the resource path; keep our trusted origin.
	return u.Path, nil
}

type ShareOptions struct {
	Days     int    `json:"days"`
	Never    bool   `json:"never"`
	Password string `json:"password"`
}
type Link struct {
	URL   string `json:"url"`
	Share struct {
		ID        string `json:"id"`
		CreatedAt int64  `json:"created_at"`
		ExpiresAt *int64 `json:"expires_at,omitempty"`
	} `json:"share"`
}

func (c *Client) Share(ctx context.Context, id string, opts ShareOptions) (Link, error) {
	var out Link
	if !identifier.MatchString(id) {
		return out, errors.New("Некорректный ID файла")
	}
	if opts.Password != "" && (len(opts.Password) < 12 || len(opts.Password) > 256) {
		return out, errors.New("Пароль ссылки должен занимать от 12 до 256 байт UTF-8")
	}
	in := map[string]any{"node_id": id, "password": opts.Password, "never_expires": opts.Never}
	if !opts.Never {
		if opts.Days < 1 || opts.Days > 365 {
			return out, errors.New("Выберите срок ссылки от 1 до 365 дней")
		}
		in["expires_at"] = time.Now().Add(time.Duration(opts.Days) * 24 * time.Hour).Unix()
	}
	// A share is intentionally never retried automatically: its secret is returned only once.
	err := c.JSON(ctx, "POST", "/api/v1/shares", in, &out)
	if err == nil {
		u, e := url.Parse(out.URL)
		if e != nil || u.User != nil || u.RawQuery != "" || u.Fragment != "" || !regexp.MustCompile(`^/s/[A-Za-z0-9_-]{43}$`).MatchString(u.Path) || u.Scheme+"://"+u.Host != c.origin {
			return Link{}, errors.New("Сервер вернул некорректную публичную ссылку")
		}
	}
	return out, err
}

func (c *Client) RevokeShare(ctx context.Context, id string) error {
	if !identifier.MatchString(id) {
		return errors.New("Некорректный ID ссылки")
	}
	return c.JSON(ctx, "DELETE", "/api/v1/shares/"+id, nil, nil)
}
