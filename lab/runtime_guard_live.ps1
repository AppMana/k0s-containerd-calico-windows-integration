# Read-only native check. Invoke through Labcontainers serial QGA on a lab VM
# with a live supervised kubelet. Never runs transaction Stage/Apply.
param(
    [Parameter(Mandatory)][string]$TransactionPath,
    [Parameter(Mandatory)][ValidatePattern('^[0-9a-f]{64}$')][string]$ExpectedSHA256
)
$ErrorActionPreference='Stop'
Set-StrictMode -Version Latest
if((Get-FileHash -LiteralPath $TransactionPath -Algorithm SHA256).Hash.ToLowerInvariant() -ne $ExpectedSHA256){throw 'Transaction source hash mismatch'}
$processes=@(Get-CimInstance -ClassName Win32_Process -Filter "Name='kubelet.exe'" -ErrorAction Stop)
if($processes.Count -eq 0){throw 'This reproduction requires an actual running kubelet process'}
$service=Get-Service kubelet -ErrorAction SilentlyContinue
if($service -and $service.Status -ne 'Stopped'){throw 'This reproduction requires kubelet without a running named service'}
$before=Get-CimInstance Win32_Service -Filter "Name='containerd'" -ErrorAction Stop
if(-not $before -or $before.State -ne 'Running'){throw 'Expected serving containerd baseline'}
$text=Get-Content -LiteralPath $TransactionPath -Raw
$start=$text.IndexOf('$kubelet = Get-Service')
$end=$text.IndexOf('$old = Get-CimInstance', $start)
if($start -lt 0 -or $end -le $start){throw 'Cannot locate production precondition'}
$guard=[scriptblock]::Create($text.Substring($start,$end-$start))
$rejected=$false
try { & $guard } catch {
    if($_.Exception.Message -notlike '*Stop*kubelet*'){throw}
    $rejected=$true
}
$after=Get-CimInstance Win32_Service -Filter "Name='containerd'" -ErrorAction Stop
if($after.ProcessId -ne $before.ProcessId -or $after.PathName -ne $before.PathName -or $after.State -ne 'Running'){throw 'Read-only guard check changed containerd identity or state'}
if(-not $rejected){throw 'Unsafe runtime upgrade precondition accepted a live supervised kubelet'}
Write-Output "SUPERVISED_KUBELET_GUARD_COMPLETE kubelets=$($processes.Count) containerdPID=$($after.ProcessId)"
