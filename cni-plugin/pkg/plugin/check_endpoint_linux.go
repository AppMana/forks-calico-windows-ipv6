package plugin

import (
	"github.com/containernetworking/cni/pkg/skel"
	"github.com/projectcalico/calico/cni-plugin/pkg/types"
)

func checkLocalEndpoint(_ *skel.CmdArgs, _ types.NetConf, _ []string) error {
	return nil
}
