Describe 'Lab workload upgrade supervisor lifecycle' {
    BeforeAll {
        $tokens=$null; $errors=$null
        $ast=[Management.Automation.Language.Parser]::ParseFile((Join-Path $PSScriptRoot 'runtime_workload_upgrade.ps1'),[ref]$tokens,[ref]$errors)
        if($errors.Count){throw $errors[0]}
        $node=$ast.Find({param($n) $n -is [Management.Automation.Language.TryStatementAst]},$true)
        if(-not $node){throw 'Missing protected supervisor lifecycle'}
        $lifecycle=[scriptblock]::Create($node.Extent.Text)
        function Stop-Service {param($Name)}
        function Start-Service {param($Name)}
        function Get-Service {param($Name)}
        function Get-CimInstance {param($ClassName,$Filter)}
    }
    BeforeEach {
        $CandidateVersion='2.3.5-appmana.post.1';$ArchiveSHA256='a'*64;$config='unused'
        $script:applied=$false;$script:failApply=$false
        $transaction={param($Mode,$Version,$ArchiveSHA256,$ConfigPath)
            $script:applied=$true
            if($script:failApply){throw 'transaction failure'}
            $global:LASTEXITCODE=0
        }
        Mock Stop-Service {}
        Mock Start-Service {}
        Mock Get-CimInstance { @() }
        Mock Get-Service {
            $service=[pscustomobject]@{}
            $service | Add-Member ScriptMethod WaitForStatus {param($state,$timeout)}
            $service
        }
    }
    It 'stops and restores the supervisor around a successful transaction' {
        & $lifecycle
        $script:applied | Should -BeTrue
        Should -Invoke Stop-Service -Exactly -Times 1 -ParameterFilter {$Name -eq 'k0sworker'}
        Should -Invoke Start-Service -Exactly -Times 1 -ParameterFilter {$Name -eq 'k0sworker'}
    }
    It 'restores the supervisor and preserves transaction failure' {
        $script:failApply=$true
        { & $lifecycle } | Should -Throw '*transaction failure*'
        Should -Invoke Start-Service -Exactly -Times 1
    }
    It 'does not apply when stopping the supervisor fails' {
        Mock Stop-Service {throw 'stop failed'}
        { & $lifecycle } | Should -Throw '*stop failed*'
        $script:applied | Should -BeFalse
        Should -Invoke Start-Service -Exactly -Times 1
    }
    It 'does not interpret failed process enumeration as a stopped kubelet' {
        Mock Get-CimInstance {throw 'process query failed'}
        { & $lifecycle } | Should -Throw '*process query failed*'
        $script:applied | Should -BeFalse
        Should -Invoke Start-Service -Exactly -Times 1
    }
}
