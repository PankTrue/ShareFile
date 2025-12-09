package database

import (
	"log"
	"os"

	"ShareFile/models"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

var DB *gorm.DB

// InitDB инициализирует подключение к базе данных
func InitDB() error {
	var err error
	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		// Значение по умолчанию для локальной разработки
		dbURL = "postgres://postgres:postgres@localhost:5432/filedb?sslmode=disable"
	}

	DB, err = gorm.Open(postgres.Open(dbURL), &gorm.Config{})
	if err != nil {
		return err
	}

	// Инициализация таблицы
	if err = initTable(); err != nil {
		return err
	}

	log.Println("База данных успешно подключена")
	return nil
}

// CloseDB закрывает подключение к базе данных
func CloseDB() error {
	if DB != nil {
		sqlDB, err := DB.DB()
		if err != nil {
			return err
		}
		return sqlDB.Close()
	}
	return nil
}

// initTable создает таблицу для хранения файлов, если она не существует
func initTable() error {
	return DB.AutoMigrate(&models.File{})
}
