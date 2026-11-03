// Copyright (c) 2026 Tigera, Inc. All rights reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package node

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/sirupsen/logrus"
	"github.com/spf13/cobra"

	confdConfig "github.com/projectcalico/calico/confd/pkg/config"
	confd "github.com/projectcalico/calico/confd/pkg/run"
	felix "github.com/projectcalico/calico/felix/daemon"
	"github.com/projectcalico/calico/lib/logrusr"
	"github.com/projectcalico/calico/node/cmd/calico-node/bpf"
	"github.com/projectcalico/calico/node/pkg/allocateip"
	"github.com/projectcalico/calico/node/pkg/cni"
	"github.com/projectcalico/calico/node/pkg/flowlogs"
	"github.com/projectcalico/calico/node/pkg/health"
	"github.com/projectcalico/calico/node/pkg/hostpathinit"
	"github.com/projectcalico/calico/node/pkg/lifecycle/shutdown"
	"github.com/projectcalico/calico/node/pkg/lifecycle/startup"
	"github.com/projectcalico/calico/node/pkg/lifecycle/utils"
	"github.com/projectcalico/calico/node/pkg/nodeinit"
	"github.com/projectcalico/calico/node/pkg/nodeservices"
	"github.com/projectcalico/calico/node/pkg/status"
	"github.com/projectcalico/calico/pkg/buildinfo"
)

// NewCommand returns a cobra command tree for node lifecycle operations.
func NewCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "node",
		Short: "Calico node lifecycle operations",
	}

	cmd.AddCommand(
		newInitCommand(),
		newStartupCommand(),
		newShutdownCommand(),
		newNodeServicesCommand(),
		newMonitorAddressesCommand(),
		newAllocateTunnelAddrsCommand(),
		newMonitorTokenCommand(),
		newCompleteStartupCommand(),
		newHostpathInitCommand(),
		newHealthCommand(),
		newStatusCommand(),
		newBPFCommand(),
		newFlowsCommand(),
		newVersionCommand(),
	)

	return cmd
}

// NewFelixCommand returns the command to run the Felix policy agent.
func NewFelixCommand() *cobra.Command {
	return newFelixCommand()
}

// NewConfdCommand returns the command to run the confd configuration daemon.
func NewConfdCommand() *cobra.Command {
	return newConfdCommand()
}

func newFelixCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "felix",
		Short: "Run the Felix policy agent",
		Run: func(cmd *cobra.Command, args []string) {
			logrus.SetFormatter(&logrusr.Formatter{Component: "felix"})
			felix.Run("/etc/calico/felix.cfg", buildinfo.Version, buildinfo.BuildDate, buildinfo.GitRevision)
		},
	}
}

func newConfdCommand() *cobra.Command {
	var runOnce bool
	var keepStageFile bool
	var confDir string

	cmd := &cobra.Command{
		Use:   "confd",
		Short: "Run the confd configuration daemon",
		Run: func(cmd *cobra.Command, args []string) {
			logrus.SetFormatter(&logrusr.Formatter{Component: "confd"})
			cfg, err := confdConfig.InitConfig(true)
			if err != nil {
				logrus.WithError(err).Fatal("Failed to initialize confd config")
			}
			cfg.ConfDir = confDir
			cfg.KeepStageFile = keepStageFile
			cfg.Onetime = runOnce
			confd.Run(cfg)
		},
	}

	cmd.Flags().BoolVar(&runOnce, "run-once", false, "Run confd in oneshot mode")
	cmd.Flags().BoolVar(&keepStageFile, "keep-stage-file", false, "Keep stage file when running confd")
	cmd.Flags().StringVar(&confDir, "confdir", "/etc/calico/confd", "Confd configuration directory")

	return cmd
}

func newInitCommand() *cobra.Command {
	var bestEffort bool

	cmd := &cobra.Command{
		Use:   "init",
		Short: "Do privileged initialisation of a new node",
		Run: func(cmd *cobra.Command, args []string) {
			if bestEffort {
				logrus.SetFormatter(&logrusr.Formatter{Component: "init-best-effort"})
			} else {
				logrus.SetFormatter(&logrusr.Formatter{Component: "init"})
			}
			nodeinit.Run(bestEffort)
		},
	}

	cmd.Flags().BoolVar(&bestEffort, "best-effort", false, "Report errors but do not fail if an error occurs during initialisation")

	return cmd
}

