# ODNS scans authoritative server

This UDP DNS server mirrors the source IP it sees on the incoming query, which may be a recursive resolver or forwarder. It also returns a static control record for A and AAAA queries.

## Requirements

- Go 1.15 or newer and Make. The first build needs access to a Go module proxy or a populated module cache.
- Permission to bind the configured UDP port and write the query and load logs. Port 53 may require additional privileges.
- `zstd` on the server's `PATH` to compress rotated query logs.
- `dig` for the optional query examples.

## Build and run

Run `make` from the project directory to build `rr-mirror`. Run `make clean` to remove the binary.

```sh
make
```

For a fresh checkout, copy the template, set `parent_domain`, and replace the address placeholders. Git ignores the local `config.yaml`. Keep your existing file if you already have one.

```sh
cp -i config.template.yaml config.yaml
```

After editing the config, run `./rr-mirror`. It reads `config.yaml` from the working directory and uses the configured IPv4 address and port. Use `-config` to select another file or `-mode ipv6` to select the configured IPv6 address. Use `-ip` and `-port` to override the listener with a literal IP address and UDP port. Wildcard addresses listen on all interfaces of the selected IP family.

On Linux, you can grant the binary permission to bind to port 53 and then run it as your regular user. That user will own newly created log files.

```sh
sudo setcap 'cap_net_bind_service=ep' ./rr-mirror
./rr-mirror
```

Reapply `setcap` after a rebuild replaces the binary.

For a local check, set `DNS_SERVER_IP` to the listener address, `DNS_PORT` to its port, and `MIRROR_FQDN` and `ZONE_FQDN` to names you configured.

```sh
dig @"$DNS_SERVER_IP" -p "$DNS_PORT" "$MIRROR_FQDN" A
dig @"$DNS_SERVER_IP" -p "$DNS_PORT" "$MIRROR_FQDN" AAAA
dig @"$DNS_SERVER_IP" -p "$DNS_PORT" "$ZONE_FQDN" NS
dig @"$DNS_SERVER_IP" -p "$DNS_PORT" "$ZONE_FQDN" SOA +norecurse
dig @"$DNS_SERVER_IP" -p "$DNS_PORT" "$MIRROR_FQDN" SOA +norecurse
```

## DNS configuration

Set `parent_domain` to the common suffix of the DNS names you configure, with or without a trailing dot. The server expands `${parent_domain}` in `records`, `ns_records`, and `soa_records` keys, nameserver targets, and SOA mailboxes. For example, the following record serves `rr-mirror.research.example.test.`.

```yaml
parent_domain: "example.test"
records:
  "rr-mirror.research.${parent_domain}": {}
```

Literal DNS names are also supported. Configurations with only literal names can omit `parent_domain`.

- `records` maps exact, case-insensitive DNS names to their mirror settings. Use `{}` for the usual reply or set `delay_on` to `ipv4` or `ipv6` to delay A and AAAA replies by one second for queries arriving from that address family.
- `ns_records` maps exact DNS names to entries in `nameservers`. Each nameserver specifies a DNS name and can include an IPv4 address, an IPv6 address, or both. These advertised addresses are independent of the command-line listener overrides.
- `soa_records` maps delegated zone apexes to their SOA settings. Each apex must have an entry in `ns_records` for the primary nameserver. Set `mailbox` in SOA DNS-name notation, with a dot in place of the email address's `@`. Increase the serial when the zone data changes. The TTL and timers are in seconds. Existing configs can add this section to enable SOA replies.
- `serve_soa` defaults to `true` and enables replies for the configured SOA zones. Set it to `false` to silently drop SOA queries. The `-serve-soa=true` and `-serve-soa=false` flags override the YAML setting.
- An A query returns the source IP as A for IPv4 or AAAA for IPv6, plus the IPv4 control record. An AAAA query returns the source IP as AAAA, using an IPv4-mapped IPv6 address for IPv4 senders, plus the IPv6 control record. NS replies include the configured nameserver and its configured addresses in the additional section.
- `answer_ttl` sets the TTL in seconds for A, AAAA, and NS replies, including control records and additional nameserver addresses. It defaults to 600. Use `-answer-ttl=120` to override the YAML setting at startup. A value of 0 sets a zero TTL. SOA replies use the TTL configured for their zone.
- An SOA query at an apex returns the SOA in the answer section. Below the apex, an existing name returns `NOERROR` with an empty answer, and an unknown name returns `NXDOMAIN`. Both negative replies carry the zone SOA in the authority section with the lower of `ttl` and `minimum`. Other unmatched queries are silently dropped.

For public DNS, delegate the relevant zones to this server from a parent zone you control and publish the nameserver's address records.

## Logs

Answered queries go to `logs/queries.csv` as semicolon-delimited CSV.

```text
timestamp_unix_ns;query_type;query_name;source_ip
```

Use `query_log` or `-logfile` to choose the output file. A bare filename goes in `logs/`. An explicit directory in the path overrides that location. The server creates the directory as needed. The active file rotates before the next row would exceed `query_log_max_mb` or `-log-max-mb`, which defaults to 100 MB (100,000,000 bytes). Archived files keep their CSV header and get a unique timestamped name beside the active file.

Rotated query logs are compressed in the background as `.csv.zst`. The original CSV is removed after successful compression. If `zstd` is unavailable or compression fails, the CSV is kept and the diagnostic log records the reason. Use `zstd -dc archive.csv.zst` to read a compressed log.

```sh
./rr-mirror -logfile scans.csv -log-max-mb 50
```

A background writer drains a 4,096-row buffer. It drops new rows when the buffer fills. Startup messages, write errors, and drop counts go to the diagnostic file beside the query CSV, for example `logs/scans.csv.log`. A compatible terminal shows a three-line load summary that updates in place once a second. Redirected stdout stays empty.

The server appends one semicolon-delimited row to `load.csv` at each five-minute UTC boundary, including intervals with zero requests. Set `load_log` in YAML or use `-load-log` to choose a different path. Use separate query and load paths for each server process. A load CSV from before peak tracking is archived beside the active file before the new header is written.

```text
window_end_utc;day_utc;avg_requests_per_second;unique_ips_5m;requests_5m;unique_ips_day;requests_day;peak_requests_per_second
```

The load counters include all requests delivered to the DNS handler, including ones it drops. Each request counts once in the interval when the handler receives it. Unique IPs count distinct source addresses within the interval or UTC day. The average rate is the interval total divided by 300 seconds. The peak is the largest request count in any one-second UTC bucket within that interval. The first row can cover a partial interval when the server starts between boundaries. Daily totals are cumulative through the end of each row's interval. The midnight row closes the previous UTC day. Counters start over when the process restarts.
