package main

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// Linux BIRD is passive toward a higher-addressed Windows mesh peer, so only
// RRAS can re-establish that session. A Windows RRAS restart sends Cease; BIRD
// then delays its restart after any earlier error (error wait time 5,30) and
// rejects the immediate RRAS reconnect, after which RRAS waits ~190s before
// retrying. Pod reachability over another path can hide that outage, so the
// crash gate observes the mesh sessions themselves.

const (
	// Longest tolerated loss of an already re-established Windows mesh session
	// during crash recovery. Exceeds BIRD's maximum error wait (30s) plus
	// RRAS reconnect handling; far below the ~190s RRAS retry interval.
	maxWindowsMeshOutage = 90 * time.Second
	// Bound from VM start until every Windows mesh session first reaches
	// Established (boot, delayed RRAS start and node initialization).
	maxWindowsMeshFirstEstablished = 10 * time.Minute
)

type birdSample struct {
	At          time.Time
	Established map[string]bool
}

// birdMeshProtocol returns Calico's BIRD protocol name for a node-mesh peer.
func birdMeshProtocol(address string) string {
	return "Mesh_" + strings.NewReplacer(".", "_", ":", "_").Replace(address)
}

// parseBirdProtocols reads `birdcl show protocols` output and reports which of
// the wanted protocols are in BGP state Established.
func parseBirdProtocols(output string, wanted []string, into map[string]bool) {
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		for _, name := range wanted {
			if fields[0] == name {
				into[name] = fields[1] == "BGP" && len(fields) >= 6 && fields[3] == "up" && fields[len(fields)-1] == "Established"
			}
		}
	}
}

type meshContinuity struct {
	FirstEstablished time.Time
	LongestOutage    time.Duration
	OutageStart      time.Time
	FinalEstablished bool
}

// analyzeMeshContinuity measures one protocol across ordered samples. An
// outage runs from the first non-Established sample after the session was up
// to the next Established sample (or the last sample when it never returns).
func analyzeMeshContinuity(samples []birdSample, protocol string) meshContinuity {
	var result meshContinuity
	var down time.Time
	for _, sample := range samples {
		up := sample.Established[protocol]
		if result.FirstEstablished.IsZero() {
			if up {
				result.FirstEstablished = sample.At
			}
			continue
		}
		switch {
		case !up && down.IsZero():
			down = sample.At
		case up && !down.IsZero():
			if d := sample.At.Sub(down); d > result.LongestOutage {
				result.LongestOutage, result.OutageStart = d, down
			}
			down = time.Time{}
		}
	}
	if len(samples) > 0 {
		last := samples[len(samples)-1]
		result.FinalEstablished = last.Established[protocol]
		if !down.IsZero() {
			if d := last.At.Sub(down); d > result.LongestOutage {
				result.LongestOutage, result.OutageStart = d, down
			}
		}
	}
	return result
}

func verifyMeshContinuity(samples []birdSample, protocols []string, started time.Time) error {
	if len(protocols) == 0 {
		return fmt.Errorf("no Windows mesh protocols to verify")
	}
	for _, protocol := range protocols {
		c := analyzeMeshContinuity(samples, protocol)
		if c.FirstEstablished.IsZero() {
			return fmt.Errorf("%s never re-established after the crash", protocol)
		}
		if c.FirstEstablished.Sub(started) > maxWindowsMeshFirstEstablished {
			return fmt.Errorf("%s first re-established %s after start (limit %s)", protocol, c.FirstEstablished.Sub(started).Round(time.Second), maxWindowsMeshFirstEstablished)
		}
		if c.LongestOutage > maxWindowsMeshOutage {
			return fmt.Errorf("%s lost its re-established session for %s from %s (limit %s)", protocol, c.LongestOutage.Round(time.Second), c.OutageStart.UTC().Format(time.RFC3339), maxWindowsMeshOutage)
		}
		if !c.FinalEstablished {
			return fmt.Errorf("%s is not Established at the end of crash recovery", protocol)
		}
	}
	return nil
}

