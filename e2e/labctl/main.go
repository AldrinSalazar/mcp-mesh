// Command labctl manages the Hyper-V machines of the end-to-end tests by
// hand: the tests do the same on their own.
//
//	go run ./e2e/labctl up              # prepare every machine (first run: about 10 minutes)
//	go run ./e2e/labctl reset hub       # back to the clean checkpoint, booted
//	go run ./e2e/labctl status
//	go run ./e2e/labctl ssh node-a      # prints the ssh command
//	go run ./e2e/labctl run hub 'sudo journalctl -u mcp-mesh-hub'
//	go run ./e2e/labctl down            # power everything off
//	go run ./e2e/labctl destroy         # remove the VMs (the base image stays)
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"

	"github.com/AldrinSalazar/mcp-mesh/e2e/hyperv"
	"github.com/AldrinSalazar/mcp-mesh/e2e/lab"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: labctl up|reset|status|ssh|down|destroy [machine...]")
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	dir, err := lab.Dir()
	fail(err)
	l := lab.New(dir, slog.New(slog.NewTextHandler(os.Stderr, nil)))
	fail(l.Start(ctx))
	defer l.Close()
	if os.Args[1] == "run" {
		if len(os.Args) < 4 {
			fail(fmt.Errorf("usage: labctl run MACHINE COMMAND..."))
		}
		m, err := l.Attach(ctx, os.Args[2])
		fail(err)
		out, err := m.Run(ctx, strings.Join(os.Args[3:], " "))
		fmt.Print(out)
		fail(err)
		return
	}
	names := os.Args[2:]
	specs := lab.Machines
	if len(names) > 0 {
		specs = nil
		for _, n := range names {
			s, ok := lab.Spec(n)
			if !ok {
				fail(fmt.Errorf("unknown machine %q", n))
			}
			specs = append(specs, s)
		}
	}
	all := make([]string, len(specs))
	for i, s := range specs {
		all[i] = s.Name
	}
	switch os.Args[1] {
	case "up":
		for _, s := range specs {
			fail(l.Ensure(ctx, s))
		}
	case "reset":
		for _, n := range all {
			m, err := l.Reset(ctx, n)
			fail(err)
			fmt.Printf("%s %s\n", m.Name, m.IP)
		}
	case "status":
		for _, n := range all {
			info, ok, err := hyperv.Get(ctx, lab.VMPrefix+n)
			fail(err)
			if !ok {
				fmt.Printf("%-8s missing\n", n)
				continue
			}
			fmt.Printf("%-8s %-8s %v\n", n, info.State, info.IPs)
		}
	case "ssh":
		for _, n := range all {
			m, err := l.Attach(ctx, n)
			fail(err)
			fmt.Printf("ssh -i %s -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null %s@%s\n", l.KeyPath(), lab.User, m.IP)
		}
	case "down":
		fail(l.Down(ctx, all...))
	case "destroy":
		fail(l.Destroy(ctx, all...))
	default:
		fail(fmt.Errorf("unknown command %q", os.Args[1]))
	}
}

func fail(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "labctl:", err)
		os.Exit(1)
	}
}
