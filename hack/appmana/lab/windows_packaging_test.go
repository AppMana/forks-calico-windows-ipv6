package main

import (
	"os"
	"strings"
	"testing"
)

func TestWindowsNodePackagesIncludeSharedRouteHelpers(t *testing.T) {
	for _, path := range []string{"../../../node/Dockerfile-windows", "../../../node/Dockerfile-windows.local", "Dockerfile.windows-node-qualification"} {
		t.Run(path, func(t *testing.T) {
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, line := range strings.Split(string(data), "\n") {
				fields := strings.Fields(line)
				if len(fields) == 3 && fields[0] == "COPY" && strings.HasSuffix(fields[1], "libs/calico/management_routes.ps1") && fields[2] == "/CalicoWindows/libs/calico/management_routes.ps1" {
					found = true
				}
			}
			if !found {
				t.Fatal("node-service.ps1 imports management_routes.ps1 but this image does not include it")
			}
		})
	}
}
