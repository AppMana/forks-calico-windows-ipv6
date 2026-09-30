param([Parameter(Mandatory=$true)][string]$Source)
$ErrorActionPreference = 'Stop'
# Execute the actual checkpoint reader with real ConvertFrom-Json. PowerShell
# 5.1 and 7 enumerate decoded JSON arrays differently in an array subexpression.
$tokens=$null; $errors=$null
$ast=[System.Management.Automation.Language.Parser]::ParseFile($Source,[ref]$tokens,[ref]$errors)
if($errors.Count){throw 'source parse failed'}
$function=$ast.Find({param($n) $n -is [System.Management.Automation.Language.FunctionDefinitionAst] -and $n.Name -eq 'Complete-ManagementRouteTransition'},$true)
if(!$function){throw 'production checkpoint reader missing'}
Invoke-Expression $function.Extent.Text
function Get-ManagementRouteCheckpointPath { 'checkpoint-fixture' }
function Test-Path { $true }
function Get-Content { '[{"Addresses":["192.0.2.20"],"DestinationPrefix":"198.18.123.0/24"},{"Addresses":["10.244.163.2"],"DestinationPrefix":"0.0.0.0/0"}]' }
function Restore-ManagementRoutes($Snapshot) {
    if($Snapshot.Count -ne 2){throw "expected two flat routes, got $($Snapshot.Count)"}
    foreach($route in $Snapshot){if($route.DestinationPrefix -isnot [string]){throw 'nested route array'}}
    $script:restored=$true
}
function Remove-Item { if(!$script:restored){throw 'checkpoint removed without restoration'}; $script:removed=$true }
$script:restored=$false; $script:removed=$false
Complete-ManagementRouteTransition
if(!$script:restored -or !$script:removed){throw 'checkpoint flow incomplete'}
"CHECKPOINT_SHAPE_PASSED:$($PSVersionTable.PSVersion)"
