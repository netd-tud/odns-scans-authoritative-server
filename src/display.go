package server

import (
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"golang.org/x/term"
)

func (m *loadMonitor) summaryLines() []string {
	m.mu.Lock()
	now := m.now()
	m.counts.advance(now)
	counts := &m.counts
	windowEnd := counts.windowEnd
	windowRequests, windowIPs, windowPeak := counts.window.requests, len(counts.window.ips), counts.peak
	dayRequests, dayIPs := counts.day.requests, len(counts.day.ips)
	lastRate := counts.lastRate
	m.mu.Unlock()

	previous := "Last 5m: waiting for first completed window"
	if lastRate != nil {
		previous = fmt.Sprintf("Last 5m: avg %.3f req/s, peak %d req/s", lastRate.average, lastRate.peak)
	}
	return []string{
		fmt.Sprintf("5m to %s UTC: %d req, %d IPs, peak %d req/s", windowEnd.Format("15:04"), windowRequests, windowIPs, windowPeak),
		previous,
		fmt.Sprintf("%s UTC: %d req, %d IPs", now.UTC().Format("2006-01-02"), dayRequests, dayIPs),
	}
}

func startLoadDisplay(output *os.File, monitor *loadMonitor, diagnostics *log.Logger) func() {
	fd := int(output.Fd())
	if os.Getenv("TERM") == "dumb" || !term.IsTerminal(fd) {
		return func() {}
	}
	stop, done := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		rendered := false
		for {
			lines := monitor.summaryLines()
			width, height, err := term.GetSize(fd)
			if err == nil && width > 1 && height > len(lines) {
				if _, err := output.WriteString(formatLoadDisplay(lines, width, rendered)); err != nil {
					diagnostics.Printf("write load summary: %v", err)
					return
				}
				rendered = true
			}
			select {
			case <-stop:
				return
			case <-ticker.C:
			}
		}
	}()
	return func() {
		close(stop)
		select {
		case <-done:
		case <-time.After(time.Second):
		}
	}
}

func formatLoadDisplay(lines []string, width int, replace bool) string {
	var output strings.Builder
	if replace {
		fmt.Fprintf(&output, "\x1b[%dF", len(lines))
	}
	for _, line := range lines {
		if len(line) >= width {
			line = line[:width-1]
		}
		output.WriteString("\r\x1b[2K")
		output.WriteString(line)
		output.WriteByte('\n')
	}
	return output.String()
}