func newStartupCommand() *cobra.Command {
	var completeStartup bool

	cmd := &cobra.Command{
		Use:   "startup",
		Short: "Do non-privileged start-up routine",
		Run: func(cmd *cobra.Command, args []string) {
			logrus.SetFormatter(&logrusr.Formatter{Component: "startup"})
			startup.Run()
			if completeStartup {
				if err := startup.MarkNetworkAvailable(); err != nil {
					utils.Terminate()
				}
			}
		},
	}

	cmd.Flags().BoolVar(&completeStartup, "complete-startup", false, "Mark the node as available immediately after startup")

	return cmd
}

func newShutdownCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "shutdown",
		Short: "Do shutdown routine",
		Run: func(cmd *cobra.Command, args []string) {
			logrus.SetFormatter(&logrusr.Formatter{Component: "shutdown"})
			shutdown.Run()
		},
	}
}

func newNodeServicesCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "node-services",
		Short: "Run consolidated node services (complete-startup, tunnel-ip-allocator, monitor-addresses, node-status-reporter, cni-config-monitor)",
		Run: func(cmd *cobra.Command, args []string) {
			logrusr.ConfigureFormatter("node-services")
			nodeservices.Run()
		},
	}
}

func newMonitorAddressesCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "monitor-addresses",
		Short: "Monitor change in node IP addresses",
		Run: func(cmd *cobra.Command, args []string) {
			logrus.SetFormatter(&logrusr.Formatter{Component: "monitor-addresses"})
			startup.ConfigureLogging()
			if err := startup.MonitorIPAddressSubnetsWithContext(context.Background()); err != nil {
				logrus.WithError(err).Fatal("Monitor addresses failed")
			}
		},
	}
}

func newAllocateTunnelAddrsCommand() *cobra.Command {
	var runOnce bool

	cmd := &cobra.Command{
		Use:   "allocate-tunnel-addrs",
		Short: "Configure tunnel addresses for this node",
		Run: func(cmd *cobra.Command, args []string) {
			logrus.SetFormatter(&logrusr.Formatter{Component: "tunnel-ip-allocator"})
			if err := allocateip.Run(context.Background(), runOnce); err != nil {
				logrus.WithError(err).Fatal("Tunnel IP allocator failed")
			}
		},
	}

	cmd.Flags().BoolVar(&runOnce, "run-once", false, "Run in oneshot mode")

	return cmd
}

func newMonitorTokenCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "monitor-token",
		Short: "Watch for Kubernetes token changes, update CNI config",
		Run: func(cmd *cobra.Command, args []string) {
			logrus.SetFormatter(&logrusr.Formatter{Component: "cni-config-monitor"})
			if err := cni.RunWithContext(context.Background()); err != nil {
				logrus.WithError(err).Fatal("CNI config monitor failed")
			}
		},
	}
}

func newCompleteStartupCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "complete-startup",
		Short: "Update the NetworkUnavailable condition in Kubernetes on successful startup",
		Run: func(cmd *cobra.Command, args []string) {
			logrus.SetFormatter(&logrusr.Formatter{Component: "complete-startup"})
			ctx := context.Background()
			if err := startup.ManageNodeCondition(ctx, 5*time.Minute); err != nil {
				utils.Terminate()
			}
		},
	}
}

func newHostpathInitCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "hostpath-init",
		Short: "Initialize hostpaths for non-root access",
		Run: func(cmd *cobra.Command, args []string) {
			logrus.SetFormatter(&logrusr.Formatter{Component: "hostpath-init"})
			hostpathinit.Run()
		},
	}
}

