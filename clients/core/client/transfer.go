package client

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

// Job is a credential-free checkpoint. Persist it before starting network I/O.
// A platform adapter schedules jobs and persists each callback atomically.
type Job struct {
	ID          string `json:"id"`
	Source      string `json:"source"`
	Name        string `json:"name"`
	Size        int64  `json:"size"`
	Modified    int64  `json:"modified"`
	Fingerprint string `json:"fingerprint"`
	Folder      string `json:"folder"`
	Upload      string `json:"upload"`
	FileID      string `json:"file_id"`
	Offset      int64  `json:"offset"`
}

func randomID() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}
func fingerprint(f *os.File, size, modified int64) (string, error) {
	h := sha256.New()
	fmt.Fprintf(h, "%d:%d:", size, modified)
	// Bounded samples detect accidental source replacement without pre-reading a multi-TB file.
	for _, offset := range []int64{0, max(0, size/2-32768), max(0, size-65536)} {
		if _, err := io.Copy(h, io.NewSectionReader(f, offset, min(65536, size-offset))); err != nil {
			return "", err
		}
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
func Prepare(path, folder string) (Job, error) {
	if !identifier.MatchString(folder) {
		return Job{}, errors.New("Выберите папку назначения")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return Job{}, errors.New("Некорректный путь к файлу")
	}
	f, err := os.Open(abs)
	if err != nil {
		return Job{}, errors.New("Не удалось открыть файл. Проверьте разрешение macOS на доступ")
	}
	defer f.Close()
	s, err := f.Stat()
	if err != nil || !s.Mode().IsRegular() {
		return Job{}, errors.New("Можно загружать только обычные файлы; папки пока не поддерживаются")
	}
	stamp := s.ModTime().UnixNano()
	fp, err := fingerprint(f, s.Size(), stamp)
	if err != nil {
		return Job{}, errors.New("Не удалось прочитать файл")
	}
	return Job{ID: randomID(), Source: abs, Name: filepath.Base(abs), Size: s.Size(), Modified: stamp, Fingerprint: fp, Folder: folder}, nil
}
func validateJob(j Job) error {
	if !identifier.MatchString(j.ID) || !identifier.MatchString(j.Folder) || j.Size < 0 || j.Offset < 0 || j.Offset > j.Size || !filepath.IsAbs(j.Source) || j.Name != filepath.Base(j.Source) || len(j.Fingerprint) != 64 {
		return errors.New("Некорректное состояние загрузки")
	}
	if j.Upload != "" {
		if _, err := UploadPath(j.Upload); err != nil {
			return err
		}
	}
	return nil
}
func tusHeaders() map[string]string { return map[string]string{"Tus-Resumable": "1.0.0"} }
func (c *Client) create(ctx context.Context, j *Job) error {
	h := tusHeaders()
	h["Upload-Length"] = strconv.FormatInt(j.Size, 10)
	h["Upload-Metadata"] = "filename " + base64.StdEncoding.EncodeToString([]byte(j.Name)) + ",folder_id " + base64.StdEncoding.EncodeToString([]byte(j.Folder))
	h["Idempotency-Key"] = j.ID
	r, err := c.request(ctx, "POST", "/api/v1/uploads", nil, 0, h)
	if err != nil {
		return err
	}
	if r.StatusCode != 201 {
		return failure(r)
	}
	defer r.Body.Close()
	path, err := UploadPath(r.Header.Get("Location"))
	if err != nil {
		return err
	}
	j.Upload = path
	j.FileID = r.Header.Get("Sharefile-File-ID")
	if !identifier.MatchString(j.FileID) {
		return errors.New("Сервер не вернул ID загруженного файла")
	}
	return nil
}

type checkpoint struct {
	Offset, Length, Chunk int64
	FileID, State         string
}

func (c *Client) head(ctx context.Context, path string) (checkpoint, error) {
	r, err := c.request(ctx, "HEAD", path, nil, 0, tusHeaders())
	if err != nil {
		return checkpoint{}, err
	}
	if r.StatusCode != 200 {
		return checkpoint{}, failure(r)
	}
	defer r.Body.Close()
	o, e1 := strconv.ParseInt(r.Header.Get("Upload-Offset"), 10, 64)
	l, e2 := strconv.ParseInt(r.Header.Get("Upload-Length"), 10, 64)
	n, e3 := strconv.ParseInt(r.Header.Get("Sharefile-Chunk-Size"), 10, 64)
	id, state := r.Header.Get("Sharefile-File-ID"), r.Header.Get("Sharefile-Upload-State")
	if e1 != nil || e2 != nil || e3 != nil || o < 0 || o > l || n < 1 || !identifier.MatchString(id) || (state != "open" && state != "committing" && state != "complete") {
		return checkpoint{}, errors.New("Некорректное состояние загрузки на сервере")
	}
	return checkpoint{o, l, min(n, 4<<20), id, state}, nil
}
func waitRetry(ctx context.Context, err error, attempt int) error {
	delay := time.Duration(min(1<<attempt, 8)) * time.Second
	var h *HTTPError
	if errors.As(err, &h) && h.RetryAfter > delay {
		delay = h.RetryAfter
	}
	t := time.NewTimer(delay)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
func retry(ctx context.Context, fn func() error) error {
	for attempt := 0; ; attempt++ {
		err := fn()
		if err == nil || !retryable(err) || attempt >= 4 {
			return err
		}
		if err = waitRetry(ctx, err, attempt); err != nil {
			return err
		}
	}
}

// Upload always reconciles with HEAD after an interrupted request. Only the durable server offset wins.
// Progress reports accepted bytes, rather than bytes merely written into a socket.
func (c *Client) Upload(ctx context.Context, j *Job, progress func(Job)) error {
	if err := validateJob(*j); err != nil {
		return err
	}
	f, err := os.Open(j.Source)
	if err != nil {
		return errors.New("Исходный файл недоступен. Проверьте разрешение или расположение файла")
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return errors.New("Не удалось прочитать свойства файла")
	}
	if !info.Mode().IsRegular() || info.Size() != j.Size || info.ModTime().UnixNano() != j.Modified {
		return errors.New("Исходный файл изменился. Отмените эту загрузку и добавьте файл заново")
	}
	fp, err := fingerprint(f, j.Size, j.Modified)
	if err != nil || fp != j.Fingerprint {
		return errors.New("Исходный файл изменился. Отмените эту загрузку и добавьте файл заново")
	}
	n, err := c.Folder(ctx, j.Folder)
	if err != nil {
		return err
	}
	if n.Kind != "folder" || n.Role < 2 {
		return errors.New("Нет права загружать в выбранную папку")
	}
	if j.Upload == "" {
		if err = retry(ctx, func() error { return c.create(ctx, j) }); err != nil {
			return err
		}
		progress(*j)
	}
	failures := 0
	for {
		var cp checkpoint
		err = retry(ctx, func() error { var e error; cp, e = c.head(ctx, j.Upload); return e })
		if err != nil {
			return err
		}
		if cp.Length != j.Size || cp.FileID != j.FileID {
			return errors.New("Состояние загрузки не соответствует исходному файлу")
		}
		j.Offset = cp.Offset
		progress(*j)
		if cp.State == "complete" {
			if cp.Offset != j.Size {
				return errors.New("Сервер не подтвердил завершение файла")
			}
			return nil
		}
		if cp.State == "committing" {
			if failures >= 4 {
				return errors.New("Сервер ещё публикует файл. Продолжите позже")
			}
			if err = waitRetry(ctx, nil, failures); err != nil {
				return err
			}
			failures++
			continue
		}
		if cp.Offset == j.Size {
			return errors.New("Сервер ещё не подтвердил завершение файла. Продолжите позже")
		}
		current, e := f.Stat()
		if e != nil || current.Size() != j.Size || current.ModTime().UnixNano() != j.Modified {
			return errors.New("Файл изменился во время передачи. Отмените загрузку")
		}
		length := min(cp.Chunk, j.Size-cp.Offset)
		h := tusHeaders()
		h["Upload-Offset"] = strconv.FormatInt(cp.Offset, 10)
		h["Content-Type"] = "application/offset+octet-stream"
		h["X-HTTP-Method-Override"] = "PATCH"
		// POST override works through KeenDNS proxies that do not pass PATCH.
		r, e := c.request(ctx, "POST", j.Upload, io.NewSectionReader(f, cp.Offset, length), length, h)
		if e == nil {
			if r.StatusCode != 204 {
				e = failure(r)
			} else {
				r.Body.Close()
			}
		}
		if e != nil {
			var conflict *HTTPError
			if !retryable(e) && !(errors.As(e, &conflict) && conflict.Status == 409) {
				return e
			}
			if failures >= 4 {
				return e
			}
			if e = waitRetry(ctx, e, failures); e != nil {
				return e
			}
			failures++
		} else {
			failures = 0
		}
		// HEAD also handles a lost final PATCH response and a partial body committed on disconnect.
	}
}
func (c *Client) Cancel(ctx context.Context, j *Job) error {
	if err := validateJob(*j); err != nil {
		return err
	}
	// Repeating creation with the original idempotency key finds a lost creation response.
	if j.Upload == "" {
		if err := retry(ctx, func() error { return c.create(ctx, j) }); err != nil {
			return err
		}
	}
	return retry(ctx, func() error {
		h := tusHeaders()
		h["X-HTTP-Method-Override"] = "DELETE"
		r, err := c.request(ctx, "POST", j.Upload, nil, 0, h)
		if err != nil {
			return err
		}
		if r.StatusCode == 204 || r.StatusCode == 404 {
			r.Body.Close()
			return nil
		}
		return failure(r)
	})
}
