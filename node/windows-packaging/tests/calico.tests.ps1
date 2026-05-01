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
