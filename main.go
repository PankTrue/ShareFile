package main

import (
	"ShareFile/database"
	"ShareFile/routes"
	"log"
)

func main() {
	// Инициализация базы данных
	if err := database.InitDB(); err != nil {
		log.Fatalf("Ошибка подключения к БД: %v", err)
	}
	defer database.CloseDB()

	// Настройка маршрутов
	router := routes.SetupRoutes()

	log.Println("Сервер запущен на :8080")
	router.Run() // listens on 0.0.0.0:8080 by default
}
