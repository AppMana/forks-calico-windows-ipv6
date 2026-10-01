// calico-windows-adapter owns the distribution integration, not host networking
// repairs. Run apply after the controller API starts, before Windows NodeReady.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"time"

	"github.com/projectcalico/calico/windows-adapter/pkg/deploy"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/tools/clientcmd"
)

var version = "development"

func run(ctx context.Context, args []string, out, diagnostics io.Writer) error {
	fs := flag.NewFlagSet("calico-windows-adapter", flag.ContinueOnError)
	fs.SetOutput(diagnostics)
	var o deploy.WindowsBGPOptions
	fs.StringVar(&o.NodeImage, "node-image", "", "immutable Calico Windows node image reference")
	fs.StringVar(&o.APIHost, "api-host", "", "Windows-reachable IPv4 Kubernetes API address")
	fs.StringVar(&o.APIPort, "api-port", "", "Kubernetes API port (443 or 6443)")
	fs.StringVar(&o.ServiceCIDR, "service-cidr", "", "IPv4 Kubernetes Service CIDR")
	fs.StringVar(&o.DNSAddress, "dns-address", "", "cluster DNS IPv4 Service address")
	fs.StringVar(&o.AutodetectionMethod, "autodetection-method", "", "explicit Calico IP autodetection method")
	fs.StringVar(&o.IPv6AutodetectionMethod, "ipv6-autodetection-method", "", "enable IPv6 with an explicit Calico host address autodetection method; does not configure pools or BGP peers")
	fs.BoolVar(&o.Offline, "offline", false, "require the digest-pinned node image to be preloaded")
	mode := fs.String("mode", "render", "render (offline), k0s-images (spec.images fragment), plan (server dry-run), or apply")
	releasePath := fs.String("release-lock", "", "versioned networking release JSON for aligned Calico/CNI and Linux/Windows kube-proxy images")
	kubeconfig := fs.String("kubeconfig", "", "explicit kubeconfig path; no implicit current cluster")
	kubecontext := fs.String("context", "", "kubeconfig context override")
	approved := fs.String("approve-sha256", "", "reviewed manifest SHA256 required for apply")
	timeout := fs.Duration("timeout", 5*time.Minute, "bounded prerequisite/API timeout")
	showVersion := fs.Bool("version", false, "print build version and exit")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("unexpected positional arguments")
	}
	if *showVersion {
		_, err := fmt.Fprintln(out, version)
		return err
	}
	if *mode != "render" && *mode != "plan" && *mode != "apply" && *mode != "k0s-images" {
		return fmt.Errorf("unknown mode %q", *mode)
	}
	if *timeout <= 0 {
		return fmt.Errorf("positive timeout required")
	}
	if *releasePath != "" {
		file, err := os.Open(*releasePath)
		if err != nil {
			return err
		}
		release, readErr := deploy.ReadRelease(file)
		closeErr := file.Close()
		if readErr != nil {
			return readErr
		}
		if closeErr != nil {
			return closeErr
		}
		o, err = release.WindowsOptions(o)
		if err != nil {
			return err
		}
		if *mode == "k0s-images" {
			images, err := release.K0sImages(o.Offline)
			if err != nil {
				return err
			}
			return json.NewEncoder(out).Encode(images)
		}
	} else if *mode == "k0s-images" {
		return fmt.Errorf("k0s-images requires --release-lock")
	}
	plan, err := deploy.NewPlan(o)
	if err != nil {
		return err
	}
	if *mode == "apply" && *approved != plan.SHA256() {
		return fmt.Errorf("apply requires --approve-sha256=%s", plan.SHA256())
	}
	if *mode != "render" {
		if *kubeconfig == "" {
			return fmt.Errorf("explicit --kubeconfig is required for %s", *mode)
		}
		loading := &clientcmd.ClientConfigLoadingRules{ExplicitPath: *kubeconfig}
		config, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(loading, &clientcmd.ConfigOverrides{CurrentContext: *kubecontext}).ClientConfig()
		if err != nil {
			return err
		}
		config.Timeout = 30 * time.Second
		config.UserAgent = "calico-windows-adapter/" + version
		client, err := dynamic.NewForConfig(config)
		if err != nil {
			return err
		}
		ctx, cancel := context.WithTimeout(ctx, *timeout)
		defer cancel()
		if *mode == "plan" {
			err = plan.Preview(ctx, client, time.Second)
		} else {
			err = plan.Apply(ctx, client, *approved, time.Second)
		}
		if err != nil {
			return err
		}
	}
	scope := "IPv4 Windows BGP bootstrap; not workload readiness"
	if o.IPv6AutodetectionMethod != "" {
		scope = "IPv6-enabled Windows BGP bootstrap over IPv4 API/DNS; not workload readiness"
	}
	if err := json.NewEncoder(diagnostics).Encode(map[string]string{"version": version, "mode": *mode, "manifestSHA256": plan.SHA256(), "scope": scope}); err != nil {
		return err
	}
	_, err = out.Write(plan.Manifest())
	return err
}

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()
	if err := run(ctx, os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
