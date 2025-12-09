package handlers

import (
	"fmt"
	"io"
	"strconv"

	"ShareFile/models"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// FileHandler содержит зависимости для обработчиков файлов
type FileHandler struct {
	DB *gorm.DB
}

// NewFileHandler создает новый экземпляр FileHandler
func NewFileHandler(db *gorm.DB) *FileHandler {
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
	fileModel := models.File{
		FileName:    file.Filename,
		FileSize:    file.Size,
		ContentType: contentType,
		Data:        data,
	}

	if err := h.DB.Create(&fileModel).Error; err != nil {
		c.JSON(500, gin.H{"error": fmt.Sprintf("Ошибка сохранения в БД: %v", err)})
		return
	}

	c.JSON(200, gin.H{
		"message":      "Файл успешно загружен",
		"id":           fileModel.ID,
		"file_name":    fileModel.FileName,
		"file_size":    fileModel.FileSize,
		"content_type": fileModel.ContentType,
		"uploaded_at":  fileModel.UploadedAt,
	})
}

// GetFileInfo возвращает информацию о файле без самого содержимого
func (h *FileHandler) GetFileInfo(c *gin.Context) {
	id := c.Param("id")
	fileID, err := strconv.ParseInt(id, 10, 64)
	if err != nil {
		c.JSON(400, gin.H{"error": "Неверный формат ID"})
		return
	}

	var file models.File
	if err := h.DB.Select("id", "file_name", "file_size", "content_type", "uploaded_at").
		First(&file, fileID).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			c.JSON(404, gin.H{"error": "Файл не найден"})
			return
		}
		c.JSON(500, gin.H{"error": fmt.Sprintf("Ошибка получения файла: %v", err)})
		return
	}

	c.JSON(200, file)
}

// DownloadFile возвращает сам файл для скачивания
func (h *FileHandler) DownloadFile(c *gin.Context) {
	id := c.Param("id")
	fileID, err := strconv.ParseInt(id, 10, 64)
	if err != nil {
		c.JSON(400, gin.H{"error": "Неверный формат ID"})
		return
	}

	var file models.File
	if err := h.DB.First(&file, fileID).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			c.JSON(404, gin.H{"error": "Файл не найден"})
			return
		}
		c.JSON(500, gin.H{"error": fmt.Sprintf("Ошибка получения файла: %v", err)})
		return
	}

	// Устанавливаем заголовки для скачивания
	c.Header("Content-Disposition", fmt.Sprintf("attachment; filename=%s", file.FileName))
	c.Header("Content-Type", file.ContentType)
	c.Header("Content-Length", fmt.Sprintf("%d", file.FileSize))

	c.Data(200, file.ContentType, file.Data)
}

// ListFiles возвращает список всех файлов
func (h *FileHandler) ListFiles(c *gin.Context) {
	var files []models.File
	if err := h.DB.Select("id", "file_name", "file_size", "content_type", "uploaded_at").
		Order("uploaded_at DESC").
		Find(&files).Error; err != nil {
		c.JSON(500, gin.H{"error": fmt.Sprintf("Ошибка получения списка файлов: %v", err)})
		return
	}

	c.JSON(200, gin.H{
		"files": files,
		"count": len(files),
	})
}
