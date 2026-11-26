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

func startQueryLog(output *rotatingQueryLog, diagnostics *log.Logger, capacity int) *queryLog {
	l := &queryLog{rows: make(chan []string, capacity), done: make(chan struct{})}
	go l.run(output, diagnostics)
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
	<-l.done
}

func (l *queryLog) reportDropped(diagnostics *log.Logger) {
	if n := atomic.SwapUint64(&l.dropped, 0); n > 0 {
		diagnostics.Printf("dropped %d query log rows because the buffer was full", n)
	}
}

func (l *queryLog) run(output *rotatingQueryLog, diagnostics *log.Logger) {
	defer close(l.done)
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	defer l.reportDropped(diagnostics)
	var lastWriteError time.Time
	for {
		select {
		case row, open := <-l.rows:
			if !open {
				return
			}
			if err := output.write(row); err != nil && time.Since(lastWriteError) >= time.Minute {
				diagnostics.Printf("write query log: %v", err)
				lastWriteError = time.Now()
			}
		case <-ticker.C:
			l.reportDropped(diagnostics)
		}
	}
}
