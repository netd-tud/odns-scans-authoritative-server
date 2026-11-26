package server

import (
	"encoding/csv"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
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
	"peak_requests_per_second",
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
	windowEnd      time.Time
	window         requestCounts
	day            requestCounts
	second         int64
	secondRequests uint64
	peak           uint64
	lastRate       *windowRate
	completed      [][]string
}

type windowRate struct {
	average float64
	peak    uint64
}

func newLoadCounts(now time.Time) loadCounts {
	return loadCounts{windowEnd: now.UTC().Truncate(loadInterval).Add(loadInterval)}
}

func (c *loadCounts) record(ip string, now time.Time) {
	c.window.add(ip)
	c.day.add(ip)
	if second := now.Unix(); second != c.second {
		c.second = second
		c.secondRequests = 0
	}
	c.secondRequests++
	if c.secondRequests > c.peak {
		c.peak = c.secondRequests
	}
}

func (c *loadCounts) advance(now time.Time) {
	for !now.Before(c.windowEnd) {
		day := c.windowEnd.Add(-loadInterval).Format("2006-01-02")
		rate := &windowRate{average: float64(c.window.requests) / loadInterval.Seconds(), peak: c.peak}
		row := []string{
			c.windowEnd.Format(time.RFC3339),
			day,
			strconv.FormatFloat(rate.average, 'f', 6, 64),
			strconv.Itoa(len(c.window.ips)),
			strconv.FormatUint(c.window.requests, 10),
			strconv.Itoa(len(c.day.ips)),
			strconv.FormatUint(c.day.requests, 10),
			strconv.FormatUint(rate.peak, 10),
		}
		c.completed = append(c.completed, row)
		c.lastRate = rate
		c.window = requestCounts{}
		c.secondRequests, c.peak = 0, 0
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

func openLoadLog(path string) (*os.File, []string, error) {
	file, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_RDWR, 0644)
	if err != nil {
		return nil, nil, err
	}
	stat, err := file.Stat()
	if err != nil {
		file.Close()
		return nil, nil, err
	}
	if stat.Size() == 0 {
		return file, loadHeader, nil
	}

	reader := csv.NewReader(file)
	reader.Comma = ';'
	header, err := reader.Read()
	if err != nil {
		file.Close()
		return nil, nil, fmt.Errorf("read load log header: %w", err)
	}
	fields := strings.Join(header, ";")
	if fields == strings.Join(loadHeader, ";") {
		return file, nil, nil
	}
	if err := file.Close(); err != nil {
		return nil, nil, err
	}
	if fields != strings.Join(loadHeader[:len(loadHeader)-1], ";") {
		return nil, nil, fmt.Errorf("unexpected header in load log %q", path)
	}
	if _, err := archiveLog(path); err != nil {
		return nil, nil, fmt.Errorf("archive load log before adding peak column: %w", err)
	}
	file, err = os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_EXCL|os.O_WRONLY, stat.Mode().Perm())
	return file, loadHeader, err
}

func startLoadMonitor(path string, diagnostics *log.Logger) (*loadMonitor, error) {
	file, header, err := openLoadLog(path)
	if err != nil {
		return nil, err
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
		m.collect(func(row []string) {
			if err := writer.write(row); err != nil {
				diagnostics.Printf("write load log: %v", err)
			}
		})
	}()
	return m, nil
}

func (m *loadMonitor) record(ip string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	// Sample and count together so a reporting tick cannot close this window in between.
	now := m.now()
	m.counts.advance(now)
	m.counts.record(ip, now)
}

func (m *loadMonitor) close() {
	close(m.stop)
	<-m.done
}

func (m *loadMonitor) flush(write func([]string)) time.Time {
	m.mu.Lock()
	m.counts.advance(m.now())
	rows, next := m.counts.completed, m.counts.windowEnd
	m.counts.completed = nil
	m.mu.Unlock()

	for _, row := range rows {
		write(row)
	}
	return next
}

func (m *loadMonitor) collect(write func([]string)) {
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
