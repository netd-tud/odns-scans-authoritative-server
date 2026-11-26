package server

import (
	"fmt"
	"strings"

	"github.com/miekg/dns"
)

func (m *mirror) configureSOA(records map[string]soaConfig) error {
	if len(records) == 0 {
		return nil
	}
	m.soaRecords = make(map[string]dns.SOA, len(records))
	for name, config := range records {
		fqdn, err := dnsName(name)
		if err != nil {
			return fmt.Errorf("SOA owner: %w", err)
		}
		apex := strings.ToLower(fqdn)
		if _, exists := m.soaRecords[apex]; exists {
			return fmt.Errorf("duplicate SOA record for %q", apex)
		}
		primary, exists := m.nsRecords[apex]
		if !exists {
			return fmt.Errorf("SOA zone %q needs an apex entry in ns_records", apex)
		}
		mailbox, err := dnsName(config.Mailbox)
		if err != nil {
			return fmt.Errorf("SOA mailbox for %q: %w", apex, err)
		}
		m.soaRecords[apex] = dns.SOA{
			Hdr:     dns.RR_Header{Name: apex, Rrtype: dns.TypeSOA, Class: dns.ClassINET, Ttl: config.TTL},
			Ns:      primary.name,
			Mbox:    mailbox,
			Serial:  config.Serial,
			Refresh: config.Refresh,
			Retry:   config.Retry,
			Expire:  config.Expire,
			Minttl:  config.Minimum,
		}
	}

	m.knownNames = make(map[string]struct{})
	for name := range m.records {
		m.rememberName(name)
	}
	for name := range m.nsRecords {
		m.rememberName(name)
	}
	return nil
}

func (m *mirror) rememberName(name string) {
	// The ancestors of a record owner also exist, even when they have no records.
	for _, offset := range dns.Split(name) {
		m.knownNames[name[offset:]] = struct{}{}
	}
}

func (m *mirror) zoneSOA(name string) (dns.SOA, bool) {
	for _, offset := range dns.Split(name) {
		if soa, ok := m.soaRecords[name[offset:]]; ok {
			return soa, true
		}
	}
	soa, ok := m.soaRecords["."]
	return soa, ok
}

func (m *mirror) soaResponse(req *dns.Msg, q dns.Question) *dns.Msg {
	if q.Qclass != dns.ClassINET || len(m.soaRecords) == 0 {
		return nil
	}
	name := strings.ToLower(q.Name)
	soa, exists := m.zoneSOA(name)
	if !exists {
		return nil
	}

	response := new(dns.Msg).SetReply(req)
	if name == soa.Hdr.Name {
		soa.Hdr.Name = q.Name
		response.Answer = []dns.RR{&soa}
		return response
	}

	if _, known := m.knownNames[name]; !known {
		response.Rcode = dns.RcodeNameError
	}
	// RFC 2308 bounds the negative cache TTL by the SOA's MINIMUM field.
	if soa.Minttl < soa.Hdr.Ttl {
		soa.Hdr.Ttl = soa.Minttl
	}
	response.Ns = []dns.RR{&soa}
	return response
}
