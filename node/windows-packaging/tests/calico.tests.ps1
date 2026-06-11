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

    Context "with CALICO_DSR_DISABLE unset" {
        BeforeEach { $env:CALICO_DSR_DISABLE = $null }

        It "returns false even when Get-IsDSRSupported is true" {
            InModuleScope $script:moduleName {
                Mock Get-IsDSRSupported { $true }
                Get-DSRSupport | Should -Be "false"
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

        It "returns true (DSR enabled) for CALICO_DSR_DISABLE='false' on a DSR-capable OS" {
            $env:CALICO_DSR_DISABLE = "false"
            InModuleScope $script:moduleName {
                Mock Get-IsDSRSupported { $true }
                Get-DSRSupport | Should -Be "true"
            }
            $env:CALICO_DSR_DISABLE = $null
        }

        It "returns false for CALICO_DSR_DISABLE='false' when the OS does not support DSR" {
            $env:CALICO_DSR_DISABLE = "false"
            InModuleScope $script:moduleName {
                Mock Get-IsDSRSupported { $false }
                Get-DSRSupport | Should -Be "false"
            }
            $env:CALICO_DSR_DISABLE = $null
        }

        It "returns false for any non-false value" {
            $env:CALICO_DSR_DISABLE = "1"
            InModuleScope $script:moduleName {
                Mock Get-IsDSRSupported { $true }
                Get-DSRSupport | Should -Be "false"
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

    It "keeps DSR_SUPPORT false by default" {
        $env:CALICO_DSR_DISABLE = $null
        InModuleScope $script:moduleName {
            Mock Get-IsContainerdRunning { $true }
            Mock Get-IsDSRSupported { $true }
            $subs = Build-CNIConfigSubstitutions -BaseDir "C:\CalicoWindows"
            $subs.DSR_SUPPORT | Should -Be "false"
        }
    }

    It "passes through CALICO_DSR_DISABLE=true to the DSR_SUPPORT key" {
        $env:CALICO_DSR_DISABLE = "true"
        InModuleScope $script:moduleName {
            Mock Get-IsContainerdRunning { $true }
            Mock Get-IsDSRSupported { $true }
            $subs = Build-CNIConfigSubstitutions -BaseDir "C:\CalicoWindows"
            $subs.DSR_SUPPORT | Should -Be "false"
        }
        $env:CALICO_DSR_DISABLE = $null
    }

    It "passes through CALICO_DSR_DISABLE=false to the DSR_SUPPORT key when the OS supports DSR" {
        $env:CALICO_DSR_DISABLE = "false"
        InModuleScope $script:moduleName {
            Mock Get-IsContainerdRunning { $true }
            Mock Get-IsDSRSupported { $true }
            $subs = Build-CNIConfigSubstitutions -BaseDir "C:\CalicoWindows"
            $subs.DSR_SUPPORT | Should -Be "true"
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

Describe "Resolve-CalicoConfdDirectory" {
    BeforeEach {
        $script:savedSandbox = $env:CONTAINER_SANDBOX_MOUNT_POINT
        $script:savedConfdHostPath = $env:CALICO_CONFD_HOST_PATH
        $env:CONTAINER_SANDBOX_MOUNT_POINT = $null
        $env:CALICO_CONFD_HOST_PATH = $null
    }

    AfterEach {
        $env:CONTAINER_SANDBOX_MOUNT_POINT = $script:savedSandbox
        $env:CALICO_CONFD_HOST_PATH = $script:savedConfdHostPath
    }

    It "uses the script root outside HostProcess mode" {
        Resolve-CalicoConfdDirectory -ScriptRoot 'C:\CalicoWindows\confd' | Should -Be 'C:\CalicoWindows\confd'
    }

    It "uses the host confd directory inside HostProcess mode" {
        $env:CONTAINER_SANDBOX_MOUNT_POINT = 'C:\var\lib\kubelet\pods\poduid\volumes'
        Resolve-CalicoConfdDirectory -ScriptRoot 'C:\var\lib\kubelet\pods\poduid\volume-subpaths\CalicoWindows\confd' | Should -Be 'C:\CalicoWindows\confd'
    }

    It "uses the host confd directory when HostProcess runs from the hpc sandbox without sandbox env" {
        Resolve-CalicoConfdDirectory -ScriptRoot 'C:\hpc\CalicoWindows\confd' | Should -Be 'C:\CalicoWindows\confd'
    }

    It "uses the host confd directory when the hpc sandbox path has mixed separators" {
        Resolve-CalicoConfdDirectory -ScriptRoot 'C:\hpc/CalicoWindows/confd' | Should -Be 'C:\CalicoWindows\confd'
    }

    It "respects CALICO_CONFD_HOST_PATH override" {
        $env:CONTAINER_SANDBOX_MOUNT_POINT = 'C:\sandbox'
        $env:CALICO_CONFD_HOST_PATH = 'D:\calico\confd'
        Resolve-CalicoConfdDirectory -ScriptRoot 'C:\sandbox\CalicoWindows\confd' | Should -Be 'D:\calico\confd'
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

Describe "Write-CNIConfig" {

    BeforeEach {
        $script:tmpDir = Join-Path ([System.IO.Path]::GetTempPath()) ("calico-cni-conf-" + [guid]::NewGuid().Guid)
        $script:baseDir = Join-Path $tmpDir "base"
        $script:confDir = Join-Path $tmpDir "net.d"
        New-Item -ItemType Directory -Path $baseDir -Force | Out-Null
        New-Item -ItemType Directory -Path $confDir -Force | Out-Null

        $script:savedCNIConfDir = $env:CNI_CONF_DIR
        $script:savedCNIConfFilename = $env:CNI_CONF_FILENAME
        $env:CNI_CONF_DIR = $confDir
        $env:CNI_CONF_FILENAME = "10-calico.conf"
    }

    AfterEach {
        $env:CNI_CONF_DIR = $script:savedCNIConfDir
        $env:CNI_CONF_FILENAME = $script:savedCNIConfFilename
        Remove-Item -Recurse -Force $script:tmpDir -ErrorAction SilentlyContinue
    }

    It "writes a non-empty CNI config atomically" {
        $template = Join-Path $baseDir "cni.conf.template"
        '{"cniVersion":"0.4.0","name":"Calico","type":"calico"}' | Set-Content -Path $template -Encoding ASCII

        InModuleScope $script:moduleName -Parameters @{ BaseDir = $baseDir } {
            Mock Build-CNIConfigSubstitutions { @{} }
            Write-CNIConfig -BaseDir $BaseDir
        }

        $outFile = Join-Path $confDir "10-calico.conf"
        (Get-Item $outFile).Length | Should -BeGreaterThan 0
        { Get-Content -Raw $outFile | ConvertFrom-Json } | Should -Not -Throw
        Get-ChildItem $confDir -Filter "*.tmp" | Should -BeNullOrEmpty
    }

    It "does not replace an existing CNI config when rendering fails" {
        $outFile = Join-Path $confDir "10-calico.conf"
        '{"cniVersion":"0.4.0","name":"old","type":"calico"}' | Set-Content -Path $outFile -Encoding ASCII
        $old = Get-Content -Raw $outFile

        InModuleScope $script:moduleName -Parameters @{ BaseDir = $baseDir } {
            Mock Build-CNIConfigSubstitutions { @{} }
            Mock Render-CNIConfigTemplate { throw "render failed" }
            { Write-CNIConfig -BaseDir $BaseDir } | Should -Throw
        }

        Get-Content -Raw $outFile | Should -Be $old
        (Get-Item $outFile).Length | Should -BeGreaterThan 0
    }

    It "rejects empty rendered CNI config" {
        $template = Join-Path $baseDir "cni.conf.template"
        "" | Set-Content -Path $template -Encoding ASCII

        InModuleScope $script:moduleName -Parameters @{ BaseDir = $baseDir } {
            Mock Build-CNIConfigSubstitutions { @{} }
            { Write-CNIConfig -BaseDir $BaseDir } | Should -Throw
        }
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

    It "returns 'reinject-no-bridge' for a legacy marker when no Calico HNS network exists" {
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

    It "returns 'skip' for a current v2 marker even when no Calico HNS network exists" {
        Set-Content -Path $markerPath -Value "v2`t1234`t10.2.0.180`tfd5a:8000:1::1" -Force -Encoding ASCII
        Test-HnsMgmtIpHookMarker -MarkerPath $markerPath `
            -BootTime (Get-Date).AddMinutes(-10) -HaveCalicoNetwork $false `
            -DesiredV4 "10.2.0.180" -DesiredV6 "fd5a:8000:1::1" `
            -CurrentHnsPid 1234 |
            Should -Be 'skip'
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

    It "returns 'skip' for a v2 marker matching the current HNS PID and desired pair" {
        Set-Content -Path $markerPath -Value "v2`t1234`t10.2.0.180`tfd5a:8000:1::1" -Force -Encoding ASCII
        Test-HnsMgmtIpHookMarker -MarkerPath $markerPath `
            -BootTime (Get-Date).AddHours(-1) -HaveCalicoNetwork $true `
            -DesiredV4 "10.2.0.180" -DesiredV6 "fd5a:8000:1::1" -CurrentHnsPid 1234 |
            Should -Be 'skip'
    }

    It "returns 'reinject-pid' for a v2 marker written for a previous HNS PID" {
        Set-Content -Path $markerPath -Value "v2`t1234`t10.2.0.180`tfd5a:8000:1::1" -Force -Encoding ASCII
        Test-HnsMgmtIpHookMarker -MarkerPath $markerPath `
            -BootTime (Get-Date).AddHours(-1) -HaveCalicoNetwork $true `
            -DesiredV4 "10.2.0.180" -DesiredV6 "fd5a:8000:1::1" -CurrentHnsPid 5678 |
            Should -Be 'reinject-pid'
    }

    It "treats legacy address-only markers as stale when the caller has the current HNS PID" {
        Set-Content -Path $markerPath -Value "10.2.0.180`tfd5a:8000:1::1" -Force -Encoding ASCII
        Test-HnsMgmtIpHookMarker -MarkerPath $markerPath `
            -BootTime (Get-Date).AddHours(-1) -HaveCalicoNetwork $true `
            -DesiredV4 "10.2.0.180" -DesiredV6 "fd5a:8000:1::1" -CurrentHnsPid 1234 |
            Should -Be 'reinject-stale'
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

    It "emits a v2 marker when an HNS PID is supplied" {
        Get-HnsMgmtIpHookMarkerLine -DesiredV4 "10.2.0.180" -DesiredV6 "fd5a:8000:1::1" -HnsPid 1234 |
            Should -Be "v2`t1234`t10.2.0.180`tfd5a:8000:1::1"
    }
}

Describe "Test-CalicoStartupCanSkip" {
    BeforeEach {
        $env:FELIX_IPV6SUPPORT = $null
        $env:CALICO_ALLOW_DUALSTACK_STARTUP_SKIP = $null
    }

    AfterEach {
        $env:FELIX_IPV6SUPPORT = $null
        $env:CALICO_ALLOW_DUALSTACK_STARTUP_SKIP = $null
    }

    It "returns false for a dual-stack bridge with matching ManagementIP unless explicitly overridden" {
        $net = [pscustomobject]@{
            Name           = 'Calico'
            Type           = 'L2Bridge'
            ManagementIP   = '10.2.0.3'
            ManagementIPv6 = '2001:5a8:4295:b600:1ac0:4dff:fe89:5194'
            Subnets        = @(
                [pscustomobject]@{ AddressPrefix = '10.3.48.192/26'; GatewayAddress = '10.3.48.193' },
                [pscustomobject]@{ AddressPrefix = '2001:5a8:4295:b601:430d:9038:5fa1:d000/122'; GatewayAddress = '2001:5a8:4295:b601:430d:9038:5fa1:d001' }
            )
        }

        Test-CalicoStartupCanSkip -ExistingCalicoNetwork $net -ExpectedManagementIP '10.2.0.3' -IPv6SupportEnabled $true |
            Should -BeFalse
    }

    It "allows dual-stack startup skip only with the explicit override" {
        $net = [pscustomobject]@{
            Name         = 'Calico'
            Type         = 'L2Bridge'
            ManagementIP = '10.2.0.3'
        }

        Test-CalicoStartupCanSkip -ExistingCalicoNetwork $net -ExpectedManagementIP '10.2.0.3' -IPv6SupportEnabled $true -AllowDualStackStartupSkip $true |
            Should -BeTrue
    }

    It "returns true for an IPv4-only existing Calico L2Bridge with matching ManagementIP" {
        $net = [pscustomobject]@{
            Name         = 'Calico'
            Type         = 'L2Bridge'
            ManagementIP = '10.2.0.3'
        }

        Test-CalicoStartupCanSkip -ExistingCalicoNetwork $net -ExpectedManagementIP '10.2.0.3' -IPv6SupportEnabled $false |
            Should -BeTrue
    }

    It "returns false when the existing bridge has the wrong ManagementIP" {
        $net = [pscustomobject]@{
            Name           = 'Calico'
            Type           = 'L2Bridge'
            ManagementIP   = '10.2.0.99'
            ManagementIPv6 = 'fd5a:8000:1:0:1ac0:4dff:fe89:5194'
        }

        Test-CalicoStartupCanSkip -ExistingCalicoNetwork $net -ExpectedManagementIP '10.2.0.3' |
            Should -BeFalse
    }

    It "returns false when there is no existing Calico bridge" {
        Test-CalicoStartupCanSkip -ExistingCalicoNetwork $null -ExpectedManagementIP '10.2.0.3' |
            Should -BeFalse
    }

    It "returns false for a non-L2Bridge network with the same name and ManagementIP" {
        $net = [pscustomobject]@{
            Name         = 'Calico'
            Type         = 'Overlay'
            ManagementIP = '10.2.0.3'
        }

        Test-CalicoStartupCanSkip -ExistingCalicoNetwork $net -ExpectedManagementIP '10.2.0.3' |
            Should -BeFalse
    }
}

Describe "Invoke-HnsHookServiceRestart" {

    It "restarts hns successfully without fallback" {
        InModuleScope $script:moduleName {
            Mock Restart-HnsService { }
            Mock Stop-RemoteAccessService { }
            Mock Stop-HnsService { }
            Mock Start-HnsService { }
            Mock Get-HnsService { [pscustomobject]@{ Status = 'Running' } }
            Mock Start-Sleep { }

            { Invoke-HnsHookServiceRestart -Attempts 1 -RunningTimeoutSeconds 1 } | Should -Not -Throw

            Assert-MockCalled Restart-HnsService -Times 1 -Exactly
            Assert-MockCalled Stop-RemoteAccessService -Times 0 -Exactly
            Assert-MockCalled Stop-HnsService -Times 0 -Exactly
            Assert-MockCalled Start-HnsService -Times 0 -Exactly
        }
    }

    It "falls back through RemoteAccess when Restart-Service hns fails" {
        InModuleScope $script:moduleName {
            Mock Restart-HnsService { throw "stop failed" }
            Mock Stop-RemoteAccessService { }
            Mock Stop-HnsService { }
            Mock Start-HnsService { }
            Mock Get-HnsService { [pscustomobject]@{ Status = 'Running' } }
            Mock Start-Sleep { }

            { Invoke-HnsHookServiceRestart -Attempts 1 -RunningTimeoutSeconds 1 } | Should -Not -Throw

            Assert-MockCalled Restart-HnsService -Times 1 -Exactly
            Assert-MockCalled Stop-RemoteAccessService -Times 1 -Exactly
            Assert-MockCalled Stop-HnsService -Times 1 -Exactly
            Assert-MockCalled Start-HnsService -Times 1 -Exactly
        }
    }

    It "retries transient fallback failures instead of crashlooping immediately" {
        InModuleScope $script:moduleName {
            $script:stopHnsCalls = 0
            Mock Restart-HnsService { throw "restart failed" }
            Mock Stop-RemoteAccessService { }
            Mock Stop-HnsService {
                $script:stopHnsCalls++
                if ($script:stopHnsCalls -eq 1) { throw "hns stop still pending" }
            }
            Mock Start-HnsService { }
            Mock Get-HnsService { [pscustomobject]@{ Status = 'Running' } }
            Mock Start-Sleep { }

            { Invoke-HnsHookServiceRestart -Attempts 2 -DelaySeconds 0 -RunningTimeoutSeconds 1 } | Should -Not -Throw

            Assert-MockCalled Restart-HnsService -Times 2 -Exactly
            Assert-MockCalled Start-HnsService -Times 1 -Exactly
        }
    }

    It "throws after bounded attempts when hns cannot be restarted" {
        InModuleScope $script:moduleName {
            Mock Restart-HnsService { throw "restart failed" }
            Mock Stop-RemoteAccessService { }
            Mock Stop-HnsService { throw "stop failed" }
            Mock Start-HnsService { }
            Mock Get-HnsService { [pscustomobject]@{ Status = 'Stopped' } }
            Mock Start-Sleep { }

            { Invoke-HnsHookServiceRestart -Attempts 2 -DelaySeconds 0 -RunningTimeoutSeconds 1 } |
                Should -Throw -ExpectedMessage '*hns did not restart after 2 attempts*'
        }
    }
}

Describe "Test-CalicoHnsNetworkNeedsStartupRecreate" {
    It "returns true for management NIC drift to a secondary adapter" {
        $net = [pscustomobject]@{
            Name           = 'Calico'
            Type           = 'L2Bridge'
            ManagementIP   = '10.2.0.24'
            ManagementIPv6 = 'fd5a:8000:1:0:a2ce:c8ff:fea2:53c2'
        }

        Test-CalicoHnsNetworkNeedsStartupRecreate `
            -ExistingCalicoNetwork $net `
            -ExpectedManagementIP '10.2.0.3' `
            -ExpectedManagementIPv6 'fd5a:8000:1:0:1ac0:4dff:fe89:5194' `
            -IPv6SupportEnabled $true |
            Should -BeTrue
    }

    It "returns false when the existing bridge already has the desired dual-stack management addresses" {
        $net = [pscustomobject]@{
            Name           = 'Calico'
            Type           = 'L2Bridge'
            ManagementIP   = '10.2.0.3'
            ManagementIPv6 = 'fd5a:8000:1:0:1ac0:4dff:fe89:5194'
        }

        Test-CalicoHnsNetworkNeedsStartupRecreate `
            -ExistingCalicoNetwork $net `
            -ExpectedManagementIP '10.2.0.3' `
            -ExpectedManagementIPv6 'fd5a:8000:1:0:1ac0:4dff:fe89:5194' `
            -IPv6SupportEnabled $true |
            Should -BeFalse
    }

    It "ignores IPv6 drift when IPv6 support is disabled" {
        $net = [pscustomobject]@{
            Name           = 'Calico'
            Type           = 'L2Bridge'
            ManagementIP   = '10.2.0.3'
            ManagementIPv6 = 'fd5a:8000:1:0:a2ce:c8ff:fea2:53c2'
        }

        Test-CalicoHnsNetworkNeedsStartupRecreate `
            -ExistingCalicoNetwork $net `
            -ExpectedManagementIP '10.2.0.3' `
            -ExpectedManagementIPv6 'fd5a:8000:1:0:1ac0:4dff:fe89:5194' `
            -IPv6SupportEnabled $false |
            Should -BeFalse
    }
}

# ---------------------------------------------------------------------
# Test-HnsManagementInterfaceAlias / Resolve-DesiredHnsManagement{IPv4,IPv6}
# Filter logic isolated from node-service.ps1 so we can validate every
# alias case the lifecycle produces:
#   * pre-vSwitch: bare "Ethernet" / "Ethernet 2" / etc.
#   * post-vSwitch: "vEthernet (Ethernet)" / "vEthernet (Ethernet 3)"
#   * existing or stale Calico vSwitch: "vEthernet (Calico)" /
#     "vEthernet (Calico_ep)" / "vEthernet (Calico-...)"
# vEthernet (anything-else) and other adapters must not match.
# ---------------------------------------------------------------------
Describe "Test-HnsManagementInterfaceAlias" {
    BeforeEach {
        $script:savedAddressPreference = $env:CALICO_HNS_MGMT_ADDRESS_INTERFACE_PREFERENCE
        $env:CALICO_HNS_MGMT_ADDRESS_INTERFACE_PREFERENCE = $null
    }

    AfterEach {
        $env:CALICO_HNS_MGMT_ADDRESS_INTERFACE_PREFERENCE = $script:savedAddressPreference
    }

    It "matches plain 'Ethernet'" {
        Test-HnsManagementInterfaceAlias 'Ethernet' | Should -BeTrue
    }
    It "matches 'Ethernet 2', 'Ethernet 3', etc." {
        Test-HnsManagementInterfaceAlias 'Ethernet 2' | Should -BeTrue
        Test-HnsManagementInterfaceAlias 'Ethernet 27' | Should -BeTrue
    }
    It "matches 'vEthernet (Ethernet)'" {
        Test-HnsManagementInterfaceAlias 'vEthernet (Ethernet)' | Should -BeTrue
    }
    It "matches 'vEthernet (Ethernet 3)'" {
        Test-HnsManagementInterfaceAlias 'vEthernet (Ethernet 3)' | Should -BeTrue
    }
    It "matches 'vEthernet (Calico)' (existing Calico vSwitch)" {
        # Lifecycle case: an existing Calico L2Bridge already has a host
        # vNIC by this name; the management address can sit on it.
        Test-HnsManagementInterfaceAlias 'vEthernet (Calico)' | Should -BeTrue
    }
    It "matches 'vEthernet (Calico_ep)' too" {
        # Bridge endpoint vNIC of an active Calico install; name shape
        # depends on Calico version. Conservative wildcard accepts both.
        Test-HnsManagementInterfaceAlias 'vEthernet (Calico_ep)' | Should -BeTrue
    }
    It "ranks plain Ethernet ahead of Calico and synthetic Ethernet vNICs" {
        Get-HnsManagementInterfaceRank 'Ethernet' | Should -BeLessThan (Get-HnsManagementInterfaceRank 'vEthernet (Calico)')
        Get-HnsManagementInterfaceRank 'Ethernet 3' | Should -BeLessThan (Get-HnsManagementInterfaceRank 'vEthernet (Ethernet 3)')
        Get-HnsManagementInterfaceRank 'vEthernet (Ethernet)' | Should -BeLessThan (Get-HnsManagementInterfaceRank 'vEthernet (Calico)')
        Get-HnsManagementInterfaceRank 'Ethernet' | Should -BeLessThan (Get-HnsManagementInterfaceRank 'vEthernet (Ethernet 2)')
    }
    It "ranks the moved management address on vEthernet (Ethernet) ahead of secondary NICs" {
        Get-HnsManagementAddressRank 'vEthernet (Ethernet)' | Should -BeLessThan (Get-HnsManagementAddressRank 'Ethernet 2')
        Get-HnsManagementAddressRank 'Ethernet' | Should -BeLessThan (Get-HnsManagementAddressRank 'vEthernet (Ethernet 2)')
        Get-HnsManagementAddressRank 'vEthernet (Ethernet 3)' | Should -BeLessThan (Get-HnsManagementAddressRank 'Ethernet 3')
        Get-HnsManagementAddressRank 'vEthernet (Ethernet)' | Should -BeLessThan (Get-HnsManagementAddressRank 'vEthernet (Calico)')
    }
    It "allows the management address preference order to be configured" {
        $env:CALICO_HNS_MGMT_ADDRESS_INTERFACE_PREFERENCE = 'Ethernet 2,vEthernet (Ethernet),Ethernet*'
        Get-HnsManagementAddressRank 'Ethernet 2' | Should -BeLessThan (Get-HnsManagementAddressRank 'vEthernet (Ethernet)')
    }
    It "rejects 'Loopback Pseudo-Interface 1'" {
        Test-HnsManagementInterfaceAlias 'Loopback Pseudo-Interface 1' | Should -BeFalse
    }
    It "rejects 'vEthernet (External)' (placeholder switch on another NIC)" {
        Test-HnsManagementInterfaceAlias 'vEthernet (External)' | Should -BeFalse
    }
    It "rejects 'Wi-Fi'" {
        Test-HnsManagementInterfaceAlias 'Wi-Fi' | Should -BeFalse
    }
}

Describe "Resolve-DesiredHnsManagementIPv6" {
    BeforeEach {
        $script:savedAddressPreference = $env:CALICO_HNS_MGMT_ADDRESS_INTERFACE_PREFERENCE
        $env:CALICO_HNS_MGMT_ADDRESS_INTERFACE_PREFERENCE = $null
    }

    AfterEach {
        $env:CALICO_HNS_MGMT_ADDRESS_INTERFACE_PREFERENCE = $script:savedAddressPreference
    }

    BeforeEach {
        # Stand-in for Get-NetIPAddress output. Only InterfaceAlias and
        # IPAddress are read by the function under test.
        $script:fakeAddrs = @(
            [pscustomobject]@{ InterfaceAlias = 'Ethernet 3'; IPAddress = 'fd5a:8000:1:0:5054:ff:fe01:2345' },
            [pscustomobject]@{ InterfaceAlias = 'Ethernet 3'; IPAddress = '2001:5a8:4295:b600:5054:ff:fe01:2345' },
            [pscustomobject]@{ InterfaceAlias = 'Ethernet 3'; IPAddress = 'fe80::5054:ff:fe01:2345' },
            [pscustomobject]@{ InterfaceAlias = 'Loopback Pseudo-Interface 1'; IPAddress = '::1' }
        )
    }

    It "picks the prefix-matching ULA when both ULA and GUA are bound" {
        Resolve-DesiredHnsManagementIPv6 -Addresses $fakeAddrs -Prefix 'fd5a:8000:1:0:' |
            Should -Be 'fd5a:8000:1:0:5054:ff:fe01:2345'
    }

    It "picks a GUA when the prefix matches a GUA prefix" {
        Resolve-DesiredHnsManagementIPv6 -Addresses $fakeAddrs -Prefix '2001:5a8:4295:b600:' |
            Should -Be '2001:5a8:4295:b600:5054:ff:fe01:2345'
    }

    It "ignores fe80 link-local even when prefix matches" {
        # Pathological prefix that would match fe80; the function must
        # still skip it because link-local is not a valid Management IP.
        Resolve-DesiredHnsManagementIPv6 -Addresses $fakeAddrs -Prefix 'fe80::' |
            Should -BeNullOrEmpty
    }

    It "returns null when no address matches the requested prefix" {
        Resolve-DesiredHnsManagementIPv6 -Addresses $fakeAddrs -Prefix 'fc00::deadbeef:' |
            Should -BeNullOrEmpty
    }

    It "finds the address on vEthernet (Calico) when the address has migrated to an existing Calico vSwitch" {
        # Reproduces the failure mode: the only IPv6 in the prefix is on
        # vEthernet (Calico), not on any 'Ethernet*' alias. The previous
        # filter missed this and returned null, so no desired pair was
        # derived and the hook was never re-injected.
        $addrs = @(
            [pscustomobject]@{ InterfaceAlias = 'vEthernet (Calico)'; IPAddress = 'fd5a:8000:1:0:9e6b:ff:feab:8438' }
        )
        Resolve-DesiredHnsManagementIPv6 -Addresses $addrs -Prefix 'fd5a:8000:1:0:' |
            Should -Be 'fd5a:8000:1:0:9e6b:ff:feab:8438'
    }

    It "still prefers the management-NIC address over a vEthernet (Calico) address" {
        # Order in the returned list is the order Get-NetIPAddress
        # produces. Real systems list physical/vEthernet (Ethernet*)
        # before vEthernet (Calico*). 'Select -First 1' on a matching
        # list gives the management-NIC bind first when both are present.
        $addrs = @(
            [pscustomobject]@{ InterfaceAlias = 'vEthernet (Ethernet)'; IPAddress = 'fd5a:8000:1:0:aaaa::1' },
            [pscustomobject]@{ InterfaceAlias = 'vEthernet (Calico)';   IPAddress = 'fd5a:8000:1:0:bbbb::2' }
        )
        Resolve-DesiredHnsManagementIPv6 -Addresses $addrs -Prefix 'fd5a:8000:1:0:' |
            Should -Be 'fd5a:8000:1:0:aaaa::1'
    }

    It "prefers the physical management NIC when a temporary vEthernet NIC has a matching ULA" {
        $addrs = @(
            [pscustomobject]@{ InterfaceAlias = 'vEthernet (Ethernet 2)'; IPAddress = 'fd5a:8000:1:0:a2ce:c8ff:fea2:53c2' },
            [pscustomobject]@{ InterfaceAlias = 'Ethernet'; IPAddress = 'fd5a:8000:1:0:1ac0:4dff:fe89:5194' }
        )
        Resolve-DesiredHnsManagementIPv6 -Addresses $addrs -Prefix 'fd5a:8000:1:0:' |
            Should -Be 'fd5a:8000:1:0:1ac0:4dff:fe89:5194'
    }

    It "models a stale secondary-adapter HNS bridge and keeps selecting the management ULA" {
        $addrs = @(
            [pscustomobject]@{ InterfaceAlias = 'Ethernet'; IPAddress = '2001:5a8:4298:3b00:1ac0:4dff:fe89:5194' },
            [pscustomobject]@{ InterfaceAlias = 'Ethernet'; IPAddress = 'fd5a:8000:1:0:1ac0:4dff:fe89:5194' },
            [pscustomobject]@{ InterfaceAlias = 'vEthernet (Calico_ep)'; IPAddress = '2001:5a8:4298:3b01:430d:9038:5fa1:d002' },
            [pscustomobject]@{ InterfaceAlias = 'vEthernet (Ethernet 2)'; IPAddress = '2001:5a8:4298:3b00:a2ce:c8ff:fea2:53c2' },
            [pscustomobject]@{ InterfaceAlias = 'vEthernet (Ethernet 2)'; IPAddress = 'fd5a:8000:1:0:a2ce:c8ff:fea2:53c2' }
        )
        Resolve-DesiredHnsManagementIPv6 -Addresses $addrs -Prefix 'fd5a:8000:1:0:' |
            Should -Be 'fd5a:8000:1:0:1ac0:4dff:fe89:5194'
    }

    It "uses the canonical management vNIC after Hyper-V moves the management ULA" {
        $addrs = @(
            [pscustomobject]@{ InterfaceAlias = 'Ethernet 2'; IPAddress = 'fd5a:8000:1:0:a2ce:c8ff:fea2:53c2' },
            [pscustomobject]@{ InterfaceAlias = 'vEthernet (Ethernet)'; IPAddress = 'fd5a:8000:1:0:1ac0:4dff:fe89:5194' },
            [pscustomobject]@{ InterfaceAlias = 'vEthernet (Calico_ep)'; IPAddress = '2001:5a8:4298:3b01:430d:9038:5fa1:d002' }
        )
        Resolve-DesiredHnsManagementIPv6 -Addresses $addrs -Prefix 'fd5a:8000:1:0:' |
            Should -Be 'fd5a:8000:1:0:1ac0:4dff:fe89:5194'
    }
}

Describe "Resolve-DesiredHnsManagementIPv4" {
    BeforeEach {
        $script:savedAddressPreference = $env:CALICO_HNS_MGMT_ADDRESS_INTERFACE_PREFERENCE
        $env:CALICO_HNS_MGMT_ADDRESS_INTERFACE_PREFERENCE = $null
    }

    AfterEach {
        $env:CALICO_HNS_MGMT_ADDRESS_INTERFACE_PREFERENCE = $script:savedAddressPreference
    }

    It "picks the IPv4 inside the requested CIDR" {
        $addrs = @(
            [pscustomobject]@{ InterfaceAlias = 'Ethernet 3'; IPAddress = '10.2.0.180' },
            [pscustomobject]@{ InterfaceAlias = 'Ethernet 2'; IPAddress = '10.0.2.15' }
        )
        Resolve-DesiredHnsManagementIPv4 -Addresses $addrs -NetworkCIDR '10.2.0.0/24' |
            Should -Be '10.2.0.180'
    }

    It "prefers the physical management NIC when a temporary vEthernet NIC is also in the CIDR" {
        $addrs = @(
            [pscustomobject]@{ InterfaceAlias = 'vEthernet (Ethernet 2)'; IPAddress = '10.2.0.24' },
            [pscustomobject]@{ InterfaceAlias = 'Ethernet'; IPAddress = '10.2.0.3' }
        )
        Resolve-DesiredHnsManagementIPv4 -Addresses $addrs -NetworkCIDR '10.2.0.0/24' |
            Should -Be '10.2.0.3'
    }

    It "models a stale secondary-adapter HNS bridge and keeps selecting the management IPv4" {
        $addrs = @(
            [pscustomobject]@{ InterfaceAlias = 'Ethernet'; IPAddress = '10.2.0.3' },
            [pscustomobject]@{ InterfaceAlias = 'vEthernet (Calico_ep)'; IPAddress = '10.3.48.194' },
            [pscustomobject]@{ InterfaceAlias = 'vEthernet (Ethernet 2)'; IPAddress = '10.2.0.24' }
        )
        Resolve-DesiredHnsManagementIPv4 -Addresses $addrs -NetworkCIDR '10.2.0.0/24' |
            Should -Be '10.2.0.3'
    }

    It "uses the canonical management vNIC after Hyper-V moves the management IPv4" {
        $addrs = @(
            [pscustomobject]@{ InterfaceAlias = 'Ethernet 2'; IPAddress = '10.2.0.24' },
            [pscustomobject]@{ InterfaceAlias = 'vEthernet (Ethernet)'; IPAddress = '10.2.0.3' },
            [pscustomobject]@{ InterfaceAlias = 'vEthernet (Calico_ep)'; IPAddress = '10.3.48.194' }
        )
        Resolve-DesiredHnsManagementIPv4 -Addresses $addrs -NetworkCIDR '10.2.0.0/24' |
            Should -Be '10.2.0.3'
    }

    It "honours a configured management address preference" {
        $env:CALICO_HNS_MGMT_ADDRESS_INTERFACE_PREFERENCE = 'Ethernet 2,vEthernet (Ethernet),Ethernet*'
        $addrs = @(
            [pscustomobject]@{ InterfaceAlias = 'Ethernet 2'; IPAddress = '10.2.0.24' },
            [pscustomobject]@{ InterfaceAlias = 'vEthernet (Ethernet)'; IPAddress = '10.2.0.3' }
        )
        Resolve-DesiredHnsManagementIPv4 -Addresses $addrs -NetworkCIDR '10.2.0.0/24' |
            Should -Be '10.2.0.24'
    }

    It "skips APIPA (169.254.x.y)" {
        $addrs = @(
            [pscustomobject]@{ InterfaceAlias = 'Ethernet'; IPAddress = '169.254.1.1' }
        )
        Resolve-DesiredHnsManagementIPv4 -Addresses $addrs -NetworkCIDR '169.254.0.0/16' |
            Should -BeNullOrEmpty
    }

    It "skips loopback (127.0.0.1)" {
        $addrs = @(
            [pscustomobject]@{ InterfaceAlias = 'Loopback Pseudo-Interface 1'; IPAddress = '127.0.0.1' }
        )
        Resolve-DesiredHnsManagementIPv4 -Addresses $addrs -NetworkCIDR '127.0.0.0/8' |
            Should -BeNullOrEmpty
    }

    It "finds an address on vEthernet (Calico) when management IP has migrated there" {
        $addrs = @(
            [pscustomobject]@{ InterfaceAlias = 'vEthernet (Calico)'; IPAddress = '10.2.0.11' }
        )
        Resolve-DesiredHnsManagementIPv4 -Addresses $addrs -NetworkCIDR '10.2.0.0/24' |
            Should -Be '10.2.0.11'
    }

    It "returns null on a malformed CIDR" {
        $addrs = @(
            [pscustomobject]@{ InterfaceAlias = 'Ethernet'; IPAddress = '10.2.0.180' }
        )
        Resolve-DesiredHnsManagementIPv4 -Addresses $addrs -NetworkCIDR 'not-a-cidr' |
            Should -BeNullOrEmpty
    }
}

Describe "Test-HnsManagementIPAddressMatchesAutodetection" {
    It "accepts a persisted IPv4 management address inside the current CIDR" {
        Test-HnsManagementIPAddressMatchesAutodetection `
            -IPAddress '10.2.0.180' `
            -AutodetectionMethod 'cidr=10.2.0.0/24' `
            -AddressFamily IPv4 |
            Should -BeTrue
    }

    It "rejects a persisted IPv4 management address from the old kind bridge CIDR" {
        Test-HnsManagementIPAddressMatchesAutodetection `
            -IPAddress '172.21.0.180' `
            -AutodetectionMethod 'cidr=10.2.0.0/24' `
            -AddressFamily IPv4 |
            Should -BeFalse
    }

    It "accepts a persisted IPv6 management address inside the current prefix" {
        Test-HnsManagementIPAddressMatchesAutodetection `
            -IPAddress '2001:5a8:4298:3b00:5054:ff:fe01:2345' `
            -AutodetectionMethod 'cidr=2001:5a8:4298:3b00::/64' `
            -AddressFamily IPv6 |
            Should -BeTrue
    }

    It "rejects a persisted IPv6 management address outside the current prefix" {
        Test-HnsManagementIPAddressMatchesAutodetection `
            -IPAddress 'fc00:f853:ccd:e793::180' `
            -AutodetectionMethod 'cidr=2001:5a8:4298:3b00::/64' `
            -AddressFamily IPv6 |
            Should -BeFalse
    }
}

Describe "Test-HnsManagementIPAddressIsAssigned" {
    It "accepts a persisted IPv6 management address that is currently assigned" {
        $addrs = @(
            [pscustomobject]@{ InterfaceAlias = 'Ethernet 2'; IPAddress = '2001:5a8:4298:3b00:5054:ff:fe01:2345' }
        )

        Test-HnsManagementIPAddressIsAssigned `
            -IPAddress '2001:5a8:4298:3b00:5054:ff:fe01:2345' `
            -Addresses $addrs `
            -AddressFamily IPv6 |
            Should -BeTrue
    }

    It "rejects a same-prefix persisted IPv6 management address that is no longer assigned" {
        $addrs = @(
            [pscustomobject]@{ InterfaceAlias = 'Ethernet 2'; IPAddress = '2001:5a8:4298:3b00:5054:ff:fe01:2345' }
        )

        Test-HnsManagementIPAddressIsAssigned `
            -IPAddress '2001:5a8:4298:3b00:2326:a05d:27c8:701f' `
            -Addresses $addrs `
            -AddressFamily IPv6 |
            Should -BeFalse
    }

    It "ignores matching addresses on non-management interfaces" {
        $addrs = @(
            [pscustomobject]@{ InterfaceAlias = 'Loopback Pseudo-Interface 1'; IPAddress = '10.2.0.180' }
        )

        Test-HnsManagementIPAddressIsAssigned `
            -IPAddress '10.2.0.180' `
            -Addresses $addrs `
            -AddressFamily IPv4 |
            Should -BeFalse
    }
}

Describe "Resolve-HnsManagementInterfaceAlias" {

    It "returns the InterfaceAlias of the first matching IPv4 (physical NIC pre-vSwitch)" {
        $addrs = @(
            [pscustomobject]@{ InterfaceAlias = 'Ethernet 3'; IPAddress = '10.2.0.180' }
        )
        Resolve-HnsManagementInterfaceAlias -Addresses $addrs -NetworkCIDR '10.2.0.0/24' |
            Should -Be 'Ethernet 3'
    }

    It "returns the physical management NIC when a temporary vEthernet NIC is also in the CIDR" {
        $addrs = @(
            [pscustomobject]@{ InterfaceAlias = 'vEthernet (Ethernet 2)'; IPAddress = '10.2.0.24' },
            [pscustomobject]@{ InterfaceAlias = 'Ethernet'; IPAddress = '10.2.0.3' }
        )
        Resolve-HnsManagementInterfaceAlias -Addresses $addrs -NetworkCIDR '10.2.0.0/24' |
            Should -Be 'Ethernet'
    }

    It "returns 'vEthernet (Calico)' when the management IP has migrated there" {
        # Reproduces the case where an existing Calico vSwitch holds
        # the management IPv4. The previous filter (Ethernet*/vEthernet
        # (Ethernet*)) missed this, External create fell through to the
        # no-AdapterName path and HNS auto-pick failed.
        $addrs = @(
            [pscustomobject]@{ InterfaceAlias = 'vEthernet (Calico)'; IPAddress = '10.2.0.11' }
        )
        Resolve-HnsManagementInterfaceAlias -Addresses $addrs -NetworkCIDR '10.2.0.0/24' |
            Should -Be 'vEthernet (Calico)'
    }

    It "ignores out-of-band NICs with addresses outside the requested CIDR" {
        # A virtualization host's user-mode-NAT or out-of-band management
        # NIC commonly has a 10.0.x.y address. We must NOT bind External
        # to that one.
        $addrs = @(
            [pscustomobject]@{ InterfaceAlias = 'Ethernet 2'; IPAddress = '10.0.2.15' },
            [pscustomobject]@{ InterfaceAlias = 'Ethernet 3'; IPAddress = '10.2.0.180' }
        )
        Resolve-HnsManagementInterfaceAlias -Addresses $addrs -NetworkCIDR '10.2.0.0/24' |
            Should -Be 'Ethernet 3'
    }

    It "returns null when nothing matches" {
        $addrs = @(
            [pscustomobject]@{ InterfaceAlias = 'Ethernet'; IPAddress = '192.168.1.1' }
        )
        Resolve-HnsManagementInterfaceAlias -Addresses $addrs -NetworkCIDR '10.2.0.0/24' |
            Should -BeNullOrEmpty
    }

    It "returns null on a malformed CIDR" {
        $addrs = @(
            [pscustomobject]@{ InterfaceAlias = 'Ethernet'; IPAddress = '10.2.0.180' }
        )
        Resolve-HnsManagementInterfaceAlias -Addresses $addrs -NetworkCIDR 'garbage' |
            Should -BeNullOrEmpty
    }
}

Describe "Test-IsCalicoManagedVMSwitch" {

    It "matches 'External' (Calico's placeholder switch)" {
        Test-IsCalicoManagedVMSwitch -Name 'External' | Should -BeTrue
    }
    It "matches 'Calico' (the main bridge name)" {
        Test-IsCalicoManagedVMSwitch -Name 'Calico' | Should -BeTrue
    }
    It "matches 'Calico_<id>' suffixes (per-pod endpoints during transient states)" {
        Test-IsCalicoManagedVMSwitch -Name 'Calico_aabbccddeeff' | Should -BeTrue
    }
    It "matches 'Calico-<id>' suffixes for upgrade flows" {
        Test-IsCalicoManagedVMSwitch -Name 'Calico-old' | Should -BeTrue
    }
    It "rejects 'CalicoExternal-Hyper-V' (foreign user-created switch)" {
        # Filter is intentionally narrow — anything that doesn't start
        # with 'Calico_' or 'Calico-' (or equal exactly Calico/External)
        # is left alone. A user-created switch named CalicoTest, Default
        # Switch, Hyper-V Switch etc. must NOT be collected by the
        # orphan-vSwitch cleanup.
        Test-IsCalicoManagedVMSwitch -Name 'CalicoTest' | Should -BeFalse
    }
    It "rejects 'Default Switch' (built-in NAT switch)" {
        Test-IsCalicoManagedVMSwitch -Name 'Default Switch' | Should -BeFalse
    }
    It "rejects 'Hyper-V Switch'" {
        Test-IsCalicoManagedVMSwitch -Name 'Hyper-V Switch' | Should -BeFalse
    }
}

# ---------------------------------------------------------------------
# Test-IsBrokenCalicoVMSwitch — predicate that distinguishes a healthy
# External vSwitch bound to a physical NIC from the half-created state
# left behind by an HCN_E_ADAPTER_NOT_FOUND mid-create. networkNeedsRecreate
# alone (subnet match check) is not enough; this fills the gap so a
# Calico HNS network that LOOKS healthy at the HNS layer but has a
# broken underlying vSwitch is actively wiped and rebuilt.
# ---------------------------------------------------------------------
Describe "Test-IsBrokenCalicoVMSwitch" {

    It "returns true when no vSwitch is supplied (HNS network has no Hyper-V switch)" {
        Test-IsBrokenCalicoVMSwitch -Switch $null | Should -BeTrue
    }

    It "returns true when SwitchType is Private (failed bind)" {
        $sw = [pscustomobject]@{
            Name = 'Calico'
            SwitchType = 'Private'
            NetAdapterInterfaceDescription = ''
        }
        Test-IsBrokenCalicoVMSwitch -Switch $sw | Should -BeTrue
    }

    It "returns true when SwitchType is Internal" {
        $sw = [pscustomobject]@{
            Name = 'Calico'
            SwitchType = 'Internal'
            NetAdapterInterfaceDescription = ''
        }
        Test-IsBrokenCalicoVMSwitch -Switch $sw | Should -BeTrue
    }

    It "returns true when SwitchType=External but NetAdapterInterfaceDescription is empty" {
        # The crucial "Calico HNS network reports L2Bridge with right
        # subnets, but its vSwitch is not actually wired to a NIC"
        # case. Get-VMSwitch on the affected host shows External but no
        # adapter binding.
        $sw = [pscustomobject]@{
            Name = 'Calico'
            SwitchType = 'External'
            NetAdapterInterfaceDescription = ''
        }
        Test-IsBrokenCalicoVMSwitch -Switch $sw | Should -BeTrue
    }

    It "returns true when NetAdapterInterfaceDescription is whitespace only" {
        $sw = [pscustomobject]@{
            Name = 'Calico'
            SwitchType = 'External'
            NetAdapterInterfaceDescription = '   '
        }
        Test-IsBrokenCalicoVMSwitch -Switch $sw | Should -BeTrue
    }

    It "returns false when vSwitch is healthy External + bound to a NIC" {
        $sw = [pscustomobject]@{
            Name = 'Calico'
            SwitchType = 'External'
            NetAdapterInterfaceDescription = 'Killer E3100G 2.5 Gigabit Ethernet Controller'
        }
        Test-IsBrokenCalicoVMSwitch -Switch $sw | Should -BeFalse
    }

    It "returns false for an Intel-class NIC binding (any non-empty value passes)" {
        $sw = [pscustomobject]@{
            Name = 'Calico'
            SwitchType = 'External'
            NetAdapterInterfaceDescription = 'Intel(R) 82574L Gigabit Network Connection'
        }
        Test-IsBrokenCalicoVMSwitch -Switch $sw | Should -BeFalse
    }
}

# ---------------------------------------------------------------------
# Get-CalicoHnsHookPaths — single source of truth for every host path
# the hns-ipv6 hook + injector touch. node-service.ps1 calls it once;
# any future debugging tool calls it instead of hardcoding the
# literals.  Each path is overridable via an env var named after the
# field.  These tests guarantee the env-var contract is honoured.
# ---------------------------------------------------------------------
Describe "Get-CalicoHnsHookPaths" {

    BeforeEach {
        # Snapshot env so leakage between cases is explicit.
        $script:savedEnv = @{}
        foreach ($k in @(
            'CALICO_HNS_HOOK_INSTALL_DIR',
            'CALICO_HNS_HOOK_DLL_PATH',
            'CALICO_HNS_HOOK_INJECTOR_PATH',
            'CALICO_HNS_HOOK_MARKER_PATH',
            'CALICO_HNS_HOOK_CFG_PATH',
            'CALICO_HNS_HOOK_LOG_PATH'))
        {
            $savedEnv[$k] = [Environment]::GetEnvironmentVariable($k, 'Process')
            [Environment]::SetEnvironmentVariable($k, $null, 'Process')
        }
    }
    AfterEach {
        foreach ($k in $savedEnv.Keys) {
            [Environment]::SetEnvironmentVariable($k, $savedEnv[$k], 'Process')
        }
    }

    It "defaults match the historical hardcoded values when no env vars set" {
        $p = Get-CalicoHnsHookPaths
        $p.InstallDir   | Should -Be 'C:\opt\calico-hns-ipv6'
        $p.DllPath      | Should -Be 'C:\opt\calico-hns-ipv6\hns-ipv6-hook.dll'
        $p.InjectorPath | Should -Be 'C:\opt\calico-hns-ipv6\hns-ipv6-injector.exe'
        $p.MarkerPath   | Should -Be 'C:\opt\calico-hns-ipv6\injected.flag'
        $p.CfgPath      | Should -Be 'C:\CalicoWindows\hns-ipv6-hook.cfg'
        # The new default — relocated from C:\hns-ipv6-hook.log per
        # the standard /var/log/<component> convention.
        $p.LogPath      | Should -Be 'C:\var\log\calico\hook.log'
    }

    It "InstallDir override propagates to DllPath / InjectorPath / MarkerPath" {
        # The most common operator override: relocate the install dir
        # so the DLL, injector, and marker all live under a custom path.
        # CfgPath and LogPath are independent (separate env vars) so
        # they retain their defaults.
        $env:CALICO_HNS_HOOK_INSTALL_DIR = 'D:\calico\hook'
        $p = Get-CalicoHnsHookPaths
        $p.InstallDir   | Should -Be 'D:\calico\hook'
        $p.DllPath      | Should -Be 'D:\calico\hook\hns-ipv6-hook.dll'
        $p.InjectorPath | Should -Be 'D:\calico\hook\hns-ipv6-injector.exe'
        $p.MarkerPath   | Should -Be 'D:\calico\hook\injected.flag'
        $p.CfgPath      | Should -Be 'C:\CalicoWindows\hns-ipv6-hook.cfg'
        $p.LogPath      | Should -Be 'C:\var\log\calico\hook.log'
    }

    It "individual path overrides win over InstallDir-derived defaults" {
        # An operator who wants the DLL at one path but the injector at
        # another (e.g. WDAC code-integrity rules force the injector
        # somewhere else). The per-path env vars take full precedence.
        $env:CALICO_HNS_HOOK_INSTALL_DIR  = 'D:\calico\hook'
        $env:CALICO_HNS_HOOK_DLL_PATH     = 'D:\sigs\calico-hook.dll'
        $env:CALICO_HNS_HOOK_INJECTOR_PATH = 'D:\bin\calico-injector.exe'
        $env:CALICO_HNS_HOOK_MARKER_PATH  = 'E:\state\calico-hook-marker'
        $p = Get-CalicoHnsHookPaths
        $p.InstallDir   | Should -Be 'D:\calico\hook'
        $p.DllPath      | Should -Be 'D:\sigs\calico-hook.dll'
        $p.InjectorPath | Should -Be 'D:\bin\calico-injector.exe'
        $p.MarkerPath   | Should -Be 'E:\state\calico-hook-marker'
    }

    It "CfgPath override is honoured even though it requires a matching DLL rebuild" {
        # The DLL's cfg file path is a compile-time constant
        # (DEFAULT_CFG_PATH in hook.c) — overriding this env var
        # tells node-service.ps1 + the injector where to *write* the
        # cfg, but a custom-built DLL must read from the same path.
        # We don't enforce that coupling here; we just confirm the
        # env var actually changes the resolved path.
        $env:CALICO_HNS_HOOK_CFG_PATH = 'D:\hook\my.cfg'
        (Get-CalicoHnsHookPaths).CfgPath | Should -Be 'D:\hook\my.cfg'
    }

    It "LogPath override moves the diagnostic log to an operator-chosen path" {
        # The runtime-overridable knob — DLL reads "log=<path>" from
        # the cfg file the injector writes, so changing this env var
        # is sufficient (no rebuild).
        $env:CALICO_HNS_HOOK_LOG_PATH = 'D:\logs\calico-hook-2026-05-06.log'
        (Get-CalicoHnsHookPaths).LogPath | Should -Be 'D:\logs\calico-hook-2026-05-06.log'
    }
}

Describe "Get-CalicoHnsHookPaths log fallthrough" {

    BeforeEach {
        $script:savedHookLog = [Environment]::GetEnvironmentVariable('CALICO_HNS_HOOK_LOG_PATH', 'Process')
        $script:savedLogDir  = [Environment]::GetEnvironmentVariable('CALICO_LOG_DIR', 'Process')
        [Environment]::SetEnvironmentVariable('CALICO_HNS_HOOK_LOG_PATH', $null, 'Process')
        [Environment]::SetEnvironmentVariable('CALICO_LOG_DIR', $null, 'Process')
    }
    AfterEach {
        [Environment]::SetEnvironmentVariable('CALICO_HNS_HOOK_LOG_PATH', $script:savedHookLog, 'Process')
        [Environment]::SetEnvironmentVariable('CALICO_LOG_DIR', $script:savedLogDir, 'Process')
    }

    It "uses CALICO_HNS_HOOK_LOG_PATH when set, ignoring CALICO_LOG_DIR" {
        $env:CALICO_LOG_DIR = 'C:\CalicoWindows\logs'
        $env:CALICO_HNS_HOOK_LOG_PATH = 'D:\custom\hook.log'
        (Get-CalicoHnsHookPaths).LogPath | Should -Be 'D:\custom\hook.log'
    }

    It "honours CALICO_LOG_DIR by writing hook.log under it when CALICO_HNS_HOOK_LOG_PATH is unset" {
        # Respects the existing Calico Windows log-dir convention:
        # in legacy NSSM installs CALICO_LOG_DIR defaults to
        # C:\CalicoWindows\logs and all calico-{node,felix,confd}.log
        # files live there. The hook log joins that root rather than
        # spraying logs across two trees.
        $env:CALICO_LOG_DIR = 'C:\CalicoWindows\logs'
        (Get-CalicoHnsHookPaths).LogPath | Should -Be 'C:\CalicoWindows\logs\hook.log'
    }

    It "falls back to the new /var/log/calico convention when neither var is set" {
        # HPC mode does not set CALICO_LOG_DIR (calico-{node,felix,confd}
        # log to stdout, captured by kubelet at C:\var\log\pods\...).
        # The hook DLL runs in svchost-hns where stdout isn't usable,
        # so it needs an on-disk path. The default mirrors the Linux
        # /var/log/calico convention.
        (Get-CalicoHnsHookPaths).LogPath | Should -Be 'C:\var\log\calico\hook.log'
    }
}

# ---------------------------------------------------------------------
# Test-RRASNeedsBootstrap — predicate that decides whether the calico-node
# pod's startup must run Install-RemoteAccess -VpnType RoutingOnly. The
# bug this catches: the playbook installs the Routing/RemoteAccess
# Windows features but doesn't run Install-RemoteAccess; without that
# step the RemoteAccess service stays Disabled, confd's Add-BgpRouter
# silently fails, and Pod->ClusterIP TCP from Windows pods times out
# silently because no Linux backend has a route back to the Windows
# pod /26.
# ---------------------------------------------------------------------
Describe "Test-RRASNeedsBootstrap" {

    It "returns true when the RemoteAccess service object is null (feature missing)" {
        # Get-Service -Name RemoteAccess on a host without the feature
        # raises an error; the caller catches it and passes $null.
        Test-RRASNeedsBootstrap -Service $null | Should -BeTrue
    }

    It "returns true when StartType=Disabled (Install-RemoteAccess never run)" {
        # The exact state every Windows worker is in immediately after
        # the playbook's win_feature step (RemoteAccess + Routing
        # installed but LAN routing not yet configured).
        $svc = [pscustomobject]@{ Name='RemoteAccess'; StartType='Disabled'; Status='Stopped' }
        Test-RRASNeedsBootstrap -Service $svc | Should -BeTrue
    }

    It "returns true when StartType=Manual but Status=Stopped" {
        # Service was configured at some point but isn't running.
        # Bootstrap re-runs Install-RemoteAccess + Start-Service.
        $svc = [pscustomobject]@{ Name='RemoteAccess'; StartType='Manual'; Status='Stopped' }
        Test-RRASNeedsBootstrap -Service $svc | Should -BeTrue
    }

    It "returns false when StartType=Manual + Status=Running" {
        $svc = [pscustomobject]@{ Name='RemoteAccess'; StartType='Manual'; Status='Running' }
        Test-RRASNeedsBootstrap -Service $svc | Should -BeFalse
    }

    It "returns false when StartType=Automatic + Status=Running (steady state)" {
        $svc = [pscustomobject]@{ Name='RemoteAccess'; StartType='Automatic'; Status='Running' }
        Test-RRASNeedsBootstrap -Service $svc | Should -BeFalse
    }

    It "returns true when service is running but LAN routing is known absent" {
        $svc = [pscustomobject]@{ Name='RemoteAccess'; StartType='Automatic'; Status='Running' }
        Test-RRASNeedsBootstrap -Service $svc -RoutingConfigured $false | Should -BeTrue
    }

    It "returns false when service is running and LAN routing is known present" {
        $svc = [pscustomobject]@{ Name='RemoteAccess'; StartType='Automatic'; Status='Running' }
        Test-RRASNeedsBootstrap -Service $svc -RoutingConfigured $true | Should -BeFalse
    }
}

Describe "Test-RRASRoutingConfigured" {
    It "returns true when Get-RemoteAccess reports RoutingStatus=Installed" {
        InModuleScope $script:moduleName {
            function Get-RemoteAccess { [pscustomobject]@{ RoutingStatus = 'Installed' } }
            Test-RRASRoutingConfigured | Should -BeTrue
            Remove-Item Function:\Get-RemoteAccess
        }
    }

    It "returns true when Get-RemoteAccess reports LanRoutingStatus=Enabled" {
        InModuleScope $script:moduleName {
            function Get-RemoteAccess { [pscustomobject]@{ LanRoutingStatus = 'Enabled' } }
            Test-RRASRoutingConfigured | Should -BeTrue
            Remove-Item Function:\Get-RemoteAccess
        }
    }

    It "returns false when Get-RemoteAccess reports RoutingStatus=Uninstalled" {
        InModuleScope $script:moduleName {
            function Get-RemoteAccess { [pscustomobject]@{ RoutingStatus = 'Uninstalled' } }
            Test-RRASRoutingConfigured | Should -BeFalse
            Remove-Item Function:\Get-RemoteAccess
        }
    }

    It "returns null when Get-RemoteAccess is unavailable" {
        InModuleScope $script:moduleName {
            Test-RRASRoutingConfigured | Should -BeNullOrEmpty
        }
    }
}

Describe "node-service complete-startup manager" {
    BeforeAll {
        $script:nodeService = Get-Content -Raw -Path (Join-Path $PSScriptRoot '../CalicoWindows/node/node-service.ps1')
    }

    It "starts calico-node.exe -complete-startup in the background" {
        $script:nodeService | Should -Match 'function Start-CompleteStartupManager'
        $script:nodeService | Should -Match 'Start-Process -NoNewWindow .*calico-node\.exe -ArgumentList "-complete-startup"'
    }

    It "guards against duplicate complete-startup processes" {
        $script:nodeService | Should -Match 'function Get-CompleteStartupPid'
        $script:nodeService | Should -Match 'function Ensure-CompleteStartupManager'
        $script:nodeService | Should -Match 'if \(-not \$\(Get-CompleteStartupPid\)\)'
    }

    It "marks networking available after both skipped and successful startup paths" {
        ([regex]::Matches($script:nodeService, 'Ensure-CompleteStartupManager')).Count | Should -BeGreaterOrEqual 3
        $script:nodeService | Should -Match 'Calico node initialisation skipped[\s\S]*?Ensure-CompleteStartupManager'
        $script:nodeService | Should -Match 'Calico node initialisation succeeded[\s\S]*?Ensure-CompleteStartupManager'
    }
}

Describe "Read-PersistedManagementPair" {
    It "returns nulls when the file does not exist" {
        $pair = Read-PersistedManagementPair -Path (Join-Path $TestDrive 'missing.env')
        $pair.V4 | Should -BeNullOrEmpty
        $pair.V6 | Should -BeNullOrEmpty
    }

    It "parses a v4+v6 pair" {
        $p = Join-Path $TestDrive 'pair.env'
        Set-Content -Path $p -Value "10.2.0.3`tfd5a:8000:1:0:1ac0:4dff:fe89:5194" -Encoding ASCII
        $pair = Read-PersistedManagementPair -Path $p
        $pair.V4 | Should -Be '10.2.0.3'
        $pair.V6 | Should -Be 'fd5a:8000:1:0:1ac0:4dff:fe89:5194'
    }

    It "parses a v4-only pair with empty v6 side" {
        $p = Join-Path $TestDrive 'pair4.env'
        Set-Content -Path $p -Value "10.2.0.3`t" -Encoding ASCII
        $pair = Read-PersistedManagementPair -Path $p
        $pair.V4 | Should -Be '10.2.0.3'
        $pair.V6 | Should -BeNullOrEmpty
    }
}

Describe "Test-IPAddressFamily" {
    It "classifies IPv4" {
        Test-IPAddressFamily -IPAddress '10.2.0.3' -AddressFamily IPv4 | Should -BeTrue
        Test-IPAddressFamily -IPAddress '10.2.0.3' -AddressFamily IPv6 | Should -BeFalse
    }
    It "classifies IPv6" {
        Test-IPAddressFamily -IPAddress 'fd5a:8000:1::1' -AddressFamily IPv6 | Should -BeTrue
        Test-IPAddressFamily -IPAddress 'fd5a:8000:1::1' -AddressFamily IPv4 | Should -BeFalse
    }
    It "rejects garbage and empty input" {
        Test-IPAddressFamily -IPAddress '' -AddressFamily IPv4 | Should -BeFalse
        Test-IPAddressFamily -IPAddress 'not-an-ip' -AddressFamily IPv4 | Should -BeFalse
    }
}

Describe "Resolve-DesiredHnsManagementAddress" {
    BeforeAll {
        # NetIPAddress-shaped helpers. "Ethernet" is the management NIC,
        # "Ethernet 2" the USB NIC in the same subnet (appmana-003 layout).
        $script:mgmtAddr = [pscustomobject]@{ InterfaceAlias = 'Ethernet';   IPAddress = '10.2.0.3' }
        $script:usbAddr  = [pscustomobject]@{ InterfaceAlias = 'Ethernet 2'; IPAddress = '10.2.0.24' }
        $script:noSleep  = { param($s) }
    }

    It "returns the explicit desired address immediately without assignment checks" {
        $r = Resolve-DesiredHnsManagementAddress -AddressFamily IPv4 `
            -ExplicitDesired '10.2.0.99' -NodeIP '10.2.0.3' -PersistedDesired '10.2.0.3' `
            -AutodetectionMethod 'cidr=10.2.0.0/24' `
            -SnapshotProvider { @() } -DeadlineSeconds 0 -SleepFunction $script:noSleep
        $r.Address | Should -Be '10.2.0.99'
        $r.Source | Should -Be 'explicit'
    }

    It "prefers the kubelet node IP over the persisted pair" {
        $r = Resolve-DesiredHnsManagementAddress -AddressFamily IPv4 `
            -NodeIP '10.2.0.3' -PersistedDesired '10.2.0.24' `
            -AutodetectionMethod 'cidr=10.2.0.0/24' `
            -SnapshotProvider { @($script:mgmtAddr, $script:usbAddr) } `
            -DeadlineSeconds 0 -SleepFunction $script:noSleep
        $r.Address | Should -Be '10.2.0.3'
        $r.Source | Should -Be 'node-ip'
    }

    It "waits for a temporarily-unassigned persisted address instead of falling back (the appmana-003 USB-NIC brick)" {
        # First two snapshots only show the USB NIC (management NIC re-binding
        # after boot/HNS restart); the third shows the management NIC again.
        $script:calls = 0
        $provider = {
            $script:calls++
            if ($script:calls -ge 3) { @($script:mgmtAddr, $script:usbAddr) } else { @($script:usbAddr) }
        }
        $r = Resolve-DesiredHnsManagementAddress -AddressFamily IPv4 `
            -PersistedDesired '10.2.0.3' `
            -AutodetectionMethod 'cidr=10.2.0.0/24' `
            -SnapshotProvider $provider `
            -DeadlineSeconds 30 -PollSeconds 3 -SleepFunction $script:noSleep
        $r.Address | Should -Be '10.2.0.3'
        $r.Source | Should -Be 'persisted'
        $r.WaitedSeconds | Should -BeGreaterThan 0
    }

    It "falls back to cidr autodetection only after the deadline expires" {
        $script:sleptTotal = 0
        $sleepCounter = { param($s) $script:sleptTotal += $s }
        $r = Resolve-DesiredHnsManagementAddress -AddressFamily IPv4 `
            -PersistedDesired '10.2.0.3' `
            -AutodetectionMethod 'cidr=10.2.0.0/24' `
            -SnapshotProvider { @($script:usbAddr) } `
            -DeadlineSeconds 12 -PollSeconds 5 -SleepFunction $sleepCounter
        $r.Address | Should -Be '10.2.0.24'
        $r.Source | Should -Be 'cidr'
        $r.WaitedSeconds | Should -Be 12
        $script:sleptTotal | Should -Be 12
    }

    It "skips the wait entirely when there is no waitable candidate (first boot)" {
        $script:slept = $false
        $r = Resolve-DesiredHnsManagementAddress -AddressFamily IPv4 `
            -AutodetectionMethod 'cidr=10.2.0.0/24' `
            -SnapshotProvider { @($script:mgmtAddr) } `
            -DeadlineSeconds 120 -SleepFunction { param($s) $script:slept = $true }
        $r.Address | Should -Be '10.2.0.3'
        $r.Source | Should -Be 'cidr'
        $r.WaitedSeconds | Should -Be 0
        $script:slept | Should -BeFalse
    }

    It "ignores a node IP outside the autodetection cidr" {
        $r = Resolve-DesiredHnsManagementAddress -AddressFamily IPv4 `
            -NodeIP '192.168.1.5' `
            -AutodetectionMethod 'cidr=10.2.0.0/24' `
            -SnapshotProvider { @($script:mgmtAddr) } `
            -DeadlineSeconds 0 -SleepFunction $script:noSleep
        $r.Address | Should -Be '10.2.0.3'
        $r.Source | Should -Be 'cidr'
    }

    It "ignores a node IP of the wrong address family" {
        $r = Resolve-DesiredHnsManagementAddress -AddressFamily IPv6 `
            -NodeIP '10.2.0.3' -PersistedDesired 'fd5a:8000:1::5194' `
            -AutodetectionMethod 'cidr=fd5a:8000:1::/64' `
            -SnapshotProvider { @([pscustomobject]@{ InterfaceAlias = 'Ethernet'; IPAddress = 'fd5a:8000:1::5194' }) } `
            -DeadlineSeconds 0 -SleepFunction $script:noSleep
        $r.Address | Should -Be 'fd5a:8000:1::5194'
        $r.Source | Should -Be 'persisted'
    }
}

Describe "node-service management pair resolution call sites" {
    BeforeAll {
        $script:nodeServiceSrc = Get-Content -Raw -Path (Join-Path $PSScriptRoot '../CalicoWindows/node/node-service.ps1')
    }

    It "defines the shared Resolve-CurrentDesiredManagementPair helper" {
        $script:nodeServiceSrc | Should -Match 'function Resolve-CurrentDesiredManagementPair'
        $script:nodeServiceSrc | Should -Match 'CALICO_MGMT_PAIR_WAIT_SECONDS'
        $script:nodeServiceSrc | Should -Match '-NodeIP \$env:NODE_IP'
    }

    It "uses the shared helper at all three decision sites" {
        ([regex]::Matches($script:nodeServiceSrc, 'Resolve-CurrentDesiredManagementPair')).Count | Should -BeGreaterOrEqual 4
        # No site may derive the desired pair from a raw one-shot snapshot anymore.
        $script:nodeServiceSrc | Should -Not -Match 'Resolve-DesiredHnsManagementIPv4 -Addresses \(Get-NetIPAddress'
        $script:nodeServiceSrc | Should -Not -Match 'Resolve-DesiredHnsManagementIPv6 -Addresses \(Get-NetIPAddress'
    }

    It "warns loudly before overwriting the persisted pair" {
        $script:nodeServiceSrc | Should -Match 'OVERWRITING persisted management pair'
    }
}

Describe "Get-RenderedBgpPeerNames" {
    It "returns empty for a missing file" {
        Get-RenderedBgpPeerNames -PeeringsPath (Join-Path $TestDrive 'nope.ps1') | Should -HaveCount 0
    }

    It "parses peer names out of a rendered peerings.ps1" {
        $p = Join-Path $TestDrive 'peerings.ps1'
        @'
$local_ip = "10.2.0.11"
$peerings =
    # IPv4 is enabled on this node.
    @{ Name = "Mesh_10_2_0_3"; IP = "10.2.0.3"; AS = 65414 },
    @{ Name = "Mesh6_fd5a_8000_1_0_1"; IP = "fd5a:8000:1::1"; LocalIP = "fd5a:8000:1::2"; AS = 65414 },
    @{ Name = "Global_10_2_0_1"; IP = "10.2.0.1"; AS = 65000; KeepOriginalNextHop = $true },
    @{}
'@ | Set-Content -Path $p -Encoding ASCII
        $names = Get-RenderedBgpPeerNames -PeeringsPath $p
        $names | Should -Be @('Mesh_10_2_0_3', 'Mesh6_fd5a_8000_1_0_1', 'Global_10_2_0_1')
    }

    It "returns empty for a rendered file with no peers (IPv4 disabled)" {
        $p = Join-Path $TestDrive 'peerings-empty.ps1'
        "`$peerings =`n    @{}" | Set-Content -Path $p -Encoding ASCII
        Get-RenderedBgpPeerNames -PeeringsPath $p | Should -HaveCount 0
    }
}

Describe "Get-BgpPeerDrift" {
    It "reports no drift when sets match" {
        $d = Get-BgpPeerDrift -RenderedPeerNames @('Mesh_10_2_0_3', 'Global_10_2_0_1') -ActualPeerNames @('Global_10_2_0_1', 'Mesh_10_2_0_3')
        $d.Missing | Should -HaveCount 0
        $d.Extra | Should -HaveCount 0
    }

    It "reports every rendered peer missing when RRAS has none (the appmana-005 wipe)" {
        $d = Get-BgpPeerDrift -RenderedPeerNames @('Mesh_10_2_0_3', 'Global_10_2_0_1') -ActualPeerNames @()
        $d.Missing | Should -Be @('Mesh_10_2_0_3', 'Global_10_2_0_1')
        $d.Extra | Should -HaveCount 0
    }

    It "reports stale confd-managed peers as extra but ignores operator peers" {
        $d = Get-BgpPeerDrift -RenderedPeerNames @('Mesh_10_2_0_3') -ActualPeerNames @('Mesh_10_2_0_3', 'Mesh_10_2_0_99', 'OperatorSpecial')
        $d.Missing | Should -HaveCount 0
        $d.Extra | Should -Be @('Mesh_10_2_0_99')
    }
}

Describe "node-service BGP drift repair wiring" {
    BeforeAll {
        $script:nodeServiceDrift = Get-Content -Raw -Path (Join-Path $PSScriptRoot '../CalicoWindows/node/node-service.ps1')
    }

    It "defines a throttled Invoke-BgpDriftRepairIfNeeded" {
        $script:nodeServiceDrift | Should -Match 'function Invoke-BgpDriftRepairIfNeeded'
        $script:nodeServiceDrift | Should -Match 'lastBgpDriftRepair'
        $script:nodeServiceDrift | Should -Match 'Get-BgpPeerDrift'
    }

    It "runs the drift check from the main monitoring loop after startup" {
        $script:nodeServiceDrift | Should -Match 'Ensure-CompleteStartupManager\s+Invoke-BgpDriftRepairIfNeeded'
    }
}

Describe "Test-CalicoBridgeEpochMarkerFresh" {
    It "is false when the marker does not exist" {
        Test-CalicoBridgeEpochMarkerFresh -MarkerPath (Join-Path $TestDrive 'nope.flag') -BootTime (Get-Date).AddHours(-1) | Should -BeFalse
    }

    It "is true when the marker is newer than boot" {
        $m = Join-Path $TestDrive 'epoch.flag'
        Set-Content -Path $m -Value 'x'
        Test-CalicoBridgeEpochMarkerFresh -MarkerPath $m -BootTime (Get-Date).AddHours(-1) | Should -BeTrue
    }

    It "is false when the marker predates boot (bridge persisted across reboot)" {
        $m = Join-Path $TestDrive 'epoch-old.flag'
        Set-Content -Path $m -Value 'x'
        (Get-Item $m).LastWriteTime = (Get-Date).AddHours(-2)
        Test-CalicoBridgeEpochMarkerFresh -MarkerPath $m -BootTime (Get-Date).AddHours(-1) | Should -BeFalse
    }
}

Describe "Test-CalicoStartupCanSkip boot-epoch gating" {
    BeforeAll {
        $script:net = [pscustomobject]@{ Name = 'Calico'; Type = 'L2Bridge'; ManagementIP = '10.2.0.3' }
    }

    It "refuses to skip when the bridge persisted from a previous boot" {
        Test-CalicoStartupCanSkip -ExistingCalicoNetwork $script:net -ExpectedManagementIP '10.2.0.3' `
            -IPv6SupportEnabled $false -BridgeFromCurrentBoot $false | Should -BeFalse
    }

    It "skips for a current-boot bridge with matching ManagementIP" {
        Test-CalicoStartupCanSkip -ExistingCalicoNetwork $script:net -ExpectedManagementIP '10.2.0.3' `
            -IPv6SupportEnabled $false -BridgeFromCurrentBoot $true | Should -BeTrue
    }
}
