package server

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/miekg/dns"
	"golang.org/x/term"
)

type queryTypeCount struct {
	qtype uint16
	count uint64
}

func (m *loadMonitor) summaryLines(maxRows, page int) []string {
	m.mu.Lock()
	now := m.now()
	m.counts.advance(now)
	counts := &m.counts
	windowEnd := counts.windowEnd
	windowRequests, windowIPs, windowPeak := counts.window.requests, len(counts.window.ips), counts.peak
	dayRequests, dayIPs := counts.day.requests, len(counts.day.ips)
	lastRate := counts.lastRate
	queryTypes := make([]queryTypeCount, 0, len(m.queryTypes))
	for qtype, count := range m.queryTypes {
		queryTypes = append(queryTypes, queryTypeCount{qtype, count})
	}
	m.mu.Unlock()

	previous := "Last 5m: awaiting first window"
	if lastRate != nil {
		previous = fmt.Sprintf("Last 5m: avg %.3f/s, peak %d/s", lastRate.average, lastRate.peak)
	}
	lines := []string{
		fmt.Sprintf("5m to %s UTC: %d req, %d IPs", windowEnd.Format("15:04"), windowRequests, windowIPs),
		fmt.Sprintf("Current peak: %d req/s", windowPeak),
		previous,
		fmt.Sprintf("%s UTC: %d req, %d IPs", now.UTC().Format("2006-01-02"), dayRequests, dayIPs),
	}
	typeRows := maxRows - len(lines) - 1
	if typeRows < 1 {
		return lines
	}
	heading := "Query type     Requests (run)"
	if len(queryTypes) == 0 {
		return append(lines, heading, "  None received")
	}
	sort.Slice(queryTypes, func(i, j int) bool {
		return queryTypes[i].qtype < queryTypes[j].qtype
	})
	if len(queryTypes) > typeRows {
		pages := (len(queryTypes) + typeRows - 1) / typeRows
		page %= pages
		heading = fmt.Sprintf("Query type     Requests (%d/%d)", page+1, pages)
		start, end := page*typeRows, (page+1)*typeRows
		if end > len(queryTypes) {
			end = len(queryTypes)
		}
		queryTypes = queryTypes[start:end]
	}
	lines = append(lines, heading)
	for _, item := range queryTypes {
		name := dns.TypeToString[item.qtype]
		if name == "" {
			name = fmt.Sprintf("TYPE%d", item.qtype)
		}
		lines = append(lines, fmt.Sprintf("  %-12s %d", name, item.count))
	}
	return lines
}

func startLoadDisplay(output *os.File, listeners []dnsListener) func() {
	fd := int(output.Fd())
	if os.Getenv("TERM") == "dumb" || !term.IsTerminal(fd) {
		return func() {}
	}
	stop, done := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		previousRows := 0
		start := time.Now()
		for {
			width, height, err := term.GetSize(fd)
			if err == nil {
				page := int(time.Since(start) / (5 * time.Second))
				lines := loadDisplayLines(listeners, width, height, page)
				if len(lines) > 0 {
					if _, err := output.WriteString(formatLoadDisplay(lines, previousRows)); err != nil {
						listeners[0].diagnostics.Printf("write load summary: %v", err)
						return
					}
					previousRows = len(lines)
				}
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

func loadDisplayLines(listeners []dnsListener, width, height, page int) []string {
	const separator = " | "
	columnWidth := (width - 1 - (len(listeners)-1)*len(separator)) / len(listeners)
	if columnWidth < 1 || height < 2 {
		return nil
	}
	tables := make([][]string, 0, len(listeners))
	rows := 0
	for _, listener := range listeners {
		family := strings.ToUpper(listener.family[:2]) + listener.family[2:]
		table := append([]string{family}, listener.monitor.summaryLines(height-2, page)...)
		tables = append(tables, table)
		if len(table) > rows {
			rows = len(table)
		}
	}
	if rows >= height {
		rows = height - 1
	}
	lines := make([]string, rows)
	for row := range lines {
		lines[row] = tableCell(tables[0], row, columnWidth)
		if len(tables) == 2 {
			lines[row] = fmt.Sprintf("%-*s%s%s", columnWidth, lines[row], separator, tableCell(tables[1], row, columnWidth))
		}
	}
	return lines
}

func tableCell(table []string, row, width int) string {
	if row >= len(table) {
		return ""
	}
	line := table[row]
	if len(line) > width {
		return line[:width]
	}
	return line
}

func formatLoadDisplay(lines []string, previousRows int) string {
	var output strings.Builder
	if previousRows > 0 {
		fmt.Fprintf(&output, "\x1b[%dF\x1b[J", previousRows)
	}
	for _, line := range lines {
		output.WriteString("\r\x1b[2K")
		output.WriteString(line)
		output.WriteByte('\n')
	}
	return output.String()
}