func TestBirdMeshProtocolNames(t *testing.T) {
	for address, want := range map[string]string{"192.0.2.20": "Mesh_192_0_2_20", "fd00:10::20": "Mesh_fd00_10__20"} {
		if got := birdMeshProtocol(address); got != want {
			t.Fatalf("%s: %s", address, got)
		}
	}
}

func TestParseBirdProtocols(t *testing.T) {
	output := `BIRD v0.3.3+birdv1.6.8 ready.
name     proto    table    state  since       info
static1  Static   master   up     19:23:32
Mesh_fd00_10__20 BGP      master   start  19:52:52    Passive       Received: Administrative shutdown
Mesh_192_0_2_20 BGP      master   up     19:57:45    Established
Node_fd00_10__1 BGP      master   up     19:27:46    Established
`
	got := map[string]bool{}
	parseBirdProtocols(output, []string{"Mesh_fd00_10__20", "Mesh_192_0_2_20"}, got)
	if got["Mesh_fd00_10__20"] || !got["Mesh_192_0_2_20"] || len(got) != 2 {
		t.Fatalf("unexpected parse: %v", got)
	}
	missing := map[string]bool{}
	parseBirdProtocols("BIRD ready.\n", []string{"Mesh_192_0_2_20"}, missing)
	if missing["Mesh_192_0_2_20"] {
		t.Fatal("absent protocol reported Established")
	}
}

func meshSamples(start time.Time, step time.Duration, states ...bool) []birdSample {
	var samples []birdSample
	for i, up := range states {
		samples = append(samples, birdSample{At: start.Add(time.Duration(i) * step), Established: map[string]bool{"Mesh_x": up}})
	}
	return samples
}

func TestMeshContinuityRejectsRRASRetryOutage(t *testing.T) {
	start := time.Date(2026, 10, 8, 20, 8, 0, 0, time.UTC)
	// Down through boot, up once RRAS starts, then lost for 190s after the
	// node-service RRAS restart (BIRD rejected the immediate reconnect).
	states := []bool{false, false, false, true, true}
	for i := 0; i < 19; i++ {
		states = append(states, false)
	}
	states = append(states, true, true)
	samples := meshSamples(start, 10*time.Second, states...)
	c := analyzeMeshContinuity(samples, "Mesh_x")
	if c.LongestOutage != 190*time.Second || !c.FinalEstablished || !c.FirstEstablished.Equal(start.Add(30*time.Second)) {
		t.Fatalf("unexpected continuity: %+v", c)
	}
	if err := verifyMeshContinuity(samples, []string{"Mesh_x"}, start); err == nil || !strings.Contains(err.Error(), "lost its re-established session for 3m10s") {
		t.Fatalf("190s outage accepted: %v", err)
	}
}

func TestMeshContinuityAcceptsBoundedReconnect(t *testing.T) {
	start := time.Date(2026, 10, 8, 20, 8, 0, 0, time.UTC)
	samples := meshSamples(start, 10*time.Second, false, true, false, false, false, false, false, true, true)
	if err := verifyMeshContinuity(samples, []string{"Mesh_x"}, start); err != nil {
		t.Fatal(err)
	}
}

func TestMeshContinuityFailsClosed(t *testing.T) {
	start := time.Date(2026, 10, 8, 20, 8, 0, 0, time.UTC)
	for name, samples := range map[string][]birdSample{
		"never":   meshSamples(start, 10*time.Second, false, false, false),
		"late":    meshSamples(start, 11*time.Minute, false, true),
		"ends":    meshSamples(start, 10*time.Second, false, true, false),
		"missing": {{At: start, Established: map[string]bool{}}},
	} {
		if err := verifyMeshContinuity(samples, []string{"Mesh_x"}, start); err == nil {
			t.Fatalf("%s accepted", name)
		}
	}
	if err := verifyMeshContinuity(meshSamples(start, time.Second, true), nil, start); err == nil {
		t.Fatal("empty protocol set accepted")
	}
	// An outage still open at the end counts toward the limit.
	open := meshSamples(start, 10*time.Second, true, false, false, false, false, false, false, false, false, false, false, false)
	if c := analyzeMeshContinuity(open, "Mesh_x"); c.LongestOutage != 100*time.Second || c.FinalEstablished {
		t.Fatalf("open outage: %+v", c)
	}
}
