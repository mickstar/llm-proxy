// Package app wires the config manager, traffic log, proxy, and control API
// into a single runnable server. It is shared by the `serve` subcommand and the
// default TUI command (which starts the server in-process when none is running).
package app

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"path/filepath"
	"time"

	"github.com/mickstar/llm-proxy/internal/config"
	"github.com/mickstar/llm-proxy/internal/control"
	"github.com/mickstar/llm-proxy/internal/halt"
	"github.com/mickstar/llm-proxy/internal/notify"
	"github.com/mickstar/llm-proxy/internal/pricing"
	"github.com/mickstar/llm-proxy/internal/proxy"
	"github.com/mickstar/llm-proxy/internal/redact"
	"github.com/mickstar/llm-proxy/internal/secret"
	"github.com/mickstar/llm-proxy/internal/stats"
	"github.com/mickstar/llm-proxy/internal/traffic"
)

// App owns the running server components.
type App struct {
	Manager *config.Manager
	Log     *traffic.Log
	Stats   *stats.Store
	Proxy   *proxy.Server
	Control *control.Server

	proxySrv   *http.Server
	controlSrv *http.Server
	proxyLn    net.Listener
	controlLn  net.Listener
}

// New loads the config at cfgPath (creating defaults if absent) and builds the
// server components without binding any listeners.
func New(cfgPath string) (*App, error) {
	mgr, err := config.Open(cfgPath)
	if err != nil {
		return nil, err
	}
	log := traffic.New(mgr.Current().Server.TrafficLogCapacity)
	aliases := redact.NewTable(0)
	breaker := halt.New()
	store := stats.New(filepath.Join(filepath.Dir(mgr.Path()), "stats.json"))
	store.BackfillCosts(pricing.Build(mgr.Current().Models))
	secrets := secret.NewResolver()
	return &App{
		Manager: mgr,
		Log:     log,
		Stats:   store,
		Proxy:   proxy.New(proxy.Options{Manager: mgr, Log: log, Aliases: aliases, Halt: breaker, Stats: store, Secrets: secrets, Notify: notify.MacOS}),
		Control: control.New(mgr, log, aliases, breaker, store, secrets),
	}, nil
}

// Start binds both listeners and serves them in the background.
func (a *App) Start() error {
	cfg := a.Manager.Current()
	proxyLn, err := net.Listen("tcp", cfg.Server.ProxyAddr)
	if err != nil {
		return fmt.Errorf("listen proxy %s: %w", cfg.Server.ProxyAddr, err)
	}
	controlLn, err := net.Listen("tcp", cfg.Server.ControlAddr)
	if err != nil {
		_ = proxyLn.Close()
		return fmt.Errorf("listen control %s: %w", cfg.Server.ControlAddr, err)
	}
	a.proxyLn, a.controlLn = proxyLn, controlLn
	a.proxySrv = &http.Server{Handler: a.Proxy}
	a.controlSrv = &http.Server{Handler: a.Control.Handler()}
	go func() {
		if err := a.proxySrv.Serve(proxyLn); err != nil && !errors.Is(err, http.ErrServerClosed) {
			fmt.Printf("proxy server stopped: %v\n", err)
		}
	}()
	go func() {
		if err := a.controlSrv.Serve(controlLn); err != nil && !errors.Is(err, http.ErrServerClosed) {
			fmt.Printf("control server stopped: %v\n", err)
		}
	}()
	return nil
}

// ProxyURL returns the base URL of the proxy listener (no trailing slash).
func (a *App) ProxyURL() string {
	if a.proxyLn == nil {
		return ""
	}
	return "http://" + a.proxyLn.Addr().String()
}

// ControlURL returns the base URL of the control listener (no trailing slash).
func (a *App) ControlURL() string {
	if a.controlLn == nil {
		return ""
	}
	return "http://" + a.controlLn.Addr().String()
}

// Close shuts down both listeners and stops the config watcher.
func (a *App) Close() error {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if a.proxySrv != nil {
		_ = a.proxySrv.Shutdown(ctx)
	}
	if a.controlSrv != nil {
		_ = a.controlSrv.Shutdown(ctx)
	}
	a.Proxy.Close()
	_ = a.Stats.Close()
	a.Manager.Close()
	return nil
}
