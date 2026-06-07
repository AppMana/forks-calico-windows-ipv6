# Copyright (c) 2018-2020 Tigera, Inc. All rights reserved.
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http:#www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

# This script may be run from the main Calico directory or directly from
# its script path by HostProcess/containerd. Resolve imports from the
# install root instead of assuming the process working directory.
$calicoRoot = Split-Path $PSScriptRoot -Parent
if ($PSScriptRoot -match '^[A-Za-z]:[\\/]hpc[\\/]CalicoWindows[\\/]confd[\\/]?$') {
  $sandboxRoot = $calicoRoot
  $hostRoot = $env:CALICO_HOST_INSTALL_DIR
  if ([string]::IsNullOrEmpty($hostRoot)) { $hostRoot = "C:\CalicoWindows" }
  $sandboxExe = Join-Path $sandboxRoot "calico-node.exe"
  $hostExe = Join-Path $hostRoot "calico-node.exe"
  $deadline = (Get-Date).AddSeconds(120)
  while ($true) {
    if ((Test-Path $sandboxExe) -and (Test-Path $hostExe)) {
      try {
        $sandboxHash = (Get-FileHash $sandboxExe -Algorithm SHA256 -ErrorAction Stop).Hash
        $hostHash = (Get-FileHash $hostExe -Algorithm SHA256 -ErrorAction Stop).Hash
        if ($sandboxHash -eq $hostHash) {
          break
        }
        Write-Host "Waiting for host CalicoWindows mirror to match sandbox image..."
      } catch {
        Write-Host ("Waiting for host CalicoWindows mirror: " + $_.Exception.Message)
      }
    } else {
      Write-Host "Waiting for host CalicoWindows mirror to install calico-node.exe..."
    }
    if ((Get-Date) -gt $deadline) {
      throw "Timed out waiting for $hostExe to match $sandboxExe"
    }
    Start-Sleep 1
  }
  $calicoRoot = $hostRoot
}
. (Join-Path $calicoRoot "config.ps1")

ipmo (Join-Path $calicoRoot "libs\calico\calico.psm1") -Force

# Autoconfigure the IPAM block mode.
if ($env:CNI_IPAM_TYPE -EQ "host-local") {
  $env:USE_POD_CIDR = "true"
} else {
  $env:USE_POD_CIDR = "false"
}

if($env:CALICO_NETWORKING_BACKEND -EQ "windows-bgp")
{
  Wait-ForCalicoInit
  Write-Host "Windows BGP is enabled, running confd..."

  if ($env:CALICO_CONFD_HOST_PATH) {
    $confdDir = $env:CALICO_CONFD_HOST_PATH
  } else {
    $confdDir = Join-Path $calicoRoot "confd"
  }
  $calicoNodeExe = Join-Path (Split-Path $confdDir -Parent) "calico-node.exe"

  Write-Host "Using confd directory $confdDir"
  cd "$confdDir"

  # Remove the old peerings and blocks so that confd will always trigger
  # reconfiguration at start of day.  This ensures that stopping and starting the service
  # reliably recovers from previous failures.
  rm peerings.ps1 -ErrorAction SilentlyContinue
  rm blocks.ps1 -ErrorAction SilentlyContinue

  # Run the calico-confd binary.
  & "$calicoNodeExe" -confd -confd-confdir="$confdDir"
} else {
  Write-Host "Windows BGP is disabled, not running confd."
  while($True) {
    Start-Sleep 10
  }
}
