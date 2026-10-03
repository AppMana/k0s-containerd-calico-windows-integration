# Isolated k0s lab only. Production rollout still drains first. This experiment
# deliberately keeps tasks alive while their kubelet supervisor is stopped.
param(
    [Parameter(Mandatory)][ValidatePattern('^[0-9a-f]{64}$')][string]$TransactionSHA256,
    [Parameter(Mandatory)][string]$BaselineVersion,
    [Parameter(Mandatory)][string]$CandidateVersion,
    [Parameter(Mandatory)][ValidatePattern('^[0-9a-f]{64}$')][string]$ArchiveSHA256,
    [Parameter(Mandatory)][ValidatePattern('^[a-zA-Z0-9][a-zA-Z0-9._-]*\.tar\.gz$')][string]$ArchiveName
)
$ErrorActionPreference='Stop'
Set-StrictMode -Version Latest
$transaction='C:\LabQualification\runtime-transaction.ps1'
$config='C:\LabQualification\runtime.toml'
$evidence='C:\LabQualification\runtime-workload-upgrade.json'
if(Test-Path $evidence){throw 'Refusing to overwrite previous upgrade evidence'}
if((Get-FileHash $transaction -Algorithm SHA256).Hash.ToLowerInvariant() -ne $TransactionSHA256){throw 'Deployment transaction hash mismatch'}
$before=Get-CimInstance Win32_Service -Filter "Name='containerd'"
if(-not $before -or $before.State -ne 'Running'){throw 'Missing serving baseline runtime'}
$match=[regex]::Match($before.PathName,'^"([^"]+)"')
if(-not $match.Success){throw 'Unexpected baseline executable path'}
$baselineExe=$match.Groups[1].Value
$version=& $baselineExe --version
if($LASTEXITCODE -ne 0 -or $version -notmatch ('\sv?'+[regex]::Escape($BaselineVersion)+'\s')){throw "Wrong baseline runtime: $version"}
if((Get-Service k0sworker).Status -ne 'Running'){throw 'k0s worker must be running before the upgrade experiment'}
if(@(Get-CimInstance Win32_Process -Filter "Name='kubelet.exe'").Count -ne 1){throw 'Expected one live baseline kubelet'}
$configSHA=(Get-FileHash $config -Algorithm SHA256).Hash
$media=@(Get-Volume -FileSystemLabel LCQUAL)
if($media.Count -ne 1){throw 'Expected one explicit qualification medium'}
$archive=Join-Path ($media[0].DriveLetter+':\') $ArchiveName
$staged=& $transaction -Mode Stage -Version $CandidateVersion -ArchiveSHA256 $ArchiveSHA256 -Archive $archive | ConvertFrom-Json
if($LASTEXITCODE -ne 0){throw 'Candidate staging failed'}
if((Get-CimInstance Win32_Service -Filter "Name='containerd'").ProcessId -ne $before.ProcessId){throw 'Staging disrupted serving runtime'}
try {
    Stop-Service k0sworker
    (Get-Service k0sworker).WaitForStatus('Stopped',[TimeSpan]::FromSeconds(45))
    $deadline=[DateTime]::UtcNow.AddSeconds(45)
    while(@(Get-CimInstance Win32_Process -Filter "Name='kubelet.exe'" -ErrorAction Stop).Count -ne 0){
        if([DateTime]::UtcNow -ge $deadline){throw 'Supervisor left a live kubelet; refusing runtime change'}
        Start-Sleep -Milliseconds 250
    }
    & $transaction -Mode Apply -Version $CandidateVersion -ArchiveSHA256 $ArchiveSHA256 -ConfigPath $config | Out-Null
    if($LASTEXITCODE -ne 0){throw 'Runtime transaction failed'}
} finally {
    Start-Service k0sworker
}
if((Get-FileHash $config -Algorithm SHA256).Hash -ne $configSHA){throw 'Runtime upgrade changed configuration'}
$after=Get-CimInstance Win32_Service -Filter "Name='containerd'"
if($after.State -ne 'Running' -or $after.ProcessId -eq $before.ProcessId -or $after.PathName -notlike ('*'+$staged.stage+'*')){throw 'Candidate runtime service identity was not activated'}
$result=@{baseline=$BaselineVersion;candidate=$CandidateVersion;beforePID=$before.ProcessId;afterPID=$after.ProcessId;configSHA256=$configSHA;transactionSHA256=$TransactionSHA256}
[IO.File]::WriteAllText($evidence,($result|ConvertTo-Json),[Text.UTF8Encoding]::new($false))
Write-Output 'RUNTIME_TRANSACTION_ACTIVATED; workload continuity still requires the independent consumer'
