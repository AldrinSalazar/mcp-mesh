package agent

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"runtime"
	"strings"
	"sync"

	"github.com/coder/websocket"

	"github.com/AldrinSalazar/mcp-mesh/internal/proto"
	"github.com/AldrinSalazar/mcp-mesh/internal/wire"
)

// Pairing lets a node that has no key ask the hub for one: the hub shows
// the request to its admins, and an approval sends back a node key.
type Pairing struct {
	// Hostname goes with the request, so the admin recognizes the machine.
	Hostname string
	// OnCode is called with the code to show while the request waits for
	// an admin, and with "" once it ends.
	OnCode func(code string)
	// Save keeps the key the hub issued and the node name it is bound to.
	// If it fails the node still uses the key, until it stops.
	Save func(node, key string) error
	// Refused, if set, is why this node must not pair (say, the key would
	// cross the internet unencrypted). It is reported instead of asking.
	Refused error
}

// ErrPairingDenied means an admin declined the request. The node doesn't
// ask again until it restarts.
var ErrPairingDenied = errors.New("an admin declined this machine's pairing request")

// maxPairFrame bounds the hub's pairing frames, which carry a key and a name.
const maxPairFrame = 16 << 10

// PairURL is the pairing endpoint next to a hub node URL.
func PairURL(hubURL string) (string, error) {
	u, err := url.Parse(hubURL)
	if err != nil {
		return "", err
	}
	u.Path = strings.TrimSuffix(u.Path, "/") + strings.TrimPrefix(proto.PairPath, proto.NodePath)
	u.RawPath = ""
	return u.String(), nil
}

// pair asks the hub for a key and waits, possibly long, for an admin.
func pair(ctx context.Context, hubURL string, req proto.Pair, onCode func(string)) (*proto.Paired, error) {
	pairURL, err := PairURL(hubURL)
	if err != nil {
		return nil, err
	}
	conn, err := wire.Dial(ctx, pairURL, nil, maxPairFrame)
	if err != nil {
		var httpErr *wire.HTTPError
		if errors.As(err, &httpErr) {
			if httpErr.Status == http.StatusNotFound {
				return nil, errors.New("the hub doesn't accept pairing requests; it may be older than this node")
			}
			if reason := httpReason(httpErr.Body); reason != "" {
				return nil, errors.New(reason)
			}
		}
		return nil, err
	}
	defer conn.CloseNow()
	ctx, cancel := context.WithCancel(ctx)
	var workers sync.WaitGroup
	defer workers.Wait()
	defer cancel()
	// Waiting can take a while; the heartbeat notices a hub that went away.
	workers.Go(func() { conn.Heartbeat(ctx) })
	if err := conn.Send(ctx, &proto.Envelope{T: proto.TPair, Pair: &req}); err != nil {
		return nil, fmt.Errorf("send pairing request: %w", err)
	}
	recv := func() (*proto.Envelope, error) {
		e, err := conn.Recv(ctx)
		if err == nil {
			return e, nil
		}
		if wire.CloseStatus(err) == proto.CloseDenied {
			return nil, ErrPairingDenied
		}
		if reason := wire.CloseReason(err); reason != "" {
			return nil, errors.New(reason)
		}
		return nil, err
	}
	e, err := recv()
	if err != nil {
		return nil, err
	}
	if e.T != proto.TPairing || e.Pairing == nil || e.Pairing.Code == "" {
		return nil, fmt.Errorf("expected pairing, received %q", e.T)
	}
	if onCode != nil {
		onCode(e.Pairing.Code)
		defer onCode("")
	}
	e, err = recv()
	if err != nil {
		return nil, err
	}
	if e.T != proto.TPaired || e.Paired == nil || e.Paired.Key == "" || strings.TrimSpace(e.Paired.Node) == "" {
		return nil, fmt.Errorf("expected paired, received %q", e.T)
	}
	conn.Close(websocket.StatusNormalClosure, "paired")
	return e.Paired, nil
}

func pairRequest(node, version, hostname string) proto.Pair {
	return proto.Pair{Version: proto.Version, Node: node, Hostname: hostname, NodeVersion: version, OS: runtime.GOOS}
}
