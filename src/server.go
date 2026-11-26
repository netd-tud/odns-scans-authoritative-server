package server

import (
	"fmt"
	"io/ioutil"
	"log"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/miekg/dns"
)

// Options selects a configured listener and provides optional overrides.
type Options struct {
	ConfigPath    string
	Mode          string
	IP            string
	Port          *int
	LoadLog       *string
	QueryLog      *string
	QueryLogMaxMB *int64
	ServeSOA      *bool
	AnswerTTL     *uint64
}

type dnsListener struct {
	listenEndpoint
	server      *dns.Server
	monitor     *loadMonitor
	diagnostics *log.Logger
}

// Run loads the configuration and serves DNS over UDP.
func Run(options Options) error {
	config, err := readConfig(options.ConfigPath)
	if err != nil {
		return err
	}
	if options.Port != nil {
		config.Listen.Port = *options.Port
	}
	if options.LoadLog != nil {
		config.LoadLog = *options.LoadLog
	}
	if options.QueryLog != nil {
		config.QueryLog = *options.QueryLog
	}
	if options.QueryLogMaxMB != nil {
		config.QueryLogMaxMB = *options.QueryLogMaxMB
	}
	if options.ServeSOA != nil {
		config.ServeSOA = options.ServeSOA
	}
	if options.AnswerTTL != nil {
		config.AnswerTTL = options.AnswerTTL
	}

	endpoints, err := config.Listen.endpoints(options.Mode, options.IP)
	if err != nil {
		return err
	}
	mirror, err := newMirror(config)
	if err != nil {
		return err
	}
	queryName, err := logFilename(config.QueryLog)
	if err != nil {
		return fmt.Errorf("query log: %w", err)
	}
	loadName, err := logFilename(config.LoadLog)
	if err != nil {
		return fmt.Errorf("load log: %w", err)
	}
	statusName := queryName + ".log"
	if loadName == queryName || loadName == statusName {
		return fmt.Errorf("load log and query log must use separate files")
	}

	// Bind every requested socket before handling DNS or creating run logs.
	listeners := make([]dnsListener, 0, len(endpoints))
	for _, endpoint := range endpoints {
		conn, err := net.ListenPacket(endpoint.network, endpoint.address)
		if err != nil {
			return fmt.Errorf("bind %s listener: %w", endpoint.family, err)
		}
		defer conn.Close()
		listeners = append(listeners, dnsListener{
			listenEndpoint: endpoint,
			server:         &dns.Server{Addr: endpoint.address, Net: endpoint.network, PacketConn: conn},
		})
	}
	runDir, err := newLogRun()
	if err != nil {
		return fmt.Errorf("create log run directory: %w", err)
	}
	for i := range listeners {
		listener := &listeners[i]
		dir := filepath.Join(runDir, listener.family)
		queryOutput, err := openQueryLog(filepath.Join(dir, queryName), config.QueryLogMaxMB)
		if err != nil {
			return fmt.Errorf("start %s query log: %w", listener.family, err)
		}
		defer queryOutput.close()
		statusLog, err := os.OpenFile(filepath.Join(dir, statusName), os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0640)
		if err != nil {
			return fmt.Errorf("start %s diagnostic log: %w", listener.family, err)
		}
		defer statusLog.Close()
		listener.diagnostics = log.New(statusLog, "", log.LstdFlags)

		listener.monitor, err = startLoadMonitor(filepath.Join(dir, loadName), listener.diagnostics)
		if err != nil {
			return fmt.Errorf("start %s load log: %w", listener.family, err)
		}
		defer listener.monitor.close()
		queryLog := startQueryLog(queryOutput, listener.diagnostics, 4096)
		defer queryLog.close()
		listener.server.Handler = mirror.handler(queryLog.enqueue, listener.monitor.record, listener.diagnostics)
	}
	stopDisplay := startLoadDisplay(os.Stdout, listeners)
	defer stopDisplay()
	return serveUntilSignal(listeners)
}

func logFilename(path string) (string, error) {
	name := filepath.Base(path)
	if name == "." || name == ".." || filepath.IsAbs(name) {
		return "", fmt.Errorf("expected a filename, got %q", path)
	}
	return name, nil
}

func newLogRun() (string, error) {
	const dir = "logs"
	if err := os.MkdirAll(dir, 0750); err != nil {
		return "", err
	}
	prefix := time.Now().UTC().Format("20060102T150405Z") + "-"
	return ioutil.TempDir(dir, prefix)
}

func serveUntilSignal(listeners []dnsListener) error {
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(signals)
	stop := make(chan struct{})
	results := make(chan error, len(listeners))
	for i := range listeners {
		go func(listener *dnsListener) {
			results <- listener.serve(stop)
		}(&listeners[i])
	}

	var firstError error
	remaining := len(listeners)
	select {
	case <-signals:
	case firstError = <-results:
		remaining--
	}
	signal.Stop(signals)
	close(stop)
	for ; remaining > 0; remaining-- {
		if err := <-results; err != nil && firstError == nil {
			firstError = err
		}
	}
	return firstError
}

func (l *dnsListener) serve(stop <-chan struct{}) error {
	done := make(chan struct{})
	shutdown := make(chan struct{})
	started := false
	l.server.NotifyStartedFunc = func() {
		started = true
		l.diagnostics.Printf("Starting DNS mirror on %s/%s", l.address, l.network)
		go func() {
			defer close(shutdown)
			select {
			case <-stop:
				if err := l.server.Shutdown(); err != nil {
					l.diagnostics.Printf("shut down DNS server: %v", err)
				}
			case <-done:
			}
		}()
	}
	err := l.server.ActivateAndServe()
	close(done)
	if started {
		<-shutdown
	}
	if err != nil {
		return fmt.Errorf("%s DNS server: %w", l.family, err)
	}
	return nil
}
