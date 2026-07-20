// testapp is a deterministic guinea-pig process for supervisor tests and the
// e2e harness. It can serve HTTP, burn CPU, spawn a grandchild, ignore
// SIGTERM, and exit after a delay — every behavior the supervisor must handle.
package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

func main() {
	var (
		port          = flag.Int("port", 0, "serve HTTP on this port (0 = no server)")
		burnCPU       = flag.Bool("burn-cpu", false, "spin one core")
		spawnChild    = flag.Bool("spawn-child", false, "spawn a grandchild copy of this process")
		exitAfter     = flag.Duration("exit-after", 0, "exit with code 0 after this duration (0 = run forever)")
		ignoreSigterm = flag.Bool("ignore-sigterm", false, "ignore SIGTERM/SIGINT (forces SIGKILL escalation)")
		llmCall       = flag.Bool("llm-call", false, "POST one fake completion to $ANTHROPIC_BASE_URL/v1/messages at startup")
	)
	flag.Parse()

	log.SetFlags(log.LstdFlags | log.Lmicroseconds)
	log.Printf("testapp started pid=%d ppid=%d args=%v", os.Getpid(), os.Getppid(), os.Args[1:])

	if *ignoreSigterm {
		ch := make(chan os.Signal, 1)
		signal.Notify(ch, syscall.SIGTERM, syscall.SIGINT)
		go func() {
			for s := range ch {
				log.Printf("ignoring signal %v", s)
			}
		}()
	}

	if *spawnChild {
		child := exec.Command(os.Args[0], "-burn-cpu=false")
		if err := child.Start(); err != nil {
			log.Fatalf("spawn grandchild: %v", err)
		}
		log.Printf("spawned grandchild pid=%d", child.Process.Pid)
		go child.Wait()
	}

	if *llmCall {
		// Exercises the runtime metering path: honors the injected base URL
		// exactly like a provider SDK would.
		go func() {
			base := os.Getenv("ANTHROPIC_BASE_URL")
			if base == "" {
				log.Printf("llm-call: ANTHROPIC_BASE_URL not set, skipping")
				return
			}
			time.Sleep(500 * time.Millisecond) // let the proxy settle
			resp, err := http.Post(base+"/v1/messages", "application/json",
				strings.NewReader(`{"model":"claude-fable-5","max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`))
			if err != nil {
				log.Printf("llm-call failed: %v", err)
				return
			}
			defer resp.Body.Close()
			log.Printf("llm-call status=%d via %s", resp.StatusCode, base)
		}()
	}

	if *burnCPU {
		go func() {
			for {
				_ = fmt.Sprintf("%d", time.Now().UnixNano())
			}
		}()
		log.Printf("burning one core")
	}

	if *port != 0 {
		http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprintf(w, "testapp pid=%d\n", os.Getpid())
		})
		http.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			fmt.Fprintln(w, "ok")
		})
		go func() {
			log.Printf("listening on :%d", *port)
			log.Fatal(http.ListenAndServe(fmt.Sprintf(":%d", *port), nil))
		}()
	}

	// Heartbeat to stdout so log streaming has content.
	tick := time.NewTicker(2 * time.Second)
	defer tick.Stop()
	deadline := make(<-chan time.Time)
	if *exitAfter > 0 {
		deadline = time.After(*exitAfter)
	}
	for {
		select {
		case t := <-tick.C:
			log.Printf("heartbeat %s", t.Format(time.RFC3339))
		case <-deadline:
			log.Printf("exit-after reached, exiting 0")
			return
		}
	}
}
