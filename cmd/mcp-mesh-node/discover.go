package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"text/tabwriter"
	"time"

	"github.com/AldrinSalazar/mcp-mesh/internal/discovery"
	"github.com/AldrinSalazar/mcp-mesh/internal/node/nodectl"
	"github.com/AldrinSalazar/mcp-mesh/internal/proto"
	"github.com/spf13/cobra"
)

const discoveryTimeout = 3 * time.Second

const noHubsFound = "No hubs found. The hub must run with mDNS enabled (the default), on the same network. Docker bridge networks and VPNs such as Tailscale do not carry discovery; use the hub's URL instead."

var discoverHubs = func(ctx context.Context, node *nodectl.Node, timeout time.Duration) ([]discovery.Hub, error) {
	return node.Discover(ctx, timeout)
}

func searchHubs(cmd *cobra.Command, node *nodectl.Node, timeout time.Duration) ([]discovery.Hub, error) {
	fmt.Fprintln(cmd.ErrOrStderr(), "Searching for hubs on the local network…")
	return discoverHubs(cmd.Context(), node, timeout)
}

func compatibleHub(hub discovery.Hub) bool {
	return hub.Version >= proto.MinVersion && hub.Version <= proto.Version
}

func discoverCmd() *cobra.Command {
	var timeout time.Duration
	var asJSON bool
	cmd := &cobra.Command{Use: "discover", Short: "Find hubs on the local network", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		if timeout <= 0 {
			return errors.New("--timeout must be a positive duration")
		}
		node, err := commandNode(cmd)
		if err != nil {
			return err
		}
		hubs, err := searchHubs(cmd, node, timeout)
		if err != nil {
			return fmt.Errorf("discover hubs: %w", err)
		}
		return printHubs(cmd, hubs, asJSON)
	}}
	cmd.Flags().DurationVar(&timeout, "timeout", discoveryTimeout, "how long to search for hubs")
	cmd.Flags().BoolVar(&asJSON, "json", false, "print the hubs as JSON")
	return cmd
}

type hubJSON struct {
	Name       string   `json:"name"`
	URL        string   `json:"url"`
	Host       string   `json:"host"`
	IPs        []string `json:"ips"`
	Version    int      `json:"version"`
	AdminPort  int      `json:"adminPort"`
	ClientPort int      `json:"clientPort"`
	Compatible bool     `json:"compatible"`
}

func printHubs(cmd *cobra.Command, hubs []discovery.Hub, asJSON bool) error {
	if len(hubs) == 0 {
		if asJSON {
			fmt.Fprintln(cmd.ErrOrStderr(), noHubsFound)
		} else {
			fmt.Fprintln(cmd.OutOrStdout(), noHubsFound)
		}
	}
	if asJSON {
		list := make([]hubJSON, 0, len(hubs))
		for _, hub := range hubs {
			ips := make([]string, 0, len(hub.IPs))
			for _, ip := range hub.IPs {
				ips = append(ips, ip.String())
			}
			list = append(list, hubJSON{hub.Name, hub.URL, hub.Host, ips, hub.Version, hub.AdminPort, hub.ClientPort, compatibleHub(hub)})
		}
		return json.NewEncoder(cmd.OutOrStdout()).Encode(list)
	}
	if len(hubs) == 0 {
		return nil
	}
	w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "NAME\tURL\tVERSION\tADMIN")
	for _, hub := range hubs {
		version := strconv.Itoa(hub.Version)
		if !compatibleHub(hub) {
			version += " (incompatible)"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", hub.Name, hub.URL, version, hubAdminURL(hub))
	}
	return w.Flush()
}

func hubAdminURL(hub discovery.Hub) string {
	if hub.AdminPort <= 0 {
		return "-"
	}
	// Use the same address as the node endpoint when it is an IP, so the
	// admin link follows discovery's choice of a reachable interface.
	host := ""
	if endpoint, err := url.Parse(hub.URL); err == nil && net.ParseIP(endpoint.Hostname()) != nil {
		host = endpoint.Hostname()
	}
	if host == "" {
		for _, ip := range hub.IPs {
			if len(ip) != 0 {
				host = ip.String()
				break
			}
		}
	}
	if host == "" {
		return "-"
	}
	return "http://" + net.JoinHostPort(host, strconv.Itoa(hub.AdminPort))
}

func promptHub(cmd *cobra.Command, node *nodectl.Node) (string, error) {
	hubs, err := searchHubs(cmd, node, discoveryTimeout)
	if err != nil {
		if cmd.Context().Err() != nil {
			return "", cmd.Context().Err()
		}
		fmt.Fprintf(cmd.ErrOrStderr(), "Could not discover hubs: %v. Enter the hub's URL instead.\n", err)
		hubs = nil
	}
	p := newPrompt(cmd)
	if len(hubs) == 0 {
		if err == nil {
			fmt.Fprintln(cmd.ErrOrStderr(), noHubsFound)
		}
		return p.ask("Hub URL (e.g. ws://192.168.1.10:7001/node)", "ws://192.168.1.10:7001/node"), nil
	}
	for i, hub := range hubs {
		note := ""
		if !compatibleHub(hub) {
			note = " (incompatible)"
		}
		fmt.Fprintf(cmd.ErrOrStderr(), "%d. %s — %s%s\n", i+1, hub.Name, hub.URL, note)
	}
	for {
		answer, err := p.askWithError("Hub", "1")
		if err != nil {
			return "", fmt.Errorf("read hub selection: %w", err)
		}
		var chosen *discovery.Hub
		if number, err := strconv.Atoi(answer); err == nil && number >= 1 && number <= len(hubs) {
			chosen = &hubs[number-1]
		} else if endpoint, err := url.Parse(answer); err == nil && (endpoint.Scheme == "ws" || endpoint.Scheme == "wss") && endpoint.Hostname() != "" {
			for i := range hubs {
				if hubs[i].URL == answer {
					chosen = &hubs[i]
					break
				}
			}
			if chosen == nil {
				return answer, nil
			}
		} else {
			fmt.Fprintln(p.out, "Enter a hub number or a full ws:// or wss:// URL.")
			continue
		}
		if !compatibleHub(*chosen) {
			fmt.Fprintf(p.out, "Hub %q is incompatible (version %d; supported %d–%d). Choose another hub or enter its URL.\n", chosen.Name, chosen.Version, proto.MinVersion, proto.Version)
			continue
		}
		return chosen.URL, nil
	}
}
