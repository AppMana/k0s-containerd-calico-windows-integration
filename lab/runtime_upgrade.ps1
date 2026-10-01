# Runs only in the SDK-owned private Windows VM, using its read-only input ISO.
$ErrorActionPreference='Stop'
Set-StrictMode -Version Latest
function Native([string]$Exe,[string[]]$Arguments) {
    $priorPreference=$ErrorActionPreference
    try {
        $ErrorActionPreference='Continue'
        $result=& $Exe @Arguments 2>&1
        $code=$LASTEXITCODE
    } finally {$ErrorActionPreference=$priorPreference}
    if($code -ne 0){throw "$Exe failed ($code): $result"}
    return $result
}
function AssertContent([string]$Ctr,[string]$Phase) {
    $output="C:\readback-$Phase.bin"
    $errors="C:\readback-$Phase.err"
    $read=Start-Process $Ctr -ArgumentList @('-n','k8s.io','content','get',"sha256:$sha") -RedirectStandardOutput $output -RedirectStandardError $errors -PassThru -Wait
    $actual=(Get-FileHash $output -Algorithm SHA256).Hash.ToLowerInvariant()
    Write-Output "Content readback phase=$Phase exit=$($read.ExitCode) expected=$sha actual=$actual bytes=$((Get-Item $output).Length)"
    Write-Output "Content readback stderr: $([IO.File]::ReadAllText($errors))"
    Native $Ctr @('-n','k8s.io','content','list') | Write-Output
    if($read.ExitCode -ne 0 -or $actual -ne $sha){throw "Content readback failed ($Phase); inspect exit status, stderr, and digest evidence before attributing data loss"}
}
$volumes=@(Get-Volume | Where-Object DriveType -eq 'CD-ROM' | ForEach-Object { "$($_.DriveLetter):\" } | Where-Object {Test-Path (Join-Path $_ 'upgrade-inputs.json')})
if($volumes.Count -ne 1){throw 'Expected one upgrade input medium'}
$media=$volumes[0]
$inputs=Get-Content (Join-Path $media 'upgrade-inputs.json') -Raw | ConvertFrom-Json
foreach($artifact in @($inputs.baseline,$inputs.candidate)){
    if((Get-FileHash (Join-Path $media $artifact.file) -Algorithm SHA256).Hash.ToLowerInvariant() -ne $artifact.sha256){throw 'Runtime archive hash mismatch'}
}
if(Get-Service containerd -ErrorAction SilentlyContinue){throw 'Unexpected existing service in fresh lab image'}
$legacy='C:\Program Files\containerd'
if(Test-Path $legacy){throw 'Unexpected existing runtime directory'}
New-Item -ItemType Directory $legacy | Out-Null
Native tar.exe @('-xzf',(Join-Path $media $inputs.baseline.file),'-C',$legacy,'--strip-components=1') | Out-Null
$oldExe=Join-Path $legacy 'containerd.exe'
$oldCtr=Join-Path $legacy 'ctr.exe'
$config=Join-Path $legacy 'config.toml'
$text=(Native $oldExe @('config','default')) -join "`n"
[IO.File]::WriteAllText($config,$text,[Text.UTF8Encoding]::new($false))
$configSHA=(Get-FileHash $config -Algorithm SHA256).Hash
$legacyPath='"'+$oldExe+'" --run-service'
New-Service -Name containerd -BinaryPathName $legacyPath -StartupType Automatic | Out-Null
# Resolve the original shim without changing the machine's global PATH.
New-ItemProperty 'HKLM:\SYSTEM\CurrentControlSet\Services\containerd' -Name Environment -PropertyType MultiString -Value ([string[]]@('PATH='+$legacy+';'+[Environment]::GetEnvironmentVariable('Path','Machine'))) | Out-Null
Start-Service containerd
$ready=$false
for($i=0;$i -lt 30;$i++){
    try { Native $oldCtr @('--timeout','3s','version') | Out-Null; $ready=$true; break } catch {Start-Sleep -Seconds 1}
}
if(-not $ready){throw 'Baseline runtime failed to start'}
$baselineVersion=(Native $oldExe @('--version')) -join ' '
if(-not $baselineVersion.Contains('v2.2.1-post.3') -or -not $baselineVersion.Contains('cc29cefb23b015605eaff5699ecd0c8ccbcf566f')){throw "Not the deployed baseline: $baselineVersion"}
$pidBefore=(Get-CimInstance Win32_Service -Filter "Name='containerd'").ProcessId
$payload='C:\acknowledged-payload.bin'
[IO.File]::WriteAllBytes($payload,[Text.Encoding]::UTF8.GetBytes('acknowledged content before runtime upgrade'))
$sha=(Get-FileHash $payload -Algorithm SHA256).Hash.ToLowerInvariant()
$ingest=Start-Process $oldCtr -ArgumentList @('-n','k8s.io','content','ingest','--expected-digest',"sha256:$sha",'upgrade-payload') -RedirectStandardInput $payload -RedirectStandardOutput 'C:\ingest.out' -RedirectStandardError 'C:\ingest.err' -PassThru -Wait
if($ingest.ExitCode -ne 0){throw "Content ingest failed: $(Get-Content C:\ingest.err -Raw)"}
# An unreferenced blob is eligible for collection on an ordinary daemon restart.
# Model retained content explicitly, rather than treating permitted GC as loss.
Native $oldCtr @('-n','k8s.io','content','label',"sha256:$sha",'containerd.io/gc.root=upgrade-qualification') | Out-Null
AssertContent $oldCtr 'before-stage'
$candidate=@{Archive=(Join-Path $media $inputs.candidate.file);ArchiveSHA256=$inputs.candidate.sha256;Version=$inputs.candidate.version}
# A bad checksum must be rejected without touching the serving old runtime.
$bad=$candidate.Clone();$bad.ArchiveSHA256=('0'*64)
$rejected=$false
try{& C:\containerd_transaction.ps1 -Mode Stage @bad}catch{$rejected=$true}
if(-not $rejected){throw 'Accepted corrupt archive identity'}
if((Get-CimInstance Win32_Service -Filter "Name='containerd'").ProcessId -ne $pidBefore){throw 'Bad stage disrupted the old runtime'}
$staged=& C:\containerd_transaction.ps1 -Mode Stage @candidate | ConvertFrom-Json
if((Get-CimInstance Win32_Service -Filter "Name='containerd'").ProcessId -ne $pidBefore){throw 'Staging disrupted the old runtime'}
AssertContent $oldCtr 'before-apply'
& C:\containerd_transaction.ps1 -Mode Apply @candidate
if((Get-FileHash $config -Algorithm SHA256).Hash -ne $configSHA){throw 'Upgrade rewrote configuration'}
if(-not (Test-Path $oldExe)){throw 'Upgrade removed the old executable'}
$newCtr=Join-Path $staged.stage 'bin\ctr.exe'
AssertContent $newCtr 'after-apply'
$pidAfter=(Get-CimInstance Win32_Service -Filter "Name='containerd'").ProcessId
$result=& C:\containerd_transaction.ps1 -Mode Apply @candidate
if($result -ne 'UNCHANGED' -or (Get-CimInstance Win32_Service -Filter "Name='containerd'").ProcessId -ne $pidAfter){throw 'Repeated apply restarted the runtime'}
Native $newCtr @('--timeout','3s','version')
Write-Output 'WINDOWS_RUNTIME_UPGRADE_COMPLETE'
