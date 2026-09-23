// Command rlcd-gateway is a local gateway between coding agents and their
// model providers. See the repository README.
package main

import (
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"strings"

	"github.com/JimmyWesley/rlcd-gateway/gateway/internal/adapters"
	"github.com/JimmyWesley/rlcd-gateway/gateway/internal/api"
	"github.com/JimmyWesley/rlcd-gateway/gateway/internal/config"
	"github.com/JimmyWesley/rlcd-gateway/gateway/internal/pipeline"
	"github.com/JimmyWesley/rlcd-gateway/gateway/internal/proxy"
	"github.com/JimmyWesley/rlcd-gateway/gateway/internal/prune"
	"github.com/JimmyWesley/rlcd-gateway/gateway/internal/recall"
	"github.com/JimmyWesley/rlcd-gateway/gateway/internal/router"
	"github.com/JimmyWesley/rlcd-gateway/gateway/internal/setup"
	"github.com/JimmyWesley/rlcd-gateway/gateway/internal/store"
	"github.com/JimmyWesley/rlcd-gateway/gateway/internal/web"
)

var version = "dev"

const usage = `rlcd-gateway — context gateway for coding agents

Usage:
  rlcd-gateway [serve] [-listen addr]   start the gateway and dashboard
  rlcd-gateway setup <agent>            point an agent at the gateway (claude, ...)
  rlcd-gateway undo <agent>             point it back at its provider
  rlcd-gateway version

Try it without changing any settings:
  ANTHROPIC_BASE_URL=http://127.0.0.1:4777 claude
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

func serve(args []string) {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	listen := fs.String("listen", "", "address to listen on (default from config, 127.0.0.1:4777)")
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

	// Feature packages plug into the request path through pipeline hooks and
	// mount their own endpoints; see internal/pipeline.
	pruner := prune.New(cs, st)
	rt := router.New(cs, st)
	px := proxy.New(cs, st)
	px.Hooks = pipeline.Hooks{Router: rt, Transformers: []pipeline.Transformer{pruner}}

	mux := http.NewServeMux()
	(&api.API{Config: cs, Store: st, Listen: addr}).Register(mux)
	pruner.Register(mux)
	rt.Register(mux)
	recall.New(cs, st).Register(mux)
	adapters.New(cs, st).Register(mux)
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) { http.NotFound(w, r) })
	mux.Handle("/ui/", web.Handler())
	mux.Handle("GET /{$}", http.RedirectHandler("/ui/", http.StatusFound))
	mux.Handle("/", px)

	fmt.Printf("rlcd-gateway %s\n  proxy      http://%s\n  dashboard  http://%s/ui/\n  config     %s\n  route      %s\n",
		version, addr, addr, config.Path(), cfg.ActiveRoute)
	log.Fatal(http.ListenAndServe(addr, localOnly(addr, mux)))
}

// localOnly rejects requests whose Host is not the loopback address we bound
// to. The dashboard has no login, so a web page must not be able to reach it
// through DNS rebinding.
func localOnly(addr string, next http.Handler) http.Handler {
	host, port, err := net.SplitHostPort(addr)
	if err != nil || !isLoopback(host) {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h, p, err := net.SplitHostPort(r.Host)
		if err != nil || p != port || !isLoopback(h) {
			http.Error(w, "rlcd-gateway only accepts local requests", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func isLoopback(h string) bool {
	if h == "localhost" {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}
