package server

import (
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

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

	address, network, err := config.Listen.endpoint(options.Mode, options.IP)
	if err != nil {
		return err
	}
	mirror, err := newMirror(config)
	if err != nil {
		return err
	}
	queryPath, err := resolveQueryLogPath(config.QueryLog)
	if err != nil {
		return err
	}
	statusPath := queryPath + ".log"
	loadPath, err := filepath.Abs(config.LoadLog)
	if err != nil {
		return err
	}
	if loadPath == queryPath || loadPath == statusPath {
		return fmt.Errorf("load log and query log must use separate files")
	}
	queryOutput, err := openQueryLog(queryPath, config.QueryLogMaxMB)
	if err != nil {
		return fmt.Errorf("start query log: %w", err)
	}
	defer queryOutput.close()
	statusLog, err := os.OpenFile(statusPath, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0640)
	if err != nil {
		return fmt.Errorf("start diagnostic log: %w", err)
	}
	defer statusLog.Close()
	diagnostics := log.New(statusLog, "", log.LstdFlags)

	monitor, err := startLoadMonitor(config.LoadLog, diagnostics)
	if err != nil {
		return fmt.Errorf("start load log: %w", err)
	}
	defer monitor.close()
	queryLog := startQueryLog(queryOutput, diagnostics, 4096)
	defer queryLog.close()

	server := &dns.Server{
		Addr:    address,
		Net:     network,
		Handler: mirror.handler(queryLog.enqueue, monitor.record, diagnostics),
	}
	diagnostics.Printf("Starting DNS mirror on %s/%s", address, network)
	stopDisplay := startLoadDisplay(os.Stdout, monitor, diagnostics)
	defer stopDisplay()
	if err := serveUntilSignal(server, diagnostics); err != nil {
		return fmt.Errorf("DNS server: %w", err)
	}
	return nil
}

func serveUntilSignal(server *dns.Server, diagnostics *log.Logger) error {
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(signals)
	done := make(chan struct{})
	defer close(done)
	server.NotifyStartedFunc = func() {
		go func() {
			select {
			case <-signals:
				signal.Stop(signals)
				if err := server.Shutdown(); err != nil {
					diagnostics.Printf("shut down DNS server: %v", err)
				}
			case <-done:
			}
		}()
	}
	return server.ListenAndServe()
}
