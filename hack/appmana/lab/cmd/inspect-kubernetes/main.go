// Read-only, serial-QGA diagnostics for an already running Kubernetes fixture.
package main

import (
	"context"
	"flag"
	"fmt"
	labv1 "github.com/appmana/labcontainers/api/v1"
	"github.com/appmana/labcontainers/pkg/client"
	"os"
	"time"
)

func main() {
	socket := flag.String("socket", "", "existing private daemon socket")
	session := flag.String("session", "", "existing isolated session ID")
	flag.Parse()
	if *socket == "" || *session == "" {
		flag.Usage()
		os.Exit(2)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	c, err := client.Dial(ctx, *socket)
	if err != nil {
		panic(err)
	}
	// Do not Resume: an observer must never acquire cleanup ownership.
	defer c.Close()
	r, err := c.RPC().Exec(ctx, &labv1.ExecRequest{Node: &labv1.NodeRef{SessionId: *session, Node: "linux"}, Argv: []string{"sh", "-c", "k0s kubectl get nodes,pods -A -o wide; k0s kubectl get events -A --sort-by=.metadata.creationTimestamp | tail -35"}, TimeoutMillis: 45000})
	if err != nil {
		panic(err)
	}
	fmt.Printf("%s\n%s\n", r.Stdout, r.Stderr)
	if r.ExitCode != 0 {
		os.Exit(1)
	}
}
