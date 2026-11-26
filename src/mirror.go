package server

import (
	"fmt"
	"log"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/miekg/dns"
)

// DefaultAnswerTTL is the TTL in seconds for A, AAAA, and NS replies.
const DefaultAnswerTTL = 600

type nameserver struct {
	name string
	ipv4 net.IP
	ipv6 net.IP
}

type mirror struct {
	records     map[string]mirrorRecord
	nsRecords   map[string]nameserver
	soaRecords  map[string]dns.SOA
	knownNames  map[string]struct{}
	controlIPv4 net.IP
	controlIPv6 net.IP
	answerTTL   uint32
}

func newMirror(config fileConfig) (*mirror, error) {
	m := &mirror{
		records:   make(map[string]mirrorRecord, len(config.Records)),
		nsRecords: make(map[string]nameserver, len(config.NSRecords)),
		answerTTL: DefaultAnswerTTL,
	}
	if config.AnswerTTL != nil {
		const maxTTL = 1<<31 - 1
		if *config.AnswerTTL > maxTTL {
			return nil, fmt.Errorf("invalid answer_ttl %d, expected 0–%d seconds", *config.AnswerTTL, maxTTL)
		}
		m.answerTTL = uint32(*config.AnswerTTL)
	}
	var err error
	if m.controlIPv4, err = parseIP(config.ControlIPv4, true); err != nil {
		return nil, fmt.Errorf("control_ipv4: %w", err)
	}
	if m.controlIPv6, err = parseIP(config.ControlIPv6, false); err != nil {
		return nil, fmt.Errorf("control_ipv6: %w", err)
	}
	for name, record := range config.Records {
		fqdn, err := dnsName(name)
		if err != nil {
			return nil, err
		}
		switch record.DelayOn {
		case "", "ipv4", "ipv6":
		default:
			return nil, fmt.Errorf("invalid delay_on %q for %q, expected ipv4 or ipv6", record.DelayOn, name)
		}
		key := strings.ToLower(fqdn)
		if _, exists := m.records[key]; exists {
			return nil, fmt.Errorf("duplicate mirror record for %q", fqdn)
		}
		m.records[key] = record
	}

	servers := make(map[string]nameserver, len(config.Nameservers))
	for id, entry := range config.Nameservers {
		server, err := newNameserver(entry)
		if err != nil {
			return nil, fmt.Errorf("nameserver %q: %w", id, err)
		}
		servers[id] = server
	}
	for name, serverID := range config.NSRecords {
		fqdn, err := dnsName(name)
		if err != nil {
			return nil, err
		}
		server, exists := servers[serverID]
		if !exists {
			return nil, fmt.Errorf("unknown nameserver %q for %q", serverID, name)
		}
		key := strings.ToLower(fqdn)
		if _, exists := m.nsRecords[key]; exists {
			return nil, fmt.Errorf("duplicate NS record for %q", fqdn)
		}
		m.nsRecords[key] = server
	}
	if config.ServeSOA == nil || *config.ServeSOA {
		if err := m.configureSOA(config.SOARecords); err != nil {
			return nil, err
		}
	}
	return m, nil
}

func newNameserver(config nameserverConfig) (nameserver, error) {
	var server nameserver
	var err error
	if server.name, err = dnsName(config.Name); err != nil {
		return server, err
	}
	if config.IPv4 != "" {
		if server.ipv4, err = parseIP(config.IPv4, true); err != nil {
			return server, err
		}
	}
	if config.IPv6 != "" {
		if server.ipv6, err = parseIP(config.IPv6, false); err != nil {
			return server, err
		}
	}
	return server, nil
}

func (m *mirror) header(name string, recordType uint16) dns.RR_Header {
	return dns.RR_Header{Name: name, Rrtype: recordType, Class: dns.ClassINET, Ttl: m.answerTTL}
}

func (m *mirror) aRecord(name string, ip net.IP) *dns.A {
	return &dns.A{Hdr: m.header(name, dns.TypeA), A: ip}
}

func (m *mirror) aaaaRecord(name string, ip net.IP) *dns.AAAA {
	return &dns.AAAA{Hdr: m.header(name, dns.TypeAAAA), AAAA: ip.To16()}
}

func (m *mirror) answer(q dns.Question, ip net.IP) (answers, additional []dns.RR, delay time.Duration) {
	name := strings.ToLower(q.Name)
	switch q.Qtype {
	case dns.TypeA, dns.TypeAAAA:
		record, exists := m.records[name]
		if !exists {
			return nil, nil, 0
		}
		ipv4 := ip.To4() != nil
		if (ipv4 && record.DelayOn == "ipv4") || (!ipv4 && record.DelayOn == "ipv6") {
			delay = time.Second
		}
		if q.Qtype == dns.TypeAAAA {
			return []dns.RR{m.aaaaRecord(q.Name, ip), m.aaaaRecord(q.Name, m.controlIPv6)}, nil, delay
		}
		var mirrored dns.RR = m.aRecord(q.Name, ip)
		if !ipv4 {
			mirrored = m.aaaaRecord(q.Name, ip)
		}
		return []dns.RR{mirrored, m.aRecord(q.Name, m.controlIPv4)}, nil, delay
	case dns.TypeNS:
		server, exists := m.nsRecords[name]
		if !exists {
			return nil, nil, 0
		}
		if server.ipv4 != nil {
			additional = append(additional, m.aRecord(server.name, server.ipv4))
		}
		if server.ipv6 != nil {
			additional = append(additional, m.aaaaRecord(server.name, server.ipv6))
		}
		return []dns.RR{&dns.NS{Hdr: m.header(q.Name, dns.TypeNS), Ns: server.name}}, additional, 0
	default:
		return nil, nil, 0
	}
}

func (m *mirror) response(req *dns.Msg, q dns.Question, ip net.IP) *dns.Msg {
	if q.Qtype == dns.TypeSOA {
		return m.soaResponse(req, q)
	}
	answers, additional, delay := m.answer(q, ip)
	if len(answers) == 0 {
		return nil
	}
	if delay > 0 {
		time.Sleep(delay)
	}
	response := new(dns.Msg).SetReply(req)
	response.Answer = answers
	response.Extra = additional
	return response
}

var queryHeader = []string{"timestamp_unix_ns", "query_type", "query_name", "source_ip"}

func (m *mirror) handler(logQuery func([]string), recordRequest func(string)) dns.HandlerFunc {
	return func(w dns.ResponseWriter, req *dns.Msg) {
		ip := w.RemoteAddr().(*net.UDPAddr).IP
		source := ip.String()
		recordRequest(source)
		if req.Opcode != dns.OpcodeQuery {
			return
		}
		for _, q := range req.Question {
			response := m.response(req, q, ip)
			if response == nil {
				continue
			}
			response.Authoritative = true

			row := []string{strconv.FormatInt(time.Now().UnixNano(), 10), dns.TypeToString[q.Qtype], q.Name, source}
			if err := w.WriteMsg(response); err != nil {
				log.Printf("write DNS reply: %v", err)
			}
			logQuery(row)
			return
		}
	}
}
