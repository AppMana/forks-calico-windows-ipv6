# Pester tests for calico.psm1's CNI-config-generation helpers.
#
# Runs on Linux (pwsh) or Windows. Windows-specific cmdlets used inside
# calico.psm1 (Get-NetAdapter, Get-Service, Get-ComputerInfo) are NOT
# called by the functions under test as long as we mock
# Get-IsDSRSupported and Get-IsContainerdRunning.

BeforeAll {
    $modulePath = "$PSScriptRoot/../CalicoWindows/libs/calico/calico.psm1"
    Import-Module $modulePath -Force
    $script:moduleName = (Get-Item $modulePath).BaseName

    # Set the env vars Build-CNIConfigSubstitutions reads. Tests can override
    # individual ones with their own $env:VAR = ... assignments.
    $env:KUBECONFIG = "C:\CalicoWindows\calico-kube-config"
    $env:K8S_SERVICE_CIDR = "10.96.0.0/12"
    $env:DNS_NAME_SERVERS = "10.96.0.10"
    $env:CALICO_DATASTORE_TYPE = "kubernetes"
    $env:ETCD_ENDPOINTS = ""
    $env:ETCD_KEY_FILE = ""
    $env:ETCD_CERT_FILE = ""
    $env:ETCD_CA_CERT_FILE = ""
    $env:CNI_IPAM_TYPE = "calico-ipam"
    $env:CALICO_NETWORKING_BACKEND = "windows-bgp"
    $env:VXLAN_VNI = "4096"
    $env:VXLAN_MAC_PREFIX = "0E-2A"
    $env:CALICO_DSR_DISABLE = $null
}

Describe "Get-DSRSupport" {

    Context "with CALICO_DSR_DISABLE=true" {
        BeforeEach { $env:CALICO_DSR_DISABLE = "true" }
        AfterEach  { $env:CALICO_DSR_DISABLE = $null }

        It "returns false even when the OS supports DSR" {
            InModuleScope $script:moduleName {
                Mock Get-IsDSRSupported { $true }
                Get-DSRSupport | Should -Be "false"
            }
        }
    }

    Context "with CALICO_DSR_DISABLE=false (or unset) on a DSR-capable OS" {
        BeforeEach { $env:CALICO_DSR_DISABLE = $null }

        It "returns true when Get-IsDSRSupported is true" {
            InModuleScope $script:moduleName {
                Mock Get-IsDSRSupported { $true }
                Get-DSRSupport | Should -Be "true"
            }
        }

        It "returns false when Get-IsDSRSupported is false" {
            InModuleScope $script:moduleName {
                Mock Get-IsDSRSupported { $false }
                Get-DSRSupport | Should -Be "false"
            }
        }
    }

    Context "with CALICO_DSR_DISABLE set to other values" {

        It "returns true (DSR enabled) for CALICO_DSR_DISABLE='false'" {
            $env:CALICO_DSR_DISABLE = "false"
            InModuleScope $script:moduleName {
                Mock Get-IsDSRSupported { $true }
                Get-DSRSupport | Should -Be "true"
            }
            $env:CALICO_DSR_DISABLE = $null
        }

        It "returns true for any non-true value (only literal 'true' disables)" {
            $env:CALICO_DSR_DISABLE = "1"
            InModuleScope $script:moduleName {
                Mock Get-IsDSRSupported { $true }
                Get-DSRSupport | Should -Be "true"
            }
            $env:CALICO_DSR_DISABLE = $null
        }
    }
}

