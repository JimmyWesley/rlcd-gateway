// Command rlcd-gateway is an LLM gateway: coding agents and any app using an
// OpenAI or Anthropic SDK point their base URL at it, and it routes, prunes
// and records every call. See the repository README.
package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"

	"github.com/JimmyWesley/rlcd-gateway/gateway/internal/config"
	"github.com/JimmyWesley/rlcd-gateway/gateway/internal/keys"
	"github.com/JimmyWesley/rlcd-gateway/gateway/internal/recall"
	"github.com/JimmyWesley/rlcd-gateway/gateway/internal/server"
	"github.com/JimmyWesley/rlcd-gateway/gateway/internal/setup"
	"github.com/JimmyWesley/rlcd-gateway/gateway/internal/store"
)

var version = "dev"

const usage = `rlcd-gateway — LLM gateway for coding agents and apps

Usage:
  rlcd-gateway [serve] [-listen addr] [-allow-host name,...]
                                        start the gateway and its dashboard
  rlcd-gateway setup <agent>            point an agent at the gateway
  rlcd-gateway undo <agent>             point it back at its provider
  rlcd-gateway keys list                list gateway keys
  rlcd-gateway keys create <name> [-rpm N] [-tokens-per-day N] [-aliases a,b] [-routes r,s]
                                        create a gateway key (shown once)
  rlcd-gateway keys revoke <id>         revoke a gateway key
  rlcd-gateway version

Agents: claude (Claude Code), codex (Codex CLI), opencode (OpenCode).

Listening:
  The default, 127.0.0.1:4777, only serves this machine, and gateway keys
  are optional. Any other address (e.g. -listen 0.0.0.0:4777) requires a
  gateway key on every proxy path, and serves the dashboard only to this
  machine unless RLCD_GATEWAY_ADMIN_TOKEN (24+ characters) is set.
  -allow-host adds host names the gateway answers to (IP addresses and
  localhost always work; other names are refused against DNS rebinding).

Try it without changing any settings:
  ANTHROPIC_BASE_URL=http://127.0.0.1:4777 claude
  OPENAI_BASE_URL=http://127.0.0.1:4777/v1 python my_app.py

Config and logs: $RLCD_GATEWAY_HOME, or ~/.rlcd-gateway.
`

func main() {
	args := os.Args[1:]
	cmd := "serve"
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		cmd, args = args[0], args[1:]
	}
	switch cmd {
	case "serve":
		serve(args)
	case "setup", "undo":
		agentCommand(cmd, args)
	case "keys":
		keysCommand(args)
	case "version":
		fmt.Println(version)
	case "help", "-h", "--help":
		fmt.Print(usage)
	default:
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
}

func agentCommand(cmd string, args []string) {
	if len(args) != 1 {
		fmt.Fprintf(os.Stderr, "usage: rlcd-gateway %s <%s>\n", cmd, strings.Join(setup.Agents(), "|"))
		os.Exit(2)
	}
	cs, err := config.Load()
	if err != nil {
		log.Fatal(err)
	}
	msg, err := setup.Run(args[0], cmd == "undo", "http://"+cs.Get().Listen)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(msg)
}

func splitList(s string) []string {
	var out []string
	for _, v := range strings.Split(s, ",") {
		if v = strings.TrimSpace(v); v != "" {
			out = append(out, v)
		}
	}
	return out
}

// keysCommand edits keys.json directly; a running gateway picks the change
// up on its next request.
func keysCommand(args []string) {
	ks, err := keys.Open(config.Dir())
	if err != nil {
		log.Fatal(err)
	}
	if len(args) == 0 {
		args = []string{"list"}
	}
	switch args[0] {
	case "list":
		for _, k := range ks.List() {
			state := "active"
			if k.Revoked != nil {
				state = "revoked"
			}
			fmt.Printf("%s  %-20s %s  %s  requests %d  cost ~$%.4f\n", k.ID, k.Name, k.Hint, state, k.Usage.Requests, k.Usage.CostUSD)
		}
	case "create":
		fs := flag.NewFlagSet("keys create", flag.ExitOnError)
		rpm := fs.Int("rpm", 0, "requests per minute (0: no limit)")
		tpd := fs.Int("tokens-per-day", 0, "tokens per UTC day (0: no limit)")
		aliases := fs.String("aliases", "", "comma-separated model aliases the key may use (empty: any)")
		routes := fs.String("routes", "", "comma-separated routes the key may use (empty: any)")
		if len(args) < 2 || strings.HasPrefix(args[1], "-") {
			fmt.Fprintln(os.Stderr, "usage: rlcd-gateway keys create <name> [-rpm N] [-tokens-per-day N] [-aliases a,b] [-routes r,s]")
			os.Exit(2)
		}
		_ = fs.Parse(args[2:])
		key, v, err := ks.Create(args[1], keys.Limits{RPM: *rpm, TokensPerDay: *tpd,
			Aliases: splitList(*aliases), Routes: splitList(*routes)})
		if err != nil {
			log.Fatal(err)
		}
		fmt.Printf("Created %s (%s). Copy the key now, it is not shown again:\n\n  %s\n", v.Name, v.ID, key)
	case "revoke":
		if len(args) != 2 {
			fmt.Fprintln(os.Stderr, "usage: rlcd-gateway keys revoke <id>")
			os.Exit(2)
		}
		if _, err := ks.Revoke(args[1]); err != nil {
			log.Fatalf("revoke %s: %v", args[1], err)
		}
		fmt.Println("Revoked", args[1])
	default:
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
}

func serve(args []string) {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	listen := fs.String("listen", "", "address to listen on (default from config, 127.0.0.1:4777)")
	allow := fs.String("allow-host", "", "extra host names to answer to when listening beyond loopback, comma-separated")
	_ = fs.Parse(args)

	cs, err := config.Load()
	if err != nil {
		log.Fatal(err)
	}
	cfg := cs.Get()
	addr := cfg.Listen
	if *listen != "" {
		addr = *listen
	}
	st, err := store.Open(config.Dir())
	if err != nil {
		log.Fatal(err)
	}

	recall.Version = version
	gw, err := server.New(cs, st, server.Options{Listen: addr,
		AdminToken: os.Getenv("RLCD_GATEWAY_ADMIN_TOKEN"), AllowHosts: splitList(*allow)})
	if err != nil {
		log.Fatal(err)
	}

	mode := "loopback only; gateway keys optional"
	if gw.Guard.Exposed() {
		mode = "EXPOSED: gateway key required on every proxy path"
		if gw.Keys.Active() == 0 {
			mode += "; there is no key yet, so every call is refused (create one: rlcd-gateway keys create <name>)"
		}
		if os.Getenv("RLCD_GATEWAY_ADMIN_TOKEN") == "" {
			mode += "; dashboard served to this machine only"
		}
	} else if cfg.RequireKeys {
		mode = "loopback only; gateway keys required"
	}
	fmt.Printf("rlcd-gateway %s\n  proxy      http://%s  (Anthropic /v1/messages, OpenAI /v1/chat/completions and /v1/responses)\n"+
		"  dashboard  http://%s/ui/\n  config     %s\n  route      %s\n  access     %s\n",
		version, addr, addr, config.Path(), cfg.ActiveRoute, mode)
	log.Fatal(http.ListenAndServe(addr, gw.Handler))
}
