package httpapi

import (
	"fmt"
	"net/http"
)

// Language is an explicit browser preference. New browsers always start in English.
func preferredLanguage(r *http.Request) string {
	if value := r.URL.Query().Get("lang"); value == "en" || value == "ru" {
		return value
	}
	if cookie, err := r.Cookie("sf_language"); err == nil && cookie.Value == "ru" {
		return "ru"
	}
	return "en"
}

func shareText(language, text string) string {
	if language == "ru" {
		return text
	}
	if translated, ok := shareEnglish[text]; ok {
		return translated
	}
	return text
}

func shareSize(language string, size int64) string {
	if size < 1024 {
		return fmt.Sprintf("%d %s", size, shareText(language, "Б"))
	}
	value := float64(size)
	for _, unit := range []string{"КиБ", "МиБ", "ГиБ", "ТиБ", "ПиБ", "ЭиБ"} {
		value /= 1024
		if value < 1024 {
			return fmt.Sprintf("%.1f %s", value, shareText(language, unit))
		}
	}
	return fmt.Sprintf("%d %s", size, shareText(language, "Б"))
}

func languageURL(r *http.Request, language string) string {
	query := r.URL.Query()
	query.Set("lang", language)
	return r.URL.Path + "?" + query.Encode()
}

var shareEnglish = map[string]string{
	"Язык":             "Language",
	"Доступ по ссылке": "Shared link",
	"Пароль ссылки":    "Link password",
	"Введите пароль, который вам передал владелец.": "Enter the password provided by the owner.",
	"Пароль":             "Password",
	"Открыть":            "Open",
	"Путь в общей папке": "Shared folder path",
	"ОБЩАЯ ПАПКА":        "SHARED FOLDER",
	"ПРЕДПРОСМОТР ФАЙЛА": "FILE PREVIEW",
	"Открывайте вложенные папки, просматривайте фото и текст или скачивайте файлы.": "Browse subfolders, preview photos and text, or download files.",
	"Скачать всё (ZIP)":            "Download all (ZIP)",
	"Скачать файл":                 "Download file",
	"Вернуться в папку":            "Back to folder",
	"Содержимое папки":             "Folder contents",
	"Название":                     "Name",
	"Размер":                       "Size",
	"Папка":                        "Folder",
	"Открыть предпросмотр":         "Open preview",
	"Скачать":                      "Download",
	"В этой папке пока нет файлов": "This folder is empty",
	"Новые файлы появятся здесь, когда владелец добавит их.": "New files will appear here when the owner adds them.",
	"Страницы папки":     "Folder pages",
	"Назад":              "Back",
	"Далее":              "Next",
	"Предпросмотр файла": "File preview",
	"Показаны первые 256 КиБ текста. Скачайте файл, чтобы прочитать его полностью.": "Showing the first 256 KiB of text. Download the file to read it in full.",
	"Файл пуст":               "This file is empty",
	"Предпросмотр недоступен": "Preview unavailable",
	"Предпросмотр этого формата пока недоступен. Скачайте файл, чтобы открыть его.":                        "Preview is not available for this format. Download the file to open it.",
	"Файл содержит двоичные данные или текст в кодировке, отличной от UTF-8. Скачайте его, чтобы открыть.": "This file contains binary data or text that is not UTF-8. Download it to open it.",
	"Содержимое доступно только для просмотра и скачивания.":                                               "Content is available for viewing and downloading only.",
	"Б": "B", "КиБ": "KiB", "МиБ": "MiB", "ГиБ": "GiB", "ТиБ": "TiB", "ПиБ": "PiB", "ЭиБ": "EiB",
	"Выбранные файлы.zip": "Selected files.zip",
}
