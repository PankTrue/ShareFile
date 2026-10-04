// Package logging bounds disk usage without an external logging service.
package logging

import (
	"fmt"
	"os"
	"sync"
)

const MaxBytes = 1 << 20

type Writer struct {
	mu   sync.Mutex
	file *os.File
	path string
	size int64
}

func Open(path string) (*Writer, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return nil, err
	}
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		f.Close()
		return nil, fmt.Errorf("regular log file required")
	}
	return &Writer{file: f, path: path, size: info.Size()}, nil
}
func (w *Writer) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(p) > MaxBytes {
		return 0, fmt.Errorf("log entry exceeds storage budget")
	}
	if w.size+int64(len(p)) > MaxBytes {
		if err := w.file.Close(); err != nil {
			return 0, err
		}
		if err := os.Rename(w.path, w.path+".1"); err != nil {
			return 0, err
		}
		f, err := os.OpenFile(w.path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			return 0, err
		}
		w.file, w.size = f, 0
	}
	n, err := w.file.Write(p)
	w.size += int64(n)
	return n, err
}
func (w *Writer) Close() error { w.mu.Lock(); defer w.mu.Unlock(); return w.file.Close() }
