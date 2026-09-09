package winmtu

import (
	"fmt"
	"net/netip"
	"strings"
)

// endpointMTUScript limits the MTU of an attached endpoint without raising a
// smaller platform MTU. HCN's EncapOverhead policy does not apply a configurable
// overlay MTU on the tested Windows Server 2022/2025 builds.
func endpointMTUScript(compartment uint32, addresses []string, mtu int) (string, error) {
	if compartment <= 1 || len(addresses) == 0 {
		return "", fmt.Errorf("MTU requires an isolated endpoint compartment and addresses")
	}
	if mtu < 576 || mtu > 65535 {
		return "", fmt.Errorf("invalid endpoint MTU %d", mtu)
	}
	var quoted []string
	seen := map[netip.Addr]bool{}
	for _, value := range addresses {
		ip, err := netip.ParseAddr(value)
		if err != nil || ip.Zone() != "" || ip.IsUnspecified() || ip.IsMulticast() || ip.IsLoopback() {
			return "", fmt.Errorf("invalid endpoint address %q", value)
		}
		ip = ip.Unmap()
		if ip.Is6() && mtu < 1280 {
			return "", fmt.Errorf("IPv6 endpoint MTU must be at least 1280")
		}
		if !seen[ip] {
			quoted = append(quoted, "'"+ip.String()+"'")
			seen[ip] = true
		}
	}
	return fmt.Sprintf(`$ErrorActionPreference='Stop'
$addresses=@(%s);$compartment=%d;$limit=%d;$changes=@()
foreach($ip in $addresses){
  $a=@(Get-NetIPAddress -IncludeAllCompartments | Where-Object IPAddress -eq $ip)
  if($a.Count -ne 1){throw 'MTU endpoint address absent or ambiguous'}
  $rows=@(Get-NetIPInterface -IncludeAllCompartments | Where-Object { $_.InterfaceIndex -eq $a[0].InterfaceIndex -and $_.AddressFamily -eq $a[0].AddressFamily -and $_.CompartmentId -eq $compartment })
  if($rows.Count -ne 1){throw 'MTU endpoint compartment mismatch'}
  $row=$rows[0]
  if($row.NlMtu -lt 576 -or $row.NlMtu -gt 65535){throw 'Invalid observed endpoint MTU'}
  $changes+=@{Row=$row;Desired=[Math]::Min([int]$row.NlMtu,$limit)}
}
foreach($change in $changes){
  if($change.Row.NlMtu -ne $change.Desired){$change.Row | Set-NetIPInterface -NlMtuBytes $change.Desired}
  $row=$change.Row
  $actual=@(Get-NetIPInterface -IncludeAllCompartments | Where-Object { $_.InterfaceIndex -eq $row.InterfaceIndex -and $_.AddressFamily -eq $row.AddressFamily -and $_.CompartmentId -eq $compartment })
  if($actual.Count -ne 1 -or $actual[0].NlMtu -ne $change.Desired){throw 'Endpoint MTU read-back failed'}
}
`, strings.Join(quoted, ","), compartment, mtu), nil
}
