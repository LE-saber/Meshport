[CmdletBinding()]
param(
    [string]$Server = $env:MESHPORT_SSH_SERVER,
    [ValidateRange(1024, 65535)][int]$LocalPort = 18080,
    [ValidateRange(1, 65535)][int]$RemotePort = 18080,
    [switch]$Stop,
    [switch]$NoBrowser
)

$ErrorActionPreference = 'Stop'
if ([string]::IsNullOrWhiteSpace($Server)) {
    throw 'Specify -Server user@host, or set the MESHPORT_SSH_SERVER environment variable.'
}
$stateDir = Join-Path $env:LOCALAPPDATA 'Meshport\tunnels'
New-Item -ItemType Directory -Force -Path $stateDir | Out-Null
$stateFile = Join-Path $stateDir "$LocalPort.json"
$errorLog = Join-Path $stateDir "$LocalPort.stderr.log"
$outputLog = Join-Path $stateDir "$LocalPort.stdout.log"
$url = "http://127.0.0.1:$LocalPort"
$tunnel = $null

if (Test-Path -LiteralPath $stateFile) {
    $state = Get-Content -LiteralPath $stateFile -Raw | ConvertFrom-Json
    $candidate = Get-Process -Id $state.ProcessId -ErrorAction SilentlyContinue
    if ($candidate -and $candidate.ProcessName -eq 'ssh' -and
        $candidate.StartTime.ToUniversalTime().Ticks.ToString() -eq $state.StartTicks) {
        if ($state.Server -ne $Server -or $state.RemotePort -ne $RemotePort) {
            throw "Port $LocalPort belongs to a different saved tunnel."
        }
        $tunnel = $candidate
    } else {
        Remove-Item -LiteralPath $stateFile
    }
}

if ($Stop) {
    if ($tunnel) {
        Stop-Process -Id $tunnel.Id
        Remove-Item -LiteralPath $stateFile
        Write-Host "Meshport SSH tunnel closed (127.0.0.1:$LocalPort)."
    } else {
        Write-Host 'No running Meshport SSH tunnel was found.'
    }
    exit 0
}

if (-not $tunnel) {
    $probe = [Net.Sockets.TcpListener]::new([Net.IPAddress]::Loopback, $LocalPort)
    try { $probe.Start() } catch { throw "Local port $LocalPort is occupied. Use -LocalPort with another port." }
    finally { $probe.Stop() }

    $sshPath = (Get-Command ssh.exe -ErrorAction Stop).Source
    $sshArgs = @('-N', '-T', '-o', 'BatchMode=yes', '-o', 'ExitOnForwardFailure=yes',
        '-o', 'ConnectTimeout=15', '-o', 'ServerAliveInterval=30', '-o', 'ServerAliveCountMax=3',
        '-L', "127.0.0.1:${LocalPort}:127.0.0.1:${RemotePort}", $Server)
    $tunnel = Start-Process -FilePath $sshPath -ArgumentList $sshArgs -WindowStyle Hidden `
        -RedirectStandardError $errorLog -RedirectStandardOutput $outputLog -PassThru
    @{
        ProcessId = $tunnel.Id
        StartTicks = $tunnel.StartTime.ToUniversalTime().Ticks.ToString()
        Server = $Server
        RemotePort = $RemotePort
    } | ConvertTo-Json | Set-Content -LiteralPath $stateFile -Encoding UTF8
}

$ready = $false
for ($attempt = 0; $attempt -lt 40; $attempt++) {
    $tunnel.Refresh()
    if ($tunnel.HasExited) { break }
    try {
        $health = Invoke-RestMethod "$url/healthz" -TimeoutSec 2
        if ($health.status -eq 'ok' -and $health.version) { $ready = $true; break }
    } catch { }
    Start-Sleep -Milliseconds 250
}
if (-not $ready) {
    if (-not $tunnel.HasExited) { Stop-Process -Id $tunnel.Id -ErrorAction SilentlyContinue }
    Remove-Item -LiteralPath $stateFile -ErrorAction SilentlyContinue
    $detail = if (Test-Path $errorLog) { Get-Content $errorLog -Raw } else { '' }
    throw "Meshport did not respond through the SSH tunnel. $detail"
}

Write-Host "Meshport $($health.version): $url/"
Write-Host "API reference: $url/api/docs"
Write-Host 'The tunnel runs in the background. Run Close-Meshport.cmd to stop it.'
if (-not $NoBrowser) { Start-Process "$url/" }
