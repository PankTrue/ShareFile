package models

import "time"

// File представляет файл в базе данных
type File struct {
	ID          int64     `json:"id"`
	FileName    string    `json:"file_name"`
	FileSize    int64     `json:"file_size"`
	ContentType string    `json:"content_type"`
	Data        []byte    `json:"-"` // не включаем в JSON для экономии места
	UploadedAt  time.Time `json:"uploaded_at"`
}
