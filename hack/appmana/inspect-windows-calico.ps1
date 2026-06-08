$ErrorActionPreference = "Continue"

Write-Host "== Host =="
hostname
Get-ComputerInfo | Select-Object WindowsProductName, OsDisplayVersion, OsBuildNumber, WindowsVersion

Write-Host "== HNS networks =="
Get-HnsNetwork | ConvertTo-Json -Depth 8

Write-Host "== HNS endpoints =="
Get-HnsEndpoint | ConvertTo-Json -Depth 8

Write-Host "== Routes =="
Get-NetRoute -AddressFamily IPv4 |
  Sort-Object DestinationPrefix, RouteMetric, InterfaceMetric |
  Select-Object -First 120 DestinationPrefix, NextHop, InterfaceAlias, RouteMetric, InterfaceMetric |
  Format-Table -AutoSize
Get-NetRoute -AddressFamily IPv6 |
  Sort-Object DestinationPrefix, RouteMetric, InterfaceMetric |
  Select-Object -First 120 DestinationPrefix, NextHop, InterfaceAlias, RouteMetric, InterfaceMetric |
  Format-Table -AutoSize

Write-Host "== CalicoWindows files =="
Get-ChildItem C:\CalicoWindows -Force -ErrorAction SilentlyContinue |
  Select-Object Name, FullName, Length, LastWriteTime |
  Format-Table -AutoSize

Write-Host "== Host CNI files =="
Get-ChildItem C:\etc\cni\net.d -Force -ErrorAction SilentlyContinue |
  Select-Object Name, FullName, Length, LastWriteTime |
  Format-Table -AutoSize

foreach ($path in @(
  "C:\CalicoWindows\nodename",
  "C:\etc\cni\net.d\calico-kubeconfig",
  "C:\etc\cni\net.d\10-calico.conf",
  "C:\etc\cni\net.d\config.ps1"
)) {
  Write-Host "== $path =="
  if (Test-Path $path) {
    Get-Content $path -ErrorAction Continue
  } else {
    Write-Host "missing"
  }
}

Write-Host "== Calico-ish logs =="
Get-ChildItem C:\var\log, C:\CalicoWindows -Recurse -File -ErrorAction SilentlyContinue |
  Where-Object { $_.FullName -match "calico|felix|confd|node|hook|bgp|bird" } |
  Sort-Object LastWriteTime -Descending |
  Select-Object -First 60 FullName, LastWriteTime, Length |
  Format-Table -AutoSize

foreach ($path in @(
  "C:\var\log\calico\calico-node.log",
  "C:\var\log\calico\calico-node.err.log",
  "C:\var\log\calico\calico-felix.log",
  "C:\var\log\calico\calico-felix.err.log",
  "C:\var\log\calico\calico-confd.log",
  "C:\var\log\calico\calico-confd.err.log",
  "C:\var\log\calico\hook.log",
  "C:\CalicoWindows\calico-node.log",
  "C:\CalicoWindows\calico-felix.log"
)) {
  Write-Host "== tail $path =="
  if (Test-Path $path) {
    Get-Content $path -Tail 160 -ErrorAction Continue
  } else {
    Write-Host "missing"
  }
}
