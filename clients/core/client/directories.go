package client

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"math"
	"os"
	"path"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"
)

const maxDirectoryEntries = 10000

type DirectoryEntry struct {
	Path string `json:"path"`
	Key  string `json:"key"`
	ID   string `json:"id"`
}
type DirectoryFile struct {
	Path     string `json:"path"`
	Job      Job    `json:"job"`
	Complete bool   `json:"complete"`
}
type DirectoryState struct {
	Folders []DirectoryEntry `json:"folders"`
	Files   []DirectoryFile  `json:"files"`
}

// One directory is one queue item and one public link. Checkpoints contain no capabilities.
type DirectoryJob struct {
	Job
	Directory *DirectoryState `json:"directory,omitempty"`
}

func validName(name string) bool {
	if !utf8.ValidString(name) || len(name) == 0 || len(name) > 240 || strings.TrimSpace(name) != name || name == "." || name == ".." || strings.ContainsAny(name, "/\\") {
		return false
	}
	for _, r := range name {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}
func PrepareDirectory(source, parent string) (DirectoryJob, error) {
	j := DirectoryJob{}
	abs, err := filepath.Abs(source)
	if err != nil || !identifier.MatchString(parent) {
		return j, errors.New("Выберите папку назначения")
	}
	info, err := os.Lstat(abs)
	if err != nil || !info.IsDir() || !validName(filepath.Base(abs)) {
		return j, errors.New("Не удалось открыть каталог или его имя недопустимо")
	}
	j.Job = Job{ID: randomID(), Source: abs, Name: filepath.Base(abs), Folder: parent}
	j.Directory = &DirectoryState{Folders: []DirectoryEntry{}, Files: []DirectoryFile{}}
	hash := sha256.New()
	err = filepath.WalkDir(abs, func(source string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return errors.New("Не удалось прочитать каталог. Проверьте права доступа")
		}
		if source == abs {
			return nil
		}
		rel, err := filepath.Rel(abs, source)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if !validName(entry.Name()) || strings.Count(rel, "/") >= 128 {
			return errors.New("Недопустимое имя или слишком глубокая вложенность: " + rel)
		}
		if len(j.Directory.Folders)+len(j.Directory.Files) >= maxDirectoryEntries {
			return errors.New("В каталоге больше 10000 объектов. Разделите его на несколько папок")
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return errors.New("Каталог содержит символическую ссылку: " + rel + ". Удалите ссылку из загружаемой папки")
		}
		if entry.IsDir() {
			j.Directory.Folders = append(j.Directory.Folders, DirectoryEntry{Path: rel, Key: randomID()})
			hash.Write([]byte("d\x00" + rel + "\x00"))
			return nil
		}
		if !entry.Type().IsRegular() {
			return errors.New("Каталог содержит специальный файл: " + rel)
		}
		job, err := Prepare(source, parent)
		if err != nil {
			return err
		}
		if job.Size > math.MaxInt64-j.Size {
			return errors.New("Суммарный размер каталога слишком велик")
		}
		j.Size += job.Size
		j.Directory.Files = append(j.Directory.Files, DirectoryFile{Path: rel, Job: job})
		hash.Write([]byte("f\x00" + rel + "\x00" + job.Fingerprint + "\x00"))
		return nil
	})
	if err != nil {
		return DirectoryJob{}, err
	}
	j.Fingerprint = hex.EncodeToString(hash.Sum(nil))
	encoded, err := json.Marshal(j)
	if err != nil || len(encoded) > 8<<20 {
		return DirectoryJob{}, errors.New("Список файлов каталога слишком велик. Разделите каталог на несколько папок")
	}
	return j, nil
}
func (c *Client) createDirectory(ctx context.Context, parent, name, key string) (Node, error) {
	var out Node
	err := retry(ctx, func() error {
		// JSON uses the same origin and refuses redirects; keys survive lost creation responses.
		return c.jsonHeaders(ctx, "POST", "/api/v1/folders", map[string]string{"parent_id": parent, "name": name}, &out, map[string]string{"Content-Type": "application/json", "Idempotency-Key": key})
	})
	if err == nil && (!identifier.MatchString(out.ID) || out.ParentID != parent || out.Kind != "folder") {
		return Node{}, errors.New("Сервер вернул некорректный каталог")
	}
	return out, err
}
func (c *Client) UploadDirectory(ctx context.Context, j *DirectoryJob, progress func(DirectoryJob)) error {
	j.Transferred = 0
	if j.Directory == nil || !identifier.MatchString(j.ID) {
		return errors.New("Некорректное состояние каталога")
	}
	fresh, err := PrepareDirectory(j.Source, j.Folder)
	if err != nil {
		return err
	}
	if fresh.Name != j.Name || fresh.Fingerprint != j.Fingerprint || fresh.Size != j.Size || len(fresh.Directory.Folders) != len(j.Directory.Folders) || len(fresh.Directory.Files) != len(j.Directory.Files) {
		return errors.New("Исходный каталог изменился. Добавьте его заново")
	}
	for i, folder := range j.Directory.Folders {
		if folder.Path != fresh.Directory.Folders[i].Path || !identifier.MatchString(folder.Key) {
			return errors.New("Некорректное состояние каталога")
		}
	}
	for i, file := range j.Directory.Files {
		if file.Path != fresh.Directory.Files[i].Path || file.Job.Source != fresh.Directory.Files[i].Job.Source || file.Job.Fingerprint != fresh.Directory.Files[i].Job.Fingerprint {
			return errors.New("Некорректное состояние файла каталога")
		}
	}
	parent, err := c.Folder(ctx, j.Folder)
	if err != nil {
		return err
	}
	if parent.Kind != "folder" || parent.Role < 3 {
		return errors.New("Для загрузки каталога с общей ссылкой нужны права управляющего")
	}
	if j.FileID == "" {
		node, err := c.createDirectory(ctx, j.Folder, j.Name, j.ID)
		if err != nil {
			return err
		}
		j.FileID = node.ID
		progress(*j)
	}
	remoteRoot, err := c.Folder(ctx, j.FileID)
	if err != nil {
		return err
	}
	if remoteRoot.ParentID != j.Folder || remoteRoot.Kind != "folder" {
		return errors.New("Загружаемая папка перемещена. Проверьте её в Web UI")
	}
	ids := map[string]string{".": j.FileID}
	for i := range j.Directory.Folders {
		folder := &j.Directory.Folders[i]
		parent := ids[path.Dir(folder.Path)]
		if parent == "" {
			return errors.New("Некорректный путь вложенного каталога")
		}
		if folder.ID == "" {
			node, err := c.createDirectory(ctx, parent, path.Base(folder.Path), folder.Key)
			if err != nil {
				return err
			}
			folder.ID = node.ID
			progress(*j)
		}
		node, err := c.Folder(ctx, folder.ID)
		if err != nil {
			return err
		}
		if node.ParentID != parent || node.Kind != "folder" {
			return errors.New("Вложенная папка перемещена. Проверьте её в Web UI")
		}
		ids[folder.Path] = folder.ID
	}
	root, err := os.OpenRoot(j.Source)
	if err != nil {
		return err
	}
	defer root.Close()
	for i := range j.Directory.Files {
		file := &j.Directory.Files[i]
		file.Job.Folder = ids[path.Dir(file.Path)]
		if file.Complete {
			node, err := c.Folder(ctx, file.Job.FileID)
			if err != nil {
				return err
			}
			if node.Kind != "file" || node.ParentID != file.Job.Folder {
				return errors.New("Загруженный файл перемещён. Проверьте папку в Web UI")
			}
			continue
		}
		previousUpload, previousFile := file.Job.Upload, file.Job.FileID
		transferred := j.Transferred
		err = c.upload(ctx, &file.Job, func(updated Job) {
			resourceChanged := updated.Upload != previousUpload || updated.FileID != previousFile
			previousUpload, previousFile = updated.Upload, updated.FileID
			file.Job = updated
			j.Transferred = transferred + updated.Transferred
			j.Offset = 0
			for _, f := range j.Directory.Files {
				j.Offset += f.Job.Offset
			}
			checkpoint := *j
			if !resourceChanged {
				checkpoint.Directory = nil
			}
			progress(checkpoint)
		}, func(string) (*os.File, error) { return root.Open(filepath.FromSlash(file.Path)) })
		if err != nil {
			return err
		}
		file.Complete = true
		progress(*j)
	}
	return nil
}
func (c *Client) CancelDirectory(ctx context.Context, j *DirectoryJob) error {
	if j.Directory == nil {
		return errors.New("Некорректное состояние каталога")
	}
	ids := map[string]string{".": j.FileID}
	for _, folder := range j.Directory.Folders {
		ids[folder.Path] = folder.ID
	}
	for i := range j.Directory.Files {
		file := &j.Directory.Files[i]
		file.Job.Folder = ids[path.Dir(file.Path)]
		if !file.Complete && identifier.MatchString(file.Job.Folder) {
			if err := c.Cancel(ctx, &file.Job); err != nil {
				return err
			}
		}
	}
	// Completed files remain in the trash for the usual retention period.
	node, err := c.createDirectory(ctx, j.Folder, j.Name, j.ID)
	if err != nil {
		return err
	}
	return c.JSON(ctx, "DELETE", "/api/v1/nodes/"+node.ID, nil, nil)
}
