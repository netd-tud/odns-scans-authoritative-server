package main

import (
	"flag"
	"log"

	"resolver/src"
)

func main() {
	configPath := flag.String("config", "config.yaml", "YAML configuration file")
	mode := flag.String("mode", "ipv4", "Listener mode (ipv4, ipv6, or both)")
	ip := flag.String("ip", "", "Override the listen address with an IPv4 or IPv6 literal")
	port := flag.Int("port", 0, "Override the configured UDP port")
	loadLog := flag.String("load-log", server.DefaultLoadLog, "Override the load CSV filename for each listener")
	logfile := flag.String("logfile", server.DefaultQueryLog, "Override the query CSV filename for each listener")
	logMaxMB := flag.Int64("log-max-mb", server.DefaultQueryLogMaxMB, "Rotate query logs at this size in MB")
	serveSOA := flag.Bool("serve-soa", true, "Enable SOA replies (overrides serve_soa in YAML when set)")
	answerTTL := flag.Uint64("answer-ttl", server.DefaultAnswerTTL, "Override the A, AAAA, and NS reply TTL in seconds")
	flag.Parse()

	options := server.Options{
		ConfigPath: *configPath,
		Mode:       *mode,
		IP:         *ip,
	}
	flag.Visit(func(option *flag.Flag) {
		switch option.Name {
		case "port":
			options.Port = port
		case "load-log":
			options.LoadLog = loadLog
		case "logfile":
			options.QueryLog = logfile
		case "log-max-mb":
			options.QueryLogMaxMB = logMaxMB
		case "serve-soa":
			options.ServeSOA = serveSOA
		case "answer-ttl":
			options.AnswerTTL = answerTTL
		}
	})
	if err := server.Run(options); err != nil {
		log.Fatal(err)
	}
}
