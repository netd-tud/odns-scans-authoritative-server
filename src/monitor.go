package server

import (
	"log"
	"os"
	"strconv"
	"sync"
	"time"
)

const loadInterval = 5 * time.Minute

var loadHeader = []string{
	"window_end_utc",
	"day_utc",
	"avg_requests_per_second",
	"unique_ips_5m",
	"requests_5m",
	"unique_ips_day",
	"requests_day",
}

type requestCounts struct {
	requests uint64
	ips      map[string]struct{}
}

func (c *requestCounts) add(ip string) {
	if c.ips == nil {
		c.ips = make(map[string]struct{})
	}
	c.requests++
	c.ips[ip] = struct{}{}
}

type loadCounts struct {
	windowEnd time.Time
	window    requestCounts
	day       requestCounts
	completed [][]string
}

func newLoadCounts(now time.Time) loadCounts {
	return loadCounts{windowEnd: now.UTC().Truncate(loadInterval).Add(loadInterval)}
}

func (c *loadCounts) record(ip string) {
	c.window.add(ip)
	c.day.add(ip)
}

func (c *loadCounts) advance(now time.Time) {
	for !now.Before(c.windowEnd) {
		day := c.windowEnd.Add(-loadInterval).Format("2006-01-02")
		row := []string{
			c.windowEnd.Format(time.RFC3339),
			day,
			strconv.FormatFloat(float64(c.window.requests)/loadInterval.Seconds(), 'f', 6, 64),
			strconv.Itoa(len(c.window.ips)),
			strconv.FormatUint(c.window.requests, 10),
			strconv.Itoa(len(c.day.ips)),
			strconv.FormatUint(c.day.requests, 10),
		}
		c.completed = append(c.completed, row)
		c.window = requestCounts{}
		if c.windowEnd.Hour() == 0 && c.windowEnd.Minute() == 0 {
			c.day = requestCounts{}
		}
		c.windowEnd = c.windowEnd.Add(loadInterval)
	}
}

type loadMonitor struct {
	mu     sync.Mutex
	counts loadCounts
	now    func() time.Time
	stop   chan struct{}
	done   chan struct{}
}

func newLoadMonitor(now func() time.Time) *loadMonitor {
	return &loadMonitor{
		counts: newLoadCounts(now()),
		now:    now,
		stop:   make(chan struct{}),
		done:   make(chan struct{}),
	}
}

func startLoadMonitor(path string) (*loadMonitor, error) {
	file, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return nil, err
	}
	stat, err := file.Stat()
	if err != nil {
		file.Close()
		return nil, err
	}

	var header []string
	if stat.Size() == 0 {
		header = loadHeader
	}
	writer, err := newCSVLog(file, header)
	if err != nil {
		file.Close()
		return nil, err
	}

	m := newLoadMonitor(time.Now)
	go func() {
		defer close(m.done)
		defer file.Close()
		m.collect(writer.write)
	}()
	return m, nil
}

func (m *loadMonitor) record(ip string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	// Sample and count together so a reporting tick cannot close this window in between.
	m.counts.advance(m.now())
	m.counts.record(ip)
}

func (m *loadMonitor) close() {
	close(m.stop)
	<-m.done
}

func (m *loadMonitor) flush(write func([]string) error) time.Time {
	m.mu.Lock()
	m.counts.advance(m.now())
	rows, next := m.counts.completed, m.counts.windowEnd
	m.counts.completed = nil
	m.mu.Unlock()

	for _, row := range rows {
		if err := write(row); err != nil {
			log.Printf("write load log: %v", err)
		}
	}
	return next
}

func (m *loadMonitor) collect(write func([]string) error) {
	timer := time.NewTimer(time.Until(m.flush(write)))
	defer timer.Stop()
	for {
		select {
		case <-m.stop:
			m.flush(write)
			return
		case <-timer.C:
			timer.Reset(time.Until(m.flush(write)))
		}
	}
}
