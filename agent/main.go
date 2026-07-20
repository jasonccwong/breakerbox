// breakerbox-agent runs on each host, supervises registered apps, collects
// metrics, and maintains an outbound WebSocket to the hub. It exposes no
// inbound network surface and executes only the fixed app-scoped verb set.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"syscall"

	"github.com/breakerbox/breakerbox/agent/internal/appconfig"
	"github.com/breakerbox/breakerbox/agent/internal/daemon"
	"github.com/breakerbox/breakerbox/agent/internal/identity"
	"github.com/breakerbox/breakerbox/pkg/protocol"
)

// version is set at build time via -ldflags "-X main.version=...".
var version = "dev"

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	store := &appconfig.Store{Dir: appconfig.DefaultStateDir()}

	var err error
	switch os.Args[1] {
	case "version":
		fmt.Printf("breakerbox-agent %s\n", version)
	case "enroll":
		err = cmdEnroll(store, os.Args[2:])
	case "run":
		err = cmdRun(store)
	case "apps":
		err = cmdApps(store, os.Args[2:])
	default:
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func cmdEnroll(store *appconfig.Store, args []string) error {
	fs := flag.NewFlagSet("enroll", flag.ExitOnError)
	hub := fs.String("hub", "", "hub base URL, e.g. https://hub.example.com or http://127.0.0.1:8090")
	token := fs.String("token", "", "one-time enrollment token from the hub UI")
	name := fs.String("name", "", "display name for this system (default: hostname)")
	_ = fs.Parse(args)
	if *hub == "" || *token == "" {
		return fmt.Errorf("--hub and --token are required")
	}

	priv, err := identity.LoadOrCreate(store.Dir)
	if err != nil {
		return err
	}
	displayName := *name
	if displayName == "" {
		displayName, _ = os.Hostname()
	}

	body, _ := json.Marshal(map[string]string{
		"token":      *token,
		"public_key": identity.PublicKeyB64(priv),
		"name":       displayName,
	})
	resp, err := http.Post(strings.TrimSuffix(*hub, "/")+"/api/bb/enroll", "application/json", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("cannot reach hub: %w", err)
	}
	defer resp.Body.Close()
	var out struct {
		SystemID string `json:"system_id"`
		Name     string `json:"name"`
		Message  string `json:"message"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&out)
	if resp.StatusCode != http.StatusOK {
		if out.Message == "" {
			out.Message = resp.Status
		}
		return fmt.Errorf("enrollment rejected: %s", out.Message)
	}

	st, err := store.Load()
	if err != nil {
		return err
	}
	st.HubURL = strings.TrimSuffix(*hub, "/")
	st.SystemID = out.SystemID
	if err := store.Save(st); err != nil {
		return err
	}
	fmt.Printf("enrolled as %q (system %s) with hub %s\n", out.Name, out.SystemID, st.HubURL)
	fmt.Println("start the agent with: breakerbox-agent run")
	return nil
}

func cmdRun(store *appconfig.Store) error {
	priv, err := identity.LoadOrCreate(store.Dir)
	if err != nil {
		return err
	}
	d, err := daemon.New(store, priv, version)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	return d.Run(ctx)
}

func cmdApps(store *appconfig.Store, args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: breakerbox-agent apps list|approve|reject|import")
	}
	switch args[0] {
	case "list":
		st, err := store.Load()
		if err != nil {
			return err
		}
		if len(st.Apps) == 0 {
			fmt.Println("no apps registered on this host")
			return nil
		}
		ids := make([]string, 0, len(st.Apps))
		for id := range st.Apps {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		for _, id := range ids {
			app := st.Apps[id]
			fmt.Printf("%s  %-20s %-9s desired=%-8s %s %s\n",
				id, app.Definition.Name, app.Approval, app.DesiredState,
				app.Definition.Cmd, strings.Join(app.Definition.Args, " "))
		}
		return nil

	case "approve", "reject":
		if len(args) < 2 {
			return fmt.Errorf("usage: breakerbox-agent apps %s <app-id>", args[0])
		}
		st, err := store.Load()
		if err != nil {
			return err
		}
		app, ok := st.Apps[args[1]]
		if !ok {
			return fmt.Errorf("unknown app %q (run: breakerbox-agent apps list)", args[1])
		}
		if args[0] == "approve" {
			fmt.Printf("You are approving this definition to run on THIS machine:\n\n")
			fmt.Printf("  name: %s\n  cmd:  %s %s\n  cwd:  %s\n",
				app.Definition.Name, app.Definition.Cmd, strings.Join(app.Definition.Args, " "), app.Definition.Cwd)
			if len(app.Definition.Env) > 0 {
				fmt.Printf("  env:  %v\n", app.Definition.Env)
			}
			fmt.Printf("\n")
		}
		if err := store.Enqueue(appconfig.SpoolOp{Op: args[0], AppID: args[1]}); err != nil {
			return err
		}
		fmt.Printf("%s queued; the running agent applies it within seconds\n", args[0])
		return nil

	case "import":
		if len(args) < 2 {
			return fmt.Errorf("usage: breakerbox-agent apps import <breakerbox.app.json>")
		}
		b, err := os.ReadFile(args[1])
		if err != nil {
			return err
		}
		var def protocol.AppDefinition
		if err := json.Unmarshal(b, &def); err != nil {
			return fmt.Errorf("%s is not valid JSON: %w", args[1], err)
		}
		if def.SchemaVersion == 0 {
			def.SchemaVersion = protocol.AppDefSchemaVersion
		}
		if def.Cwd == "" {
			// Default cwd to the definition file's directory — the prompt
			// tells the coding agent to write it next to the project.
			if abs, err := absDir(args[1]); err == nil {
				def.Cwd = abs
			}
		}
		if err := def.Validate(); err != nil {
			return fmt.Errorf("invalid definition: %s", err)
		}
		fmt.Printf("Importing (and approving) on this machine:\n\n")
		fmt.Printf("  name: %s\n  cmd:  %s %s\n  cwd:  %s\n\n", def.Name, def.Cmd, strings.Join(def.Args, " "), def.Cwd)
		if err := store.Enqueue(appconfig.SpoolOp{Op: "import", Definition: &def}); err != nil {
			return err
		}
		fmt.Println("import queued; the running agent registers it with the hub within seconds")
		return nil

	default:
		return fmt.Errorf("unknown apps subcommand %q", args[0])
	}
}

func absDir(file string) (string, error) {
	abs, err := filepath.Abs(file)
	if err != nil {
		return "", err
	}
	return filepath.Dir(abs), nil
}

func usage() {
	fmt.Fprintln(os.Stderr, `breakerbox-agent — the BreakerBox host agent

Usage:
  breakerbox-agent enroll --hub URL --token T   enroll this host with a hub
  breakerbox-agent run                          run the agent (service entrypoint)
  breakerbox-agent apps list                    list registered apps and approval state
  breakerbox-agent apps approve <id>            approve an app definition on this host
  breakerbox-agent apps reject <id>             reject an app definition
  breakerbox-agent apps import <file>           import breakerbox.app.json (register + approve)
  breakerbox-agent version                      print version

State dir: ` + appconfig.DefaultStateDir() + `
(override with BREAKERBOX_STATE_DIR)`)
}
