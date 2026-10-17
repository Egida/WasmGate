// Command wasmgate is the WasmGate data plane: a fast, L4 TCP reverse proxy.
//
// Milestone: M0/M1 (basic proxying). The structure is deliberately built with
// seams for the planned product (hot reload, sliding-window DDoS, WASM plugins).
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"time"

	"wasmgate/internal/config"
	"wasmgate/internal/observe"
	"wasmgate/internal/proxy"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "fatal:", err)
		os.Exit(1)
	}
}

// run wires the graph (config -> runtime -> server), starts accepting and then
// blocks until a shutdown signal arrives. On Unix, SIGHUP reloads the config
// without dropping connections; the returned error is reported by main.
func run() error {
	var (
		configPath    = flag.String("config", "configs/wasmgate.yaml", "path to the configuration file")
		logLevel      = flag.String("log-level", "info", "log level: debug|info|warn|error")
		shutdownGrace = flag.Duration("shutdown-grace", 15*time.Second, "graceful shutdown timeout")
	)
	flag.Parse()

	log := observe.NewLogger(*logLevel)

	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}

	rt := proxy.NewRuntime(cfg)
	srv := proxy.NewServer(log, rt)

	if err := srv.Start(); err != nil {
		return err
	}
	log.Info("wasmgate started", "config_version", rt.Load().Version)

	// Build the set of signals: shutdown signals plus (on Unix) SIGHUP for reload.
	reload := reloadSignal()
	sigs := shutdownSignals()
	if reload != nil {
		sigs = append(sigs, reload)
	}
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, sigs...)
	defer signal.Stop(sigCh)

	for s := range sigCh {
		if reload != nil && s == reload {
			newCfg, err := config.Load(*configPath)
			if err != nil {
				log.Error("reload failed, keeping previous config", "err", err)
				continue
			}
			v, err := srv.Reload(context.Background(), newCfg)
			if err != nil {
				log.Error("reload failed, keeping previous config", "err", err)
				continue
			}
			log.Info("configuration reloaded", "config_version", v)
			continue
		}

		log.Info("shutting down", "signal", s.String())
		shutCtx, shutCancel := context.WithTimeout(context.Background(), *shutdownGrace)
		err := srv.Shutdown(shutCtx)
		shutCancel()
		if err != nil {
			log.Warn("connections force-closed after grace expired", "err", err)
		}
		return nil
	}
	return nil
}
