// Copyright (c) 2017-2025 Tigera, Inc. All rights reserved.
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

package dataplane

import (
	"context"
	"fmt"
	"github.com/projectcalico/calico/libcalico-go/lib/winmtu"
	"os/exec"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	log "github.com/sirupsen/logrus"
	"k8s.io/client-go/kubernetes"

	"github.com/projectcalico/calico/felix/calc"
	"github.com/projectcalico/calico/felix/collector"
	"github.com/projectcalico/calico/felix/config"
	windataplane "github.com/projectcalico/calico/felix/dataplane/windows"
	"github.com/projectcalico/calico/felix/dataplane/windows/hns"
	"github.com/projectcalico/calico/libcalico-go/lib/health"
	"github.com/projectcalico/calico/libcalico-go/lib/ipam"
)

func StartDataplaneDriver(configParams *config.Config,
	healthAggregator *health.HealthAggregator,
	collector collector.Collector,
	configChangedRestartCallback func(),
	fatalErrorCallback func(error),
	k8sClientSet *kubernetes.Clientset,
	_ *calc.LookupsCache,
	_ ipam.Interface,
) (DataplaneDriver, *exec.Cmd) {
	log.Info("Using Windows dataplane driver.")

	dpConfig := windataplane.Config{
		IPv6Enabled:      configParams.Ipv6Support,
		HealthAggregator: healthAggregator,

		Hostname:     configParams.FelixHostname,
		VXLANEnabled: configParams.Encapsulation.VXLANEnabled,
		VXLANID:      configParams.VXLANVNI,
		VXLANPort:    configParams.VXLANPort,
	}

	// Windows assigns an isolated compartment after CNI ADD. Enforce the
	// explicit VXLAN MTU during endpoint policy reconciliation instead.
	if dpConfig.VXLANEnabled && configParams.VXLANMTU > 0 {
		mtu := configParams.VXLANMTU
		if configParams.Ipv6Support && configParams.VXLANMTUV6 > 0 && configParams.VXLANMTUV6 < mtu {
			mtu = configParams.VXLANMTUV6
		}
		dpConfig.EnsureEndpointMTU = func(endpointID string) error {
			return winmtu.ApplyEndpointMTU(context.Background(), endpointID, mtu)
		}
	}
	winDP := windataplane.NewWinDataplaneDriver(hns.API{}, dpConfig)
	winDP.Start()

	return winDP, nil
}

func SupportsBPF() error {
	return fmt.Errorf("BPF dataplane is not supported on Windows")
}

func ConfigurePrometheusMetrics(configParams *config.Config) {
	if configParams.PrometheusGoMetricsEnabled && configParams.PrometheusProcessMetricsEnabled {
		log.Info("Including Golang, and Process metrics")
	} else {
		if !configParams.PrometheusGoMetricsEnabled {
			log.Info("Discarding Golang metrics")
			prometheus.Unregister(collectors.NewGoCollector())
		}
		if !configParams.PrometheusProcessMetricsEnabled {
			log.Info("Discarding process metrics")
			prometheus.Unregister(collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))
		}
	}
}