func newHealthCommand() *cobra.Command {
	var felixLive, felixReady bool
	var birdReady, bird6Ready bool
	var birdLive, bird6Live bool
	var thresholdTime time.Duration

	cmd := &cobra.Command{
		Use:   "health",
		Short: "Run node health checks",
		Run: func(cmd *cobra.Command, args []string) {
			health.Run(birdReady, bird6Ready, felixReady, felixLive, birdLive, bird6Live, thresholdTime)
		},
	}

	cmd.Flags().BoolVar(&felixLive, "felix-live", false, "Run felix liveness checks")
	cmd.Flags().BoolVar(&felixReady, "felix-ready", false, "Run felix readiness checks")
	cmd.Flags().BoolVar(&birdReady, "bird-ready", false, "Run BIRD readiness checks")
	cmd.Flags().BoolVar(&bird6Ready, "bird6-ready", false, "Run BIRD6 readiness checks")
	cmd.Flags().BoolVar(&birdLive, "bird-live", false, "Run BIRD liveness checks")
	cmd.Flags().BoolVar(&bird6Live, "bird6-live", false, "Run BIRD6 liveness checks")
	cmd.Flags().DurationVar(&thresholdTime, "threshold-time", 30*time.Second, "Threshold time for bird readiness")

	return cmd
}

func newStatusCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Node status operations",
	}

	cmd.AddCommand(&cobra.Command{
		Use:   "report",
		Short: "Run node status reporter",
		Run: func(cmd *cobra.Command, args []string) {
			logrus.SetFormatter(&logrusr.Formatter{Component: "status-reporter"})
			if err := status.RunWithContext(context.Background()); err != nil {
				logrus.WithError(err).Fatal("Status reporter failed")
			}
		},
	})

	cmd.AddCommand(&cobra.Command{
		Use:   "show",
		Short: "Print out node status",
		Run: func(cmd *cobra.Command, args []string) {
			status.Show()
		},
	})

	return cmd
}

func newBPFCommand() *cobra.Command {
	return &cobra.Command{
		Use:                "bpf",
		Short:              "BPF debug tools",
		DisableFlagParsing: true,
		Run: func(cmd *cobra.Command, args []string) {
			logrus.SetOutput(os.Stderr)
			os.Args = append([]string{"calico-bpf"}, args...)
			bpf.RunBPFCmd()
		},
	}
}

func newFlowsCommand() *cobra.Command {
	var numFlows int

	cmd := &cobra.Command{
		Use:   "flows",
		Short: "Fetch or watch node flow logs",
		Run: func(cmd *cobra.Command, args []string) {
			if numFlows == 0 {
				fmt.Println("Specify --num (-n) with a positive number to fetch flows, or negative to watch")
				os.Exit(1)
			}
			flowlogs.RunFlowsCmd(numFlows)
		},
	}

	cmd.Flags().IntVarP(&numFlows, "num", "n", 0, "Number of flows to fetch (negative = watch forever)")

	return cmd
}

func newVersionCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print version information",
		Run: func(cmd *cobra.Command, args []string) {
			buildinfo.PrintVersion()
		},
	}
}

