package routes

import (
	"ShareFile/database"
	"ShareFile/handlers"

	"github.com/gin-gonic/gin"
)

// SetupRoutes настраивает все маршруты приложения
func SetupRoutes() *gin.Engine {
	router := gin.Default()

	// Создаем обработчик файлов
	fileHandler := handlers.NewFileHandler(database.DB)

	// Эндпоинт для загрузки файла
	router.POST("/upload", fileHandler.UploadFile)

	// Эндпоинт для получения информации о файле
	router.GET("/file/:id", fileHandler.GetFileInfo)

	// Эндпоинт для скачивания файла
	router.GET("/file/:id/download", fileHandler.DownloadFile)

	// Эндпоинт для проверки работоспособности
	router.GET("/ping", func(c *gin.Context) {
		c.JSON(200, gin.H{
			"message": "pong",
		})
	})

	return router
}
