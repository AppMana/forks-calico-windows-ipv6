package winmtu

import (
	"strings"
	"testing"
)

func TestEndpointMTURejectsUnsafeTargets(t *testing.T) {
	for _, tc := range []struct {
		name        string
		compartment uint32
		ips         []string
		mtu         int
	}{
		{"host", 1, []string{"10.244.1.2"}, 1370},
		{"unspecified compartment", 0, []string{"10.244.1.2"}, 1370},
		{"missing address", 7, nil, 1370},
		{"injection", 7, []string{"';throw 'bad"}, 1370},
		{"loopback", 7, []string{"127.0.0.1"}, 1370},
		{"multicast", 7, []string{"ff02::1"}, 1370},
		{"unspecified", 7, []string{"0.0.0.0"}, 1370},
		{"scoped address", 7, []string{"fe80::1%2"}, 1370},
		{"too small", 7, []string{"10.244.1.2"}, 575},
		{"IPv6 minimum", 7, []string{"fd00::2"}, 1279},
		{"too large", 7, []string{"10.244.1.2"}, 65536},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := endpointMTUScript(tc.compartment, tc.ips, tc.mtu); err == nil {
				t.Fatal("unsafe MTU target accepted")
			}
		})
	}
}

func TestEndpointMTUUsesObservedPodCompartmentAndReadBack(t *testing.T) {
	script, err := endpointMTUScript(7, []string{"10.244.167.133", "fd00::2"}, 1370)
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{"$compartment=7", "$limit=1370", "'10.244.167.133','fd00::2'", "CompartmentId -eq $compartment", "[Math]::Min", "Set-NetIPInterface -NlMtuBytes", "Endpoint MTU read-back failed"} {
		if !strings.Contains(script, required) {
			t.Errorf("missing %s", required)
		}
	}
}