Describe "Build-CNIConfigSubstitutions" {

    It "produces a hashtable containing every __PLACEHOLDER__ used in the template" {
        InModuleScope $script:moduleName {
            Mock Get-IsContainerdRunning { $true }
            Mock Get-IsDSRSupported { $true }

            $subs = Build-CNIConfigSubstitutions -BaseDir "C:\CalicoWindows"
            # All keys that the template references must be present.
            foreach ($key in @(
                'NODENAME_FILE','KUBECONFIG','K8S_SERVICE_CIDR','DNS_NAME_SERVERS',
                'DATASTORE_TYPE','DSR_SUPPORT','ETCD_ENDPOINTS','ETCD_KEY_FILE',
                'ETCD_CERT_FILE','ETCD_CA_CERT_FILE','IPAM_TYPE','MODE','VNI',
                'MAC_PREFIX','ROUTE_TYPE'))
            {
                $subs.ContainsKey($key) | Should -BeTrue -Because "missing key $key"
            }
        }
    }

    It "uses SDNROUTE when containerd is running" {
        InModuleScope $script:moduleName {
            Mock Get-IsContainerdRunning { $true }
            Mock Get-IsDSRSupported { $false }
            $subs = Build-CNIConfigSubstitutions -BaseDir "C:\CalicoWindows"
            $subs.ROUTE_TYPE | Should -Be "SDNROUTE"
        }
    }

    It "uses ROUTE when containerd is not running" {
        InModuleScope $script:moduleName {
            Mock Get-IsContainerdRunning { $false }
            Mock Get-IsDSRSupported { $false }
            $subs = Build-CNIConfigSubstitutions -BaseDir "C:\CalicoWindows"
            $subs.ROUTE_TYPE | Should -Be "ROUTE"
        }
    }

    It "uses MODE=vxlan when CALICO_NETWORKING_BACKEND=vxlan" {
        $env:CALICO_NETWORKING_BACKEND = "vxlan"
        InModuleScope $script:moduleName {
            Mock Get-IsContainerdRunning { $true }
            Mock Get-IsDSRSupported { $false }
            $subs = Build-CNIConfigSubstitutions -BaseDir "C:\CalicoWindows"
            $subs.MODE | Should -Be "vxlan"
        }
        $env:CALICO_NETWORKING_BACKEND = "windows-bgp"
    }

    It "leaves MODE empty for windows-bgp" {
        InModuleScope $script:moduleName {
            Mock Get-IsContainerdRunning { $true }
            Mock Get-IsDSRSupported { $false }
            $subs = Build-CNIConfigSubstitutions -BaseDir "C:\CalicoWindows"
            $subs.MODE | Should -Be ""
        }
    }

    It "splits multi-DNS-server input into JSON-quoted comma list" {
        $env:DNS_NAME_SERVERS = "10.96.0.10,10.96.0.11"
        InModuleScope $script:moduleName {
            Mock Get-IsContainerdRunning { $true }
            Mock Get-IsDSRSupported { $false }
            $subs = Build-CNIConfigSubstitutions -BaseDir "C:\CalicoWindows"
            $subs.DNS_NAME_SERVERS | Should -Be '"10.96.0.10","10.96.0.11"'
        }
        $env:DNS_NAME_SERVERS = "10.96.0.10"
    }

    It "passes through CALICO_DSR_DISABLE to the DSR_SUPPORT key" {
        $env:CALICO_DSR_DISABLE = "true"
        InModuleScope $script:moduleName {
            Mock Get-IsContainerdRunning { $true }
            Mock Get-IsDSRSupported { $true }
            $subs = Build-CNIConfigSubstitutions -BaseDir "C:\CalicoWindows"
            $subs.DSR_SUPPORT | Should -Be "false"
        }
        $env:CALICO_DSR_DISABLE = $null
    }

    Context "default values when env vars are unset" {

        BeforeEach {
            $script:savedDS = $env:CALICO_DATASTORE_TYPE
            $script:savedIPAM = $env:CNI_IPAM_TYPE
            $script:savedNodenameHost = $env:CALICO_NODENAME_FILE_HOST_PATH
            $env:CALICO_DATASTORE_TYPE = $null
            $env:CNI_IPAM_TYPE = $null
            $env:CALICO_NODENAME_FILE_HOST_PATH = $null
        }
        AfterEach {
            $env:CALICO_DATASTORE_TYPE = $script:savedDS
            $env:CNI_IPAM_TYPE = $script:savedIPAM
            $env:CALICO_NODENAME_FILE_HOST_PATH = $script:savedNodenameHost
        }

        It "DATASTORE_TYPE defaults to 'kubernetes' when unset" {
            InModuleScope $script:moduleName {
                Mock Get-IsContainerdRunning { $true }
                Mock Get-IsDSRSupported { $false }
                $subs = Build-CNIConfigSubstitutions -BaseDir "C:\CalicoWindows"
                $subs.DATASTORE_TYPE | Should -Be "kubernetes"
            }
        }

        It "IPAM_TYPE defaults to 'calico-ipam' when unset" {
            InModuleScope $script:moduleName {
                Mock Get-IsContainerdRunning { $true }
                Mock Get-IsDSRSupported { $false }
                $subs = Build-CNIConfigSubstitutions -BaseDir "C:\CalicoWindows"
                $subs.IPAM_TYPE | Should -Be "calico-ipam"
            }
        }

        It "NODENAME_FILE defaults to host-visible C:\CalicoWindows\nodename" {
            InModuleScope $script:moduleName {
                Mock Get-IsContainerdRunning { $true }
                Mock Get-IsDSRSupported { $false }
                # BaseDir is the in-container path; NODENAME_FILE must be
                # the host-visible path because the CNI plugin runs in the
                # host namespace, not the container's.
                $subs = Build-CNIConfigSubstitutions -BaseDir "C:\hpc\sandbox\CalicoWindows"
                $subs.NODENAME_FILE | Should -Be "C:\\CalicoWindows\\nodename"
            }
        }

        It "NODENAME_FILE respects CALICO_NODENAME_FILE_HOST_PATH override" {
            $env:CALICO_NODENAME_FILE_HOST_PATH = "D:\custom\nodename"
            InModuleScope $script:moduleName {
                Mock Get-IsContainerdRunning { $true }
                Mock Get-IsDSRSupported { $false }
                $subs = Build-CNIConfigSubstitutions -BaseDir "C:\CalicoWindows"
                $subs.NODENAME_FILE | Should -Be "D:\\custom\\nodename"
            }
        }
    }

    Context "explicit env vars override defaults" {

        It "uses CALICO_DATASTORE_TYPE when set" {
            $env:CALICO_DATASTORE_TYPE = "etcdv3"
            InModuleScope $script:moduleName {
                Mock Get-IsContainerdRunning { $true }
                Mock Get-IsDSRSupported { $false }
                $subs = Build-CNIConfigSubstitutions -BaseDir "C:\CalicoWindows"
                $subs.DATASTORE_TYPE | Should -Be "etcdv3"
            }
            $env:CALICO_DATASTORE_TYPE = "kubernetes"  # restore from BeforeAll
        }

        It "uses CNI_IPAM_TYPE when set" {
            $env:CNI_IPAM_TYPE = "host-local"
            InModuleScope $script:moduleName {
                Mock Get-IsContainerdRunning { $true }
                Mock Get-IsDSRSupported { $false }
                $subs = Build-CNIConfigSubstitutions -BaseDir "C:\CalicoWindows"
                $subs.IPAM_TYPE | Should -Be "host-local"
            }
            $env:CNI_IPAM_TYPE = "calico-ipam"
        }
    }
}

