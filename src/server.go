package server

import (
	"fmt"
	"log"
	"os"

	"github.com/miekg/dns"
)

// Options selects a configured listener and provides optional overrides.
type Options struct {
	ConfigPath string
	Mode       string
	IP         string
	Port       *int
	LoadLog    *string
	ServeSOA   *bool
	AnswerTTL  *uint64
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
	monitor, err := startLoadMonitor(config.LoadLog)
	if err != nil {
		return fmt.Errorf("start load log: %w", err)
	}
	defer monitor.close()
	queryLog := startQueryLog(os.Stdout, 4096)
	defer queryLog.close()

	server := &dns.Server{
		Addr:    address,
		Net:     network,
		Handler: mirror.handler(queryLog.enqueue, monitor.record),
	}
	log.Printf("Starting DNS mirror on %s/%s", address, network)
	if err := server.ListenAndServe(); err != nil {
		return fmt.Errorf("DNS server: %w", err)
	}
	return nil
}
