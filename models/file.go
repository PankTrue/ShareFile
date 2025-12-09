package models

import "time"

// File представляет файл в базе данных
type File struct {
	ID          int64     `gorm:"primaryKey;autoIncrement" json:"id"`
	FileName    string    `gorm:"type:varchar(255);not null" json:"file_name"`
	FileSize    int64     `gorm:"type:bigint;not null" json:"file_size"`
	ContentType string    `gorm:"type:varchar(100)" json:"content_type"`
	Data        []byte    `gorm:"type:bytea;not null" json:"-"`
	UploadedAt  time.Time `gorm:"default:CURRENT_TIMESTAMP" json:"uploaded_at"`
}

// TableName указывает имя таблицы для GORM
func (File) TableName() string {
	return "files"
}
