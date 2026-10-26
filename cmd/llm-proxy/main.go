// Command llm-proxy is a local, OpenAI-compatible checkpoint for LLM traffic.
//
// Usage:
//
//	llm-proxy                 start the server (if needed) and open the TUI
//	llm-proxy serve           run the proxy + control API headless
//	llm-proxy fetch <model>   fetch model metadata from OpenRouter into config
//	llm-proxy resume          clear the halted state left by a blocked request
//
// The global --config <path> flag overrides the default config file location.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/mickstar/llm-proxy/internal/app"
	"github.com/mickstar/llm-proxy/internal/catalog"
	"github.com/mickstar/llm-proxy/internal/config"
	"github.com/mickstar/llm-proxy/internal/openrouter"
	"github.com/mickstar/llm-proxy/internal/secret"
	"github.com/mickstar/llm-proxy/internal/tui"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "llm-proxy: "+err.Error())
		os.Exit(1)
	}
}

func run(args []string) error {
	configFlag, rest := extractConfigFlag(args)
	path, err := config.ResolvePath(configFlag)
	if err != nil {
		return err
	}

	cmd := ""
	if len(rest) > 0 {
		cmd = rest[0]
	}
	switch cmd {
	case "serve":
		return runServe(path)
	case "resume":
		return runResume(path)
	case "fetch":
		if len(rest) < 2 {
			return errors.New("usage: llm-proxy fetch <author/slug>")
		}
		return runFetch(path, rest[1])
	case "help", "-h", "--help":
		fmt.Print(usage)
		return nil
	default:
		if cmd != "" {
			return fmt.Errorf("unknown command %q\n\n%s", cmd, usage)
		}
		return runDefault(path)
	}
}

const usage = `usage: llm-proxy [--config <path>] [command]

commands:
  (none)               start the server if needed, then open the TUI
  serve                run the proxy + control API headless
  fetch <author/slug>  fetch model metadata from OpenRouter into the config
  resume               clear the halted state left by a blocked request
`

// extractConfigFlag pulls --config/-c out of args, returning the path and the
// remaining positional args.
func extractConfigFlag(args []string) (string, []string) {
	var path string
	rest := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--config" || a == "-c":
			if i+1 < len(args) {
				path = args[i+1]
				i++
			}
		case strings.HasPrefix(a, "--config="):
			path = strings.TrimPrefix(a, "--config=")
		case strings.HasPrefix(a, "-c="):
			path = strings.TrimPrefix(a, "-c=")
		default:
			rest = append(rest, a)
		}
	}
	return path, rest
}

func runServe(path string) error {
	a, err := app.New(path)
	if err != nil {
		return err
	}
	if err := a.Start(); err != nil {
		return err
	}
	fmt.Printf("proxy    listening on %s\n", a.ProxyURL())
	fmt.Printf("control  listening on %s\n", a.ControlURL())
	fmt.Printf("config   %s\n", a.Manager.Path())

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	<-ctx.Done()
	fmt.Println("shutting down")
	return a.Close()
}

func runDefault(path string) error {
	c, err := config.Load(path)
	if err != nil {
		return err
	}
	controlURL := "http://" + c.Server.ControlAddr
	if healthy(controlURL) {
		return tui.Run(controlURL)
	}
	a, err := app.New(path)
	if err != nil {
		return err
	}
	if err := a.Start(); err != nil {
		return err
	}
	defer func() { _ = a.Close() }()
	return tui.Run(a.ControlURL())
}

// runFetch adds or updates a model. When a server is running it routes the
// mutation through the control API (single writer); otherwise it edits the
// config file directly.
// runResume clears the proxy's halted state on a running server.
func runResume(path string) error {
	c, err := config.Load(path)
	if err != nil {
		return err
	}
	controlURL := "http://" + c.Server.ControlAddr
	if !healthy(controlURL) {
		return fmt.Errorf("no running server at %s", controlURL)
	}
	resp, err := httpPost(controlURL+"/api/resume", nil)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("resume failed: %s", readError(resp))
	}
	fmt.Println("proxy resumed")
	return nil
}

func runFetch(path, id string) error {
	if _, _, err := catalog.ParseModelID(id); err != nil {
		return err
	}
	c, err := config.Load(path)
	if err != nil {
		return err
	}
	controlURL := "http://" + c.Server.ControlAddr
	if healthy(controlURL) {
		return fetchViaAPI(controlURL, id)
	}
	key, err := secret.NewResolver().Resolve(c.OpenRouter.APIKey)
	if err != nil {
		return fmt.Errorf("upstream credential: %w", err)
	}
	client := openrouter.New(c.OpenRouter.BaseURL, key)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	m, err := client.FetchModel(ctx, id)
	if err != nil {
		return err
	}
	models, added := catalog.Upsert(c.Models, *m)
	c.Models = models
	if _, err := config.Save(path, c); err != nil {
		return err
	}
	verb := "updated"
	if added {
		verb = "added"
	}
	fmt.Printf("%s model %s (context length %d, prompt %s / completion %s)\n",
		verb, m.ID, m.ContextLength, m.Pricing.Prompt, m.Pricing.Completion)
	return nil
}

func fetchViaAPI(controlURL, id string) error {
	body, _ := json.Marshal(map[string]string{"id": id})
	resp, err := httpPost(controlURL+"/api/models/fetch", body)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("fetch failed: %s", readError(resp))
	}
	var m config.Model
	if err := json.NewDecoder(resp.Body).Decode(&m); err != nil {
		return err
	}
	fmt.Printf("added/updated model %s via running server (context length %d)\n", m.ID, m.ContextLength)
	return nil
}

func httpPost(url string, body []byte) (*http.Response, error) {
	client := &http.Client{Timeout: 30 * time.Second}
	return client.Post(url, "application/json", bytes.NewReader(body))
}

func readError(resp *http.Response) string {
	var e struct {
		Error string `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&e); err == nil && e.Error != "" {
		return e.Error
	}
	return resp.Status
}

// healthy probes the control API health endpoint.
func healthy(controlURL string) bool {
	client := &http.Client{Timeout: 500 * time.Millisecond}
	resp, err := client.Get(controlURL + "/api/health")
	if err != nil {
		return false
	}
	defer func() { _ = resp.Body.Close() }()
	return resp.StatusCode == http.StatusOK
}
