package handlers

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"time"

	"ShareFile/models"

	"github.com/gin-gonic/gin"
)

// FileHandler содержит зависимости для обработчиков файлов
type FileHandler struct {
	DB *sql.DB
}

// NewFileHandler создает новый экземпляр FileHandler
func NewFileHandler(db *sql.DB) *FileHandler {
	return &FileHandler{DB: db}
}

// UploadFile обрабатывает загрузку файла
func (h *FileHandler) UploadFile(c *gin.Context) {
	// Получаем файл из формы
	file, err := c.FormFile("file")
	if err != nil {
		c.JSON(400, gin.H{"error": "Файл не найден в запросе"})
		return
	}

	// Открываем файл
	src, err := file.Open()
	if err != nil {
		c.JSON(500, gin.H{"error": fmt.Sprintf("Ошибка открытия файла: %v", err)})
		return
	}
	defer src.Close()

	// Читаем содержимое файла
	data, err := io.ReadAll(src)
	if err != nil {
		c.JSON(500, gin.H{"error": fmt.Sprintf("Ошибка чтения файла: %v", err)})
		return
	}

	// Определяем content type
	contentType := file.Header.Get("Content-Type")
	if contentType == "" {
		contentType = "application/octet-stream"
	}

	// Сохраняем в базу данных
	ctx := context.Background()
	query := `
		INSERT INTO files (file_name, file_size, content_type, data)
		VALUES ($1, $2, $3, $4)
		RETURNING id, uploaded_at
	`

	var fileID int64
	var uploadedAt time.Time
	err = h.DB.QueryRowContext(ctx, query, file.Filename, file.Size, contentType, data).
		Scan(&fileID, &uploadedAt)

	if err != nil {
		c.JSON(500, gin.H{"error": fmt.Sprintf("Ошибка сохранения в БД: %v", err)})
		return
	}

	c.JSON(200, gin.H{
		"message":      "Файл успешно загружен",
		"id":           fileID,
		"file_name":    file.Filename,
		"file_size":    file.Size,
		"content_type": contentType,
		"uploaded_at":  uploadedAt,
	})
}

// GetFileInfo возвращает информацию о файле без самого содержимого
func (h *FileHandler) GetFileInfo(c *gin.Context) {
	id := c.Param("id")

	var file models.File
	query := `
		SELECT id, file_name, file_size, content_type, uploaded_at
		FROM files
		WHERE id = $1
	`

	err := h.DB.QueryRow(query, id).Scan(
		&file.ID,
		&file.FileName,
		&file.FileSize,
		&file.ContentType,
		&file.UploadedAt,
	)

	if err == sql.ErrNoRows {
		c.JSON(404, gin.H{"error": "Файл не найден"})
		return
	}
	if err != nil {
		c.JSON(500, gin.H{"error": fmt.Sprintf("Ошибка получения файла: %v", err)})
		return
	}

	c.JSON(200, file)
}

// DownloadFile возвращает сам файл для скачивания
func (h *FileHandler) DownloadFile(c *gin.Context) {
	id := c.Param("id")

	var file models.File
	query := `
		SELECT id, file_name, file_size, content_type, data, uploaded_at
		FROM files
		WHERE id = $1
	`

	err := h.DB.QueryRow(query, id).Scan(
		&file.ID,
		&file.FileName,
		&file.FileSize,
		&file.ContentType,
		&file.Data,
		&file.UploadedAt,
	)

	if err == sql.ErrNoRows {
		c.JSON(404, gin.H{"error": "Файл не найден"})
		return
	}
	if err != nil {
		c.JSON(500, gin.H{"error": fmt.Sprintf("Ошибка получения файла: %v", err)})
		return
	}

	// Устанавливаем заголовки для скачивания
	c.Header("Content-Disposition", fmt.Sprintf("attachment; filename=%s", file.FileName))
	c.Header("Content-Type", file.ContentType)
	c.Header("Content-Length", fmt.Sprintf("%d", file.FileSize))

	c.Data(200, file.ContentType, file.Data)
}
