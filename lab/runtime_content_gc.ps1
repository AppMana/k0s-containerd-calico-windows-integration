# Negative control: restart the SAME installed daemon, without an upgrade.
# This script is for an owned, isolated runtime VM with no kubelet/workloads.
$ErrorActionPreference='Stop'
if(Get-Service kubelet -ErrorAction SilentlyContinue){throw 'Control requires isolated runtime VM'}
$service=Get-CimInstance Win32_Service -Filter "Name='containerd'"
$match=[regex]::Match($service.PathName,'^"([^"]+)"')
if(-not $match.Success){throw "Expected quoted runtime executable; observed path=$($service.PathName); payloadExists=$(Test-Path C:\acknowledged-payload.bin); diagnosticExists=$(Test-Path C:\runtime_upgrade.ps1)"}
$ctr=Join-Path (Split-Path $match.Groups[1].Value) 'ctr.exe'
$ns='lab-gc-'+[guid]::NewGuid().ToString('N')
$digests=@{}
foreach($kind in @('rooted','unreferenced')){
    $payload="C:\gc-$kind.bin"
    [IO.File]::WriteAllBytes($payload,[Text.Encoding]::UTF8.GetBytes("$ns-$kind"))
    $sha=(Get-FileHash $payload -Algorithm SHA256).Hash.ToLowerInvariant()
    $digests[$kind]=$sha
    $p=Start-Process $ctr -ArgumentList @('-n',$ns,'content','ingest','--expected-digest',"sha256:$sha",$kind) -RedirectStandardInput $payload -RedirectStandardOutput "C:\gc-$kind.out" -RedirectStandardError "C:\gc-$kind.err" -Wait -PassThru
    if($p.ExitCode -ne 0){throw "Ingest $kind failed"}
    if($kind -eq 'rooted'){
        $label=Start-Process $ctr -ArgumentList @('-n',$ns,'content','label',"sha256:$sha",'containerd.io/gc.root=qualification') -RedirectStandardOutput 'C:\gc-label.out' -RedirectStandardError 'C:\gc-label.err' -Wait -PassThru
        if($label.ExitCode -ne 0){throw "GC root labeling failed: $([IO.File]::ReadAllText('C:\gc-label.err'))"}
    }
}
Restart-Service containerd
$ready=$false
for($i=0;$i -lt 30;$i++){
    & $ctr --timeout 3s version
    if($LASTEXITCODE -eq 0){$ready=$true;break}
    Start-Sleep -Seconds 1
}
if(-not $ready){throw 'Same runtime restart failed'}
foreach($kind in @('rooted','unreferenced')){
    $sha=$digests[$kind]
    $p=Start-Process $ctr -ArgumentList @('-n',$ns,'content','get',"sha256:$sha") -RedirectStandardOutput "C:\gc-$kind-read.bin" -RedirectStandardError "C:\gc-$kind-read.err" -Wait -PassThru
    Write-Output "SAME_VERSION_RESTART kind=$kind exit=$($p.ExitCode) stderr=$([IO.File]::ReadAllText("C:\gc-$kind-read.err"))"
    if($kind -eq 'rooted'){
        if($p.ExitCode -ne 0 -or (Get-FileHash "C:\gc-$kind-read.bin" -Algorithm SHA256).Hash.ToLowerInvariant() -ne $sha){throw 'Rooted data lost on restart'}
    } elseif($p.ExitCode -ne 1 -or -not [IO.File]::ReadAllText("C:\gc-$kind-read.err").Contains("content digest sha256:${sha}: not found")){
        throw 'Expected exact unreferenced-content NotFound; control inconclusive'
    }
}
Write-Output 'SAME_VERSION_GC_CONTROL_COMPLETE'
