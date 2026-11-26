package server

import (
	"encoding/csv"
	"io"
	"log"
	"sync/atomic"
	"time"
)

type csvLog struct {
	writer *csv.Writer
}

func newCSVLog(output io.Writer, header []string) (*csvLog, error) {
	writer := csv.NewWriter(output)
	writer.Comma = ';'
	logger := &csvLog{writer: writer}
	if header != nil {
		if err := logger.write(header); err != nil {
			return nil, err
		}
	}
	return logger, nil
}

func (l *csvLog) write(row []string) error {
	if err := l.writer.Write(row); err != nil {
		return err
	}
	l.writer.Flush()
	return l.writer.Error()
}

type queryLog struct {
	dropped uint64
	rows    chan []string
	done    chan struct{}
}

func startQueryLog(output io.Writer, capacity int) *queryLog {
	l := &queryLog{rows: make(chan []string, capacity), done: make(chan struct{})}
	go l.run(output)
	return l
}

func (l *queryLog) enqueue(row []string) {
	select {
	case l.rows <- row:
	default:
		atomic.AddUint64(&l.dropped, 1)
	}
}

func (l *queryLog) close() {
	close(l.rows)
	select {
	case <-l.done:
	case <-time.After(time.Second):
	}
}

func (l *queryLog) reportDropped() {
	if n := atomic.SwapUint64(&l.dropped, 0); n > 0 {
		log.Printf("dropped %d query log rows because the buffer was full", n)
	}
}

func (l *queryLog) run(output io.Writer) {
	defer close(l.done)
	writer, err := newCSVLog(output, queryHeader)
	if err != nil {
		log.Printf("start query log: %v", err)
		return
	}
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	defer l.reportDropped()
	for {
		select {
		case row, open := <-l.rows:
			if !open {
				return
			}
			if err := writer.write(row); err != nil {
				log.Printf("write query log: %v", err)
				return
			}
		case <-ticker.C:
			l.reportDropped()
		}
	}
}