// RewriteLegacyArgs translates pre-v3.33 single-dash calico-node flags
// (e.g. -felix-live, -bird-ready, -startup, -complete-startup, -confd,
// -felix, -shutdown, -init, -monitor-token) into their v3.33 Cobra
// subcommand equivalents. If args do not match legacy flags, it returns
// args unchanged and false.
func RewriteLegacyArgs(args []string) ([]string, bool) {
	if len(args) <= 1 || !strings.HasPrefix(args[1], "-") || args[1] == "-h" || args[1] == "--help" {
		return args, false
	}
	if args[1] == "-bpf" || args[1] == "--bpf" {
		return append([]string{args[0], "component", "node", "bpf"}, args[2:]...), true
	}

	flagSet := flag.NewFlagSet("Calico", flag.ContinueOnError)
	flagSet.SetOutput(io.Discard)

	version := flagSet.Bool("v", false, "Display version")
	runNodeServices := flagSet.Bool("node-services", false, "Run consolidated node services")
	monitorAddrs := flagSet.Bool("monitor-addresses", false, "Monitor change in node IP addresses")
	monitorToken := flagSet.Bool("monitor-token", false, "Watch for Kubernetes token changes")
	runStatusReporter := flagSet.Bool("status-reporter", false, "Run node status reporter")
	runFelix := flagSet.Bool("felix", false, "Run Felix")
	flows := flagSet.Int("flows", 0, "Fetch a number of Flows")
	runInit := flagSet.Bool("init", false, "Do privileged initialisation of a new node")
	bestEffort := flagSet.Bool("best-effort", false, "Report errors but do not fail during init")
	initHostpaths := flagSet.Bool("hostpath-init", false, "Initialize hostpaths for non-root access")
	runStartup := flagSet.Bool("startup", false, "Do non-privileged start-up routine")
	runShutdown := flagSet.Bool("shutdown", false, "Do shutdown routine")
	completeStartup := flagSet.Bool("complete-startup", false, "Update NetworkUnavailable condition")
	runAllocateTunnelAddrs := flagSet.Bool("allocate-tunnel-addrs", false, "Configure tunnel addresses")
	allocateTunnelAddrsRunOnce := flagSet.Bool("allocate-tunnel-addrs-run-once", false, "Run allocate-tunnel-addrs once")
	felixLive := flagSet.Bool("felix-live", false, "Run felix liveness checks")
	birdLive := flagSet.Bool("bird-live", false, "Run bird liveness checks")
	bird6Live := flagSet.Bool("bird6-live", false, "Run bird6 liveness checks")
	birdReady := flagSet.Bool("bird-ready", false, "Run BIRD readiness checks")
	bird6Ready := flagSet.Bool("bird6-ready", false, "Run BIRD6 readiness checks")
	felixReady := flagSet.Bool("felix-ready", false, "Run felix readiness checks")
	thresholdTime := flagSet.Duration("threshold-time", 30*time.Second, "Threshold time for bird readiness")
	showStatus := flagSet.Bool("show-status", false, "Print out node status")
	runConfd := flagSet.Bool("confd", false, "Run confd")
	confdRunOnce := flagSet.Bool("confd-run-once", false, "Run confd in oneshot mode")
	confdKeep := flagSet.Bool("confd-keep-stage-file", false, "Keep stage file when running confd")
	confdConfDir := flagSet.String("confd-confdir", "/etc/calico/confd", "Confd configuration directory")

	if err := flagSet.Parse(args[1:]); err != nil || flagSet.NArg() > 0 {
		return args, false
	}

	if *felixLive || *birdReady || *bird6Ready || *felixReady || *birdLive || *bird6Live {
		return []string{
			args[0], "component", "node", "health",
			fmt.Sprintf("--felix-live=%t", *felixLive),
			fmt.Sprintf("--felix-ready=%t", *felixReady),
			fmt.Sprintf("--bird-live=%t", *birdLive),
			fmt.Sprintf("--bird6-live=%t", *bird6Live),
			fmt.Sprintf("--bird-ready=%t", *birdReady),
			fmt.Sprintf("--bird6-ready=%t", *bird6Ready),
			fmt.Sprintf("--threshold-time=%s", thresholdTime.String()),
		}, true
	}

	switch {
	case *version:
		return []string{args[0], "component", "node", "version"}, true
	case *runFelix:
		return []string{args[0], "component", "felix"}, true
	case *runInit:
		return []string{args[0], "component", "node", "init", fmt.Sprintf("--best-effort=%t", *bestEffort)}, true
	case *runStartup:
		return []string{args[0], "component", "node", "startup", fmt.Sprintf("--complete-startup=%t", *completeStartup)}, true
	case *runShutdown:
		return []string{args[0], "component", "node", "shutdown"}, true
	case *completeStartup:
		return []string{args[0], "component", "node", "complete-startup"}, true
	case *runConfd:
		return []string{
			args[0], "component", "confd",
			fmt.Sprintf("--confdir=%s", *confdConfDir),
			fmt.Sprintf("--run-once=%t", *confdRunOnce),
			fmt.Sprintf("--keep-stage-file=%t", *confdKeep),
		}, true
	case *runAllocateTunnelAddrs:
		return []string{args[0], "component", "node", "allocate-tunnel-addrs", fmt.Sprintf("--run-once=%t", *allocateTunnelAddrsRunOnce)}, true
	case *runNodeServices:
		return []string{args[0], "component", "node", "node-services"}, true
	case *monitorAddrs:
		return []string{args[0], "component", "node", "monitor-addresses"}, true
	case *monitorToken:
		return []string{args[0], "component", "node", "monitor-token"}, true
	case *runStatusReporter:
		return []string{args[0], "component", "node", "status", "report"}, true
	case *initHostpaths:
		return []string{args[0], "component", "node", "hostpath-init"}, true
	case *showStatus:
		return []string{args[0], "component", "node", "status", "show"}, true
	case *flows != 0:
		return []string{args[0], "component", "node", "flows", fmt.Sprintf("--num=%d", *flows)}, true
	default:
		return args, false
	}
}
