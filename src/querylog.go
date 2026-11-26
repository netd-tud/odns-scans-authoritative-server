package server

import (
	"bytes"
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	// DefaultQueryLog is the active query CSV path.
	DefaultQueryLog = "logs/queries.csv"
	// DefaultQueryLogMaxMB is the query log rotation threshold in decimal megabytes.
	DefaultQueryLogMaxMB = 100
	bytesPerMB           = 1000000
)

type rotatingQueryLog struct {
	path     string
	maxBytes int64
	file     *os.File
	size     int64
	buffer   bytes.Buffer
	encoder  *csvLog
	header   []byte
}

func resolveQueryLogPath(path string) (string, error) {
	if path == "" {
		return "", fmt.Errorf("query log path is empty")
	}
	if filepath.Base(path) == path {
		path = filepath.Join(filepath.Dir(DefaultQueryLog), path)
	}
	return filepath.Abs(path)
}

func openQueryLog(path string, maxMB int64) (*rotatingQueryLog, error) {
	if maxMB < 1 || maxMB > (1<<63-1)/bytesPerMB {
		return nil, fmt.Errorf("invalid query_log_max_mb %d, expected a positive size in MB", maxMB)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0750); err != nil {
		return nil, err
	}
	l := &rotatingQueryLog{path: path, maxBytes: maxMB * bytesPerMB}
	var err error
	l.encoder, err = newCSVLog(&l.buffer, queryHeader)
	if err != nil {
		return nil, err
	}
	l.header = append([]byte(nil), l.buffer.Bytes()...)
	if err := l.open(); err != nil {
		return nil, err
	}
	return l, nil
}

func (l *rotatingQueryLog) open() error {
	file, err := os.OpenFile(l.path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0640)
	if err != nil {
		return err
	}
	info, err := file.Stat()
	if err != nil {
		file.Close()
		return err
	}
	l.file, l.size = file, info.Size()
	if l.size == 0 {
		if err := l.append(l.header); err != nil {
			l.close()
			return err
		}
	}
	return nil
}

func (l *rotatingQueryLog) close() error {
	if l.file == nil {
		return nil
	}
	err := l.file.Close()
	l.file = nil
	return err
}

func (l *rotatingQueryLog) append(data []byte) error {
	n, err := l.file.Write(data)
	if err != nil {
		if n > 0 {
			if truncateErr := l.file.Truncate(l.size); truncateErr != nil {
				l.size += int64(n)
				return fmt.Errorf("append: %v (roll back partial write: %w)", err, truncateErr)
			}
		}
		return err
	}
	l.size += int64(n)
	return nil
}

func (l *rotatingQueryLog) rotate() error {
	if err := l.close(); err != nil {
		return err
	}
	if err := archiveLog(l.path); err != nil {
		return err
	}
	return l.open()
}

func archiveLog(path string) error {
	extension := filepath.Ext(path)
	stem := strings.TrimSuffix(filepath.Base(path), extension)
	stamp := time.Now().UTC().Format("20060102T150405Z")
	// Reserve a unique filename, including when several rotations share a timestamp.
	archive, err := ioutil.TempFile(filepath.Dir(path), stem+"-"+stamp+"-*"+extension)
	if err != nil {
		return err
	}
	archivePath := archive.Name()
	if err := archive.Close(); err != nil {
		os.Remove(archivePath)
		return err
	}
	if err := os.Rename(path, archivePath); err != nil {
		os.Remove(archivePath)
		return err
	}
	return nil
}

func (l *rotatingQueryLog) write(row []string) error {
	l.buffer.Reset()
	if err := l.encoder.write(row); err != nil {
		return err
	}
	if l.file == nil {
		if err := l.open(); err != nil {
			return err
		}
	}
	if l.size > int64(len(l.header)) && int64(l.buffer.Len()) > l.maxBytes-l.size {
		if err := l.rotate(); err != nil {
			return err
		}
	}
	return l.append(l.buffer.Bytes())
}
