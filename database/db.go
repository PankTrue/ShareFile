package database

import (
	"database/sql"
	"log"
	"os"

	_ "github.com/jackc/pgx/v5/stdlib"
)

var DB *sql.DB

// InitDB инициализирует подключение к базе данных
func InitDB() error {
	var err error
	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		// Значение по умолчанию для локальной разработки
		dbURL = "postgres://postgres:postgres@localhost:5432/filedb?sslmode=disable"
	}

	DB, err = sql.Open("pgx", dbURL)
	if err != nil {
		return err
	}

	// Проверка подключения
	if err = DB.Ping(); err != nil {
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
		return DB.Close()
	}
	return nil
}

// initTable создает таблицу для хранения файлов, если она не существует
func initTable() error {
	query := `
	CREATE TABLE IF NOT EXISTS files (
		id SERIAL PRIMARY KEY,
		file_name VARCHAR(255) NOT NULL,
		file_size BIGINT NOT NULL,
		content_type VARCHAR(100),
		data BYTEA NOT NULL,
		uploaded_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
	);
	`
	_, err := DB.Exec(query)
	return err
}
