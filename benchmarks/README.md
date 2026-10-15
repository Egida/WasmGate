# Benchmarks & load tests

Placeholder for the measurement harness (see `docs/03-mvp.md`, §8).

Planned scripts:

- `wrk2/` — HTTP-over-L4 throughput + latency (`p50`, `p99`)
- `vegeta/` — RPS / connection-rate scenarios
- `iperf3/` — raw L4 throughput (Gbps)
- `run.sh` — orchestrates S0–S5 from `docs/03-mvp.md` and feeds `benchstat`

The Go-side micro-benchmarks live next to the code (`go test -bench`).