Describe "Render-CNIConfigTemplate" {

    BeforeEach {
        $script:tmpTemplate = New-TemporaryFile
        @'
{
  "type": "calico",
  "mode": "__MODE__",
  "windows_loopback_DSR": __DSR_SUPPORT__,
  "K8S_SERVICE_CIDR": "__K8S_SERVICE_CIDR__"
}
'@ | Set-Content $script:tmpTemplate
    }

    AfterEach { Remove-Item $script:tmpTemplate -Force -ErrorAction SilentlyContinue }

    It "substitutes every key from the hashtable" {
        $subs = @{
            MODE             = "vxlan"
            DSR_SUPPORT      = "false"
            K8S_SERVICE_CIDR = "10.96.0.0/12"
        }
        $rendered = Render-CNIConfigTemplate -TemplatePath $script:tmpTemplate -Subs $subs
        $rendered = $rendered -join "`n"
        $rendered | Should -Match '"mode": "vxlan"'
        $rendered | Should -Match '"windows_loopback_DSR": false'
        $rendered | Should -Match '"K8S_SERVICE_CIDR": "10.96.0.0/12"'
    }

    It "produces valid JSON after substitution of the real cni.conf.template" {
        $realTemplate = "$PSScriptRoot/../CalicoWindows/cni.conf.template"
        $subs = @{
            NODENAME_FILE     = "C:\\CalicoWindows\\nodename"
            KUBECONFIG        = "C:\\CalicoWindows\\calico-kube-config"
            K8S_SERVICE_CIDR  = "10.96.0.0/12"
            DNS_NAME_SERVERS  = '"10.96.0.10"'
            DATASTORE_TYPE    = "kubernetes"
            DSR_SUPPORT       = "false"
            ETCD_ENDPOINTS    = ""
            ETCD_KEY_FILE     = ""
            ETCD_CERT_FILE    = ""
            ETCD_CA_CERT_FILE = ""
            IPAM_TYPE         = "calico-ipam"
            MODE              = "windows-bgp"
            VNI               = "4096"
            MAC_PREFIX        = "0E-2A"
            ROUTE_TYPE        = "SDNROUTE"
        }
        $rendered = Render-CNIConfigTemplate -TemplatePath $realTemplate -Subs $subs
        $rendered = $rendered -join "`n"
        # No leftover __PLACEHOLDER__ tokens.
        $rendered | Should -Not -Match '__[A-Z_]+__'
        # Parses as JSON.
        { $rendered | ConvertFrom-Json } | Should -Not -Throw
        $obj = $rendered | ConvertFrom-Json
        $obj.windows_loopback_DSR | Should -Be $false
    }

    It "handles a value that itself contains a substring matching another placeholder" {
        # Robustness: replacing __MODE__ with a value containing "__VNI__"
        # must still cause __VNI__ to be substituted afterwards.
        $tmp = New-TemporaryFile
        '__MODE__-x-__VNI__' | Set-Content $tmp
        $rendered = Render-CNIConfigTemplate -TemplatePath $tmp -Subs @{ MODE = "abc"; VNI = "42" }
        ($rendered -join "") | Should -Be "abc-x-42"
        Remove-Item $tmp -Force
    }
}

