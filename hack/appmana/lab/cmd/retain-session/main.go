// Retain a specific owned lab for bounded inspection, or explicitly destroy it.
package main

import (
	"context"
	"flag"
	"fmt"
	"github.com/appmana/labcontainers/pkg/client"
	"os"
	"time"
)

func main() {
	socket := flag.String("socket", "", "existing private daemon socket")
	id := flag.String("session", "", "specific existing lab session")
	ttl := flag.Duration("ttl", 2*time.Hour, "bounded retention (maximum 4h)")
	destroy := flag.Bool("destroy", false, "destroy only the explicitly named retained lab, including its disposable disks; collect evidence first")
	flag.Parse()
	if *socket == "" || *id == "" || *ttl <= 0 || *ttl > 4*time.Hour {
		flag.Usage()
		os.Exit(2)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	c, err := client.Dial(ctx, *socket)
	if err != nil {
		panic(err)
	}
	lab, err := c.Resume(ctx, *id)
	if err != nil {
		panic(err)
	}
	if *destroy {
		if err := lab.Destroy(ctx); err != nil {
			panic(err)
		}
		fmt.Printf("destroyed explicit retained session=%s socket=%s\n", lab.ID(), c.Socket())
		if err := c.Close(); err != nil {
			panic(err)
		}
		return
	}
	// On Keep failure do not Close an acquired session: Close owns teardown.
	// Process exit closes the observer's connection without stopping the owner.
	if err := lab.Keep(ctx, *ttl); err != nil {
		panic(err)
	}
	fmt.Printf("retained session=%s socket=%s state=%s artifacts=%s ttl=%s\n", lab.ID(), c.Socket(), c.StateDirectory(), lab.Artifacts(), *ttl)
	if err := c.Close(); err != nil {
		panic(err)
	}
}
