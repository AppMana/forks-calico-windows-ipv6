// Execute an explicit command through an existing lab's serial control channel.
// Unlike Resume, this does not acquire session cleanup ownership.
package main

import (
	"context"
	"crypto/sha256"
	"flag"
	"fmt"
	labv1 "github.com/appmana/labcontainers/api/v1"
	"github.com/appmana/labcontainers/pkg/client"
	"os"
	"time"
)

func main() {
	socket := flag.String("socket", "", "existing daemon socket")
	id := flag.String("session", "", "existing isolated session ID")
	node := flag.String("node", "", "explicit node")
	timeout := flag.Duration("timeout", 5*time.Minute, "command deadline")
	input := flag.String("put", "", "optional local file to upload before execution")
	digest := flag.String("sha256", "", "required SHA256 for upload")
	destination := flag.String("destination", "", "explicit guest upload path")
	flag.Parse()
	if *socket == "" || *id == "" || *node == "" || len(flag.Args()) == 0 {
		flag.Usage()
		os.Exit(2)
	}
	ctx, cancel := context.WithTimeout(context.Background(), *timeout+time.Minute)
	defer cancel()
	c, err := client.Dial(ctx, *socket)
	if err != nil {
		panic(err)
	}
	defer c.Close()
	ref := &labv1.NodeRef{SessionId: *id, Node: *node}
	if *input != "" {
		b, err := os.ReadFile(*input)
		if err != nil {
			panic(err)
		}
		if *destination == "" || fmt.Sprintf("%x", sha256.Sum256(b)) != *digest {
			panic("upload destination/checksum invalid")
		}
		if _, err := c.RPC().Put(ctx, &labv1.PutRequest{Node: ref, Path: *destination, Mode: 0755, Content: b}); err != nil {
			panic(err)
		}
	}
	r, err := c.RPC().Exec(ctx, &labv1.ExecRequest{Node: ref, Argv: flag.Args(), TimeoutMillis: timeout.Milliseconds()})
	if err != nil {
		panic(err)
	}
	fmt.Printf("%s\n%s", r.Stdout, r.Stderr)
	if r.ExitCode != 0 {
		os.Exit(1)
	}
}