# ---------------------------------------------------------------------
# Test-HnsMgmtIpHookMarker — pure decision function for whether the
# hns-ipv6-hook needs to be re-injected. The bug this catches: the
# previous in-line check in node-service.ps1 only looked at file
# mtime > boot time, not at WHICH desired ManagementIPv6 the marker
# was written for. Failure mode in the field: hook configured with
# the temporary RandomizeIdentifiers-era IPv6 at first boot, NIC then
# rotates to the stable EUI-64-derived address before the next
# calico-node container restart. Marker check returns 'skip', hook
# keeps filtering for the temporary address that is no longer bound,
# GetAdaptersAddresses returns empty → HNS dual-stack create fails
# with HCN_E_ADAPTER_NOT_FOUND.
# ---------------------------------------------------------------------
Describe "Test-HnsMgmtIpHookMarker" {

    BeforeEach {
        $script:tmpDir = Join-Path ([System.IO.Path]::GetTempPath()) ("calico-hook-marker-" + [guid]::NewGuid().Guid)
        New-Item -ItemType Directory -Path $tmpDir | Out-Null
        $script:markerPath = Join-Path $tmpDir 'injected.flag'
    }

    AfterEach {
        Remove-Item -Recurse -Force $script:tmpDir -ErrorAction SilentlyContinue
    }

    It "returns 'inject-fresh' when no marker exists" {
        Test-HnsMgmtIpHookMarker -MarkerPath $markerPath `
            -BootTime (Get-Date).AddMinutes(-10) -HaveCalicoNetwork $true `
            -DesiredV4 "10.2.0.180" -DesiredV6 "fd5a:8000:1::1" |
            Should -Be 'inject-fresh'
    }

    It "returns 'reinject-no-bridge' when marker exists but no Calico HNS network" {
        # Reproduces the field bug: a prior pod injected the hook with
        # stale addresses, container died before the bridge came up.
        # Subsequent pod must re-inject (no working bridge for
        # Restart-Service hns to destroy).
        Set-Content -Path $markerPath -Value "10.2.0.180`tfd5a:8000:1::OLD" -Force -Encoding ASCII
        Test-HnsMgmtIpHookMarker -MarkerPath $markerPath `
            -BootTime (Get-Date).AddMinutes(-10) -HaveCalicoNetwork $false `
            -DesiredV4 "10.2.0.180" -DesiredV6 "fd5a:8000:1::1" |
            Should -Be 'reinject-no-bridge'
    }

    It "returns 'reinject-mismatch' when marker records a different desired pair" {
        # The field-observed scenario: marker written when desired was
        # the stale randomized address; current desired is the stable
        # EUI-64 address. Hook would otherwise keep filtering for the
        # stale value.
        Set-Content -Path $markerPath -Value "10.2.0.180`tfd5a:8000:1:0:cf95:b85d:32e:531f" -Force -Encoding ASCII
        Test-HnsMgmtIpHookMarker -MarkerPath $markerPath `
            -BootTime (Get-Date).AddMinutes(-10) -HaveCalicoNetwork $true `
            -DesiredV4 "10.2.0.180" -DesiredV6 "fd5a:8000:1:0:5054:ff:fe01:2345" |
            Should -Be 'reinject-mismatch'
    }

    It "returns 'reinject-mismatch' when marker IPv4 differs but IPv6 matches" {
        Set-Content -Path $markerPath -Value "10.2.0.99`tfd5a:8000:1::1" -Force -Encoding ASCII
        Test-HnsMgmtIpHookMarker -MarkerPath $markerPath `
            -BootTime (Get-Date).AddMinutes(-10) -HaveCalicoNetwork $true `
            -DesiredV4 "10.2.0.180" -DesiredV6 "fd5a:8000:1::1" |
            Should -Be 'reinject-mismatch'
    }

    It "returns 'reinject-stale' when marker predates last boot" {
        Set-Content -Path $markerPath -Value "10.2.0.180`tfd5a:8000:1::1" -Force -Encoding ASCII
        # Marker file mtime is "now". Set boot time AFTER that.
        $futureBootTime = (Get-Item $markerPath).LastWriteTime.AddMinutes(5)
        Test-HnsMgmtIpHookMarker -MarkerPath $markerPath `
            -BootTime $futureBootTime -HaveCalicoNetwork $true `
            -DesiredV4 "10.2.0.180" -DesiredV6 "fd5a:8000:1::1" |
            Should -Be 'reinject-stale'
    }

    It "returns 'reinject-stale' when no boot time is supplied" {
        Set-Content -Path $markerPath -Value "10.2.0.180`tfd5a:8000:1::1" -Force -Encoding ASCII
        Test-HnsMgmtIpHookMarker -MarkerPath $markerPath `
            -HaveCalicoNetwork $true `
            -DesiredV4 "10.2.0.180" -DesiredV6 "fd5a:8000:1::1" |
            Should -Be 'reinject-stale'
    }

    It "returns 'skip' when marker matches and Calico bridge exists post-boot" {
        # The happy path that the marker check is supposed to protect:
        # don't restart hns because doing so would destroy the working
        # Calico bridge and cause kubelet liveness to oscillate.
        Set-Content -Path $markerPath -Value "10.2.0.180`tfd5a:8000:1::1" -Force -Encoding ASCII
        Test-HnsMgmtIpHookMarker -MarkerPath $markerPath `
            -BootTime (Get-Date).AddHours(-1) -HaveCalicoNetwork $true `
            -DesiredV4 "10.2.0.180" -DesiredV6 "fd5a:8000:1::1" |
            Should -Be 'skip'
    }

    It "returns 'skip' for IPv4-only desired pair when the marker records the same" {
        # CALICO_DESIRED_HNS_MGMT_IPV6 unset — marker records IPv4 only.
        Set-Content -Path $markerPath -Value "10.2.0.180`t" -Force -Encoding ASCII
        Test-HnsMgmtIpHookMarker -MarkerPath $markerPath `
            -BootTime (Get-Date).AddHours(-1) -HaveCalicoNetwork $true `
            -DesiredV4 "10.2.0.180" -DesiredV6 "" |
            Should -Be 'skip'
    }

    It "treats trailing CRLF in the marker file as equivalent to no terminator" {
        # Set-Content -Encoding ASCII on Windows produces CRLF. The
        # comparison must tolerate that without re-injecting on every
        # restart (would defeat the whole purpose of the marker).
        Set-Content -Path $markerPath -Value "10.2.0.180`tfd5a:8000:1::1" -Force -Encoding ASCII
        # Sanity check the file content has trailing newline byte(s).
        (Get-Content $markerPath -Raw) | Should -Match "`n$"
        Test-HnsMgmtIpHookMarker -MarkerPath $markerPath `
            -BootTime (Get-Date).AddHours(-1) -HaveCalicoNetwork $true `
            -DesiredV4 "10.2.0.180" -DesiredV6 "fd5a:8000:1::1" |
            Should -Be 'skip'
    }
}

Describe "Get-HnsMgmtIpHookMarkerLine" {

    It "uses tab as the separator between v4 and v6" {
        # The reader (Test-HnsMgmtIpHookMarker) expects exactly this
        # format. Both functions need to agree.
        Get-HnsMgmtIpHookMarkerLine -DesiredV4 "10.2.0.180" -DesiredV6 "fd5a:8000:1::1" |
            Should -Be "10.2.0.180`tfd5a:8000:1::1"
    }

    It "handles empty desired-v6 by emitting a trailing tab" {
        Get-HnsMgmtIpHookMarkerLine -DesiredV4 "10.2.0.180" -DesiredV6 "" |
            Should -Be "10.2.0.180`t"
    }

    It "handles empty desired-v4 by emitting a leading tab" {
        # Would happen if IP_AUTODETECTION_METHOD is unset but
        # CALICO_DESIRED_HNS_MGMT_IPV6 is — the v4 lookup returns
        # empty.
        Get-HnsMgmtIpHookMarkerLine -DesiredV4 "" -DesiredV6 "fd5a:8000:1::1" |
            Should -Be "`tfd5a:8000:1::1"
    }
}
