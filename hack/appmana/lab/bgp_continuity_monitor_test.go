package main

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	labv1 "github.com/appmana/labcontainers/api/v1"
)

type execFunc func(string, time.Duration, ...string) (*labv1.ExecResponse, error)

// windowsMeshProtocols derives Linux BIRD's mesh protocol names for the Windows
// node from Calico's own node address annotations, for each configured family.
func windowsMeshProtocols(execute execFunc, node string) ([]string, error) {
	r, err := execute("linux", 30*time.Second, "k0s", "kubectl", "--request-timeout=20s", "get", "node", node, "-o",
		`jsonpath={.metadata.annotations.projectcalico\.org/IPv4Address}{"\n"}{.metadata.annotations.projectcalico\.org/IPv6Address}`)
	if err != nil {
		return nil, err
	}
	var protocols []string
	for _, line := range strings.Split(strings.TrimSpace(string(r.Stdout)), "\n") {
		address := strings.TrimSpace(strings.SplitN(line, "/", 2)[0])
		if address != "" {
			protocols = append(protocols, birdMeshProtocol(address))
		}
	}
	if len(protocols) == 0 {
		return nil, fmt.Errorf("Windows node has no Calico BGP address annotations")
	}
	return protocols, nil
}

// birdProtocolScript prints IPv4 and IPv6 BIRD protocol tables from the Linux
// calico-node pod. Read-only; it never changes BGP state.
const birdProtocolScript = `set -e
pod=$(k0s kubectl --request-timeout=10s -n kube-system get pod -l k8s-app=calico-node --field-selector spec.nodeName=linux -o name)
test -n "$pod"
k0s kubectl --request-timeout=10s -n kube-system exec "$pod" -c calico-node -- birdcl -s /var/run/calico/bird.ctl show protocols
k0s kubectl --request-timeout=10s -n kube-system exec "$pod" -c calico-node -- birdcl6 -s /var/run/calico/bird6.ctl show protocols 2>/dev/null || true
`

type meshMonitor struct {
	execute   execFunc
	protocols []string
	mu        sync.Mutex
	samples   []birdSample
	started   time.Time
	cancel    context.CancelFunc
	done      chan struct{}
}

func newMeshMonitor(execute execFunc, protocols []string) *meshMonitor {
	return &meshMonitor{execute: execute, protocols: protocols}
}

func (m *meshMonitor) sample() {
	at := time.Now()
	established := map[string]bool{}
	// A failed observation records every session as not Established; transport
	// failures must not be mistaken for continuity.
	if r, err := m.execute("linux", 25*time.Second, "sh", "-c", birdProtocolScript); err == nil {
		parseBirdProtocols(string(r.Stdout), m.protocols, established)
	}
	m.mu.Lock()
	m.samples = append(m.samples, birdSample{At: at, Established: established})
	m.mu.Unlock()
}

func (m *meshMonitor) start(ctx context.Context) {
	sampleCtx, cancel := context.WithCancel(ctx)
	m.started, m.cancel, m.done = time.Now(), cancel, make(chan struct{})
	go func() {
		defer close(m.done)
		for {
			m.sample()
			select {
			case <-sampleCtx.Done():
				return
			case <-time.After(5 * time.Second):
			}
		}
	}()
}

func (m *meshMonitor) stop() {
	if m.cancel != nil {
		m.cancel()
		<-m.done
	}
}

func (m *meshMonitor) snapshot() []birdSample {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]birdSample(nil), m.samples...)
}

// settle keeps sampling after workload verification until every session has
// stayed Established for a minute, so an outage that starts just before or
// after the consumer passes is measured to its end. Bounded by six minutes.
func (m *meshMonitor) settle(ctx context.Context) ([]birdSample, time.Time) {
	deadline := time.Now().Add(6 * time.Minute)
	for time.Now().Before(deadline) && ctx.Err() == nil {
		samples := m.snapshot()
		stable := len(samples) > 0
		for _, protocol := range m.protocols {
			var upSince time.Time
			for _, s := range samples {
				if s.Established[protocol] {
					if upSince.IsZero() {
						upSince = s.At
					}
				} else {
					upSince = time.Time{}
				}
			}
			if upSince.IsZero() || samples[len(samples)-1].At.Sub(upSince) < time.Minute {
				stable = false
			}
		}
		if stable {
			break
		}
		select {
		case <-ctx.Done():
		case <-time.After(5 * time.Second):
		}
	}
	m.stop()
	return m.snapshot(), m.started
}
