<#
.SYNOPSIS
  qwenchik for Windows: on-demand launcher (no autostart).

.DESCRIPTION
  Windows equivalent of scripts/qwenchik. Runs llama-server.exe and qagent.exe
  as ordinary background processes, only while you need them.

    .\scripts\qwenchik.ps1 setup -ModelBin C:\llama\llama-server.exe -ModelPath C:\models\Qwen3.5-4B-Q4_K_M.gguf [-Device Vulkan0] [-Context 32768]
    .\scripts\qwenchik.ps1 build             # builds qagent.exe (needs Go)
    .\scripts\qwenchik.ps1 start [-Search] [-NoOpen]
    .\scripts\qwenchik.ps1 stop
    .\scripts\qwenchik.ps1 status
    .\scripts\qwenchik.ps1 open
    .\scripts\qwenchik.ps1 logs [model|app]

  setup saves the paths to %APPDATA%\qwenchik\config.json and creates
  "Qwenchik" and "Qwenchik - stop" shortcuts on the desktop.
  Works in Windows PowerShell 5.1 and PowerShell 7+.
#>
[CmdletBinding()]
param(
  [Parameter(Position = 0)]
  [ValidateSet('setup', 'build', 'start', 'stop', 'status', 'open', 'logs', 'help')]
  [string]$Command = 'help',
  [Parameter(Position = 1)]
  [string]$Target = 'app',
  [string]$ModelBin,
  [string]$ModelPath,
  [string]$Device = 'Vulkan0',
  [int]$Context = 32768,
  [string]$ModelAlias = 'local-qwen',
  [switch]$Search,
  [switch]$NoOpen,
  [switch]$Quiet
)

Set-StrictMode -Version 3
$ErrorActionPreference = 'Stop'

$ProjectDir = Split-Path -Parent $PSScriptRoot
$ConfigDir = Join-Path $env:APPDATA 'qwenchik'
$StateDir = Join-Path $env:LOCALAPPDATA 'qwenchik'
$ConfigFile = Join-Path $ConfigDir 'config.json'
$StateFile = Join-Path $StateDir 'processes.json'
$SearxngEnv = Join-Path $ConfigDir 'searxng.env'
$DockerMark = Join-Path $StateDir 'started-docker-desktop'
$AppExe = Join-Path $ProjectDir 'qagent.exe'
$AppUrl = 'http://127.0.0.1:8080'
$ModelHealth = 'http://127.0.0.1:8000/health'
$SearxngUrl = 'http://127.0.0.1:8081'

# Keep in sync with runtimeModelPolicyArgs in internal/cli/runtime.go
# (cache + scheduling + reasoning flags). CI checks that these values match.
$ModelPolicyArgs = @('--cache-ram', '512', '--ctx-checkpoints', '4',
  '--threads', '4', '--threads-batch', '4', '--poll', '0', '--poll-batch', '0',
  '--reasoning', 'off', '--no-webui')

function Say([string]$Message) { if (-not $Quiet) { Write-Host "> $Message" } }

function Fail([string]$Message) {
  if ($Quiet) {
    # Started from a hidden shortcut: show the error in a dialog instead of a console.
    try {
      Add-Type -AssemblyName System.Windows.Forms
      [void][System.Windows.Forms.MessageBox]::Show($Message, 'Qwenchik', 'OK', 'Error')
    } catch { }
  }
  throw $Message
}

function Quote-Arg([string]$Value) {
  # Start-Process joins ArgumentList with spaces; quote arguments that need it.
  if ($Value -match '[\s"]') { return '"' + ($Value -replace '"', '\"') + '"' }
  return $Value
}

function Wait-Url([string]$Url, [int]$Seconds) {
  $deadline = (Get-Date).AddSeconds($Seconds)
  while ((Get-Date) -lt $deadline) {
    try {
      $response = Invoke-WebRequest -Uri $Url -UseBasicParsing -TimeoutSec 2
      if ($response.StatusCode -lt 400) { return $true }
    } catch { }
    Start-Sleep -Seconds 1
  }
  return $false
}

function Read-Config {
  if (-not (Test-Path $ConfigFile)) { Fail "Run setup first: .\scripts\qwenchik.ps1 setup -ModelBin <llama-server.exe> -ModelPath <model.gguf>" }
  return Get-Content $ConfigFile -Raw | ConvertFrom-Json
}

function Read-State {
  if (Test-Path $StateFile) { return Get-Content $StateFile -Raw | ConvertFrom-Json }
  return [pscustomobject]@{ model = 0; app = 0 }
}

function Write-State($State) {
  New-Item -ItemType Directory -Force -Path $StateDir | Out-Null
  $State | ConvertTo-Json | Set-Content -Path $StateFile -Encoding UTF8
}

function Get-Tracked([int]$ProcessId, [string]$Name) {
  # Only return the process if the PID still belongs to the program we started.
  if ($ProcessId -le 0) { return $null }
  $process = Get-Process -Id $ProcessId -ErrorAction SilentlyContinue
  if ($process -and $process.ProcessName -like "$Name*") { return $process }
  return $null
}

function Test-DockerReady {
  if (-not (Get-Command docker -ErrorAction SilentlyContinue)) { return $false }
  & docker info *> $null
  return ($LASTEXITCODE -eq 0)
}

function Invoke-SearchCompose([string[]]$ComposeArgs) {
  & docker compose -p qwenchik-search -f (Join-Path $ProjectDir 'compose.searxng.yml') --env-file $SearxngEnv @ComposeArgs
}

function Start-Search {
  if (-not (Get-Command docker -ErrorAction SilentlyContinue)) { Fail 'Docker is not installed (needed only for -Search)' }
  New-Item -ItemType Directory -Force -Path $ConfigDir, $StateDir | Out-Null
  if (-not (Test-Path $SearxngEnv)) {
    $bytes = New-Object byte[] 32
    [System.Security.Cryptography.RandomNumberGenerator]::Create().GetBytes($bytes)
    $secret = -join ($bytes | ForEach-Object { $_.ToString('x2') })
    Set-Content -Path $SearxngEnv -Value "SEARXNG_SECRET=$secret" -Encoding ASCII
  }
  if (-not (Test-DockerReady)) {
    $desktop = Join-Path $env:ProgramFiles 'Docker\Docker\Docker Desktop.exe'
    if (-not (Test-Path $desktop)) { Fail "Docker Desktop not found: $desktop" }
    Say 'Starting Docker Desktop (only for SearXNG)...'
    Start-Process -FilePath $desktop | Out-Null
    New-Item -ItemType File -Force -Path $DockerMark | Out-Null
    $deadline = (Get-Date).AddMinutes(3)
    while (-not (Test-DockerReady)) {
      if ((Get-Date) -gt $deadline) { Fail 'Docker Desktop did not start within 3 minutes' }
      Start-Sleep -Seconds 3
    }
  }
  Say 'Starting SearXNG...'
  Invoke-SearchCompose @('up', '-d') | Out-Null
  if (-not (Wait-Url $SearxngUrl 60)) { Say 'SearXNG is not responding yet; search falls back to other providers' }
}

function Stop-Search {
  if ((Test-DockerReady) -and (Test-Path $SearxngEnv)) {
    try { Invoke-SearchCompose @('down') *> $null } catch { }
  }
  if (Test-Path $DockerMark) {
    Say 'Stopping Docker Desktop...'
    & docker desktop stop *> $null
    if ($LASTEXITCODE -ne 0) {
      Get-Process -Name 'Docker Desktop' -ErrorAction SilentlyContinue | Stop-Process -Force
    }
    Remove-Item $DockerMark -Force
  }
}

function Invoke-Setup {
  if (-not $ModelBin -or -not $ModelPath) { Fail 'setup requires -ModelBin and -ModelPath' }
  foreach ($path in @($ModelBin, $ModelPath)) {
    if (-not (Test-Path -PathType Leaf $path)) { Fail "File not found: $path" }
  }
  New-Item -ItemType Directory -Force -Path $ConfigDir | Out-Null
  [pscustomobject]@{
    modelBin   = (Resolve-Path $ModelBin).Path
    modelPath  = (Resolve-Path $ModelPath).Path
    device     = $Device
    context    = $Context
    modelAlias = $ModelAlias
  } | ConvertTo-Json | Set-Content -Path $ConfigFile -Encoding UTF8
  Say "Config saved: $ConfigFile"

  $shell = New-Object -ComObject WScript.Shell
  $desktopDir = [Environment]::GetFolderPath('Desktop')
  $ps = Join-Path $PSHOME 'powershell.exe'
  if (-not (Test-Path $ps)) { $ps = Join-Path $PSHOME 'pwsh.exe' }
  foreach ($item in @(
      @{ Name = 'Qwenchik'; Args = 'start -Quiet'; Icon = 'imageres.dll,76' },
      @{ Name = 'Qwenchik - stop'; Args = 'stop -Quiet'; Icon = 'imageres.dll,93' })) {
    $link = $shell.CreateShortcut((Join-Path $desktopDir ($item.Name + '.lnk')))
    $link.TargetPath = $ps
    $link.Arguments = "-NoProfile -ExecutionPolicy Bypass -WindowStyle Hidden -File `"$PSCommandPath`" $($item.Args)"
    $link.WorkingDirectory = $ProjectDir
    $link.IconLocation = Join-Path $env:SystemRoot ('System32\' + $item.Icon)
    $link.Save()
  }
  Say 'Desktop shortcuts created: Qwenchik, Qwenchik - stop'
  if (-not (Test-Path $AppExe)) { Say 'qagent.exe not found yet: run ".\scripts\qwenchik.ps1 build"' }
}

function Invoke-Build {
  if (-not (Get-Command go -ErrorAction SilentlyContinue)) { Fail 'Go is not installed: https://go.dev/dl/' }
  Say 'Building qagent.exe (embedded UI from internal/api/web/dist)...'
  Push-Location $ProjectDir
  try {
    & go build -tags sqlite_fts5 -o $AppExe ./cmd/qagent
    if ($LASTEXITCODE -ne 0) { Fail 'go build failed' }
  } finally { Pop-Location }
  Say "Done: $AppExe"
}

function Invoke-Start {
  $config = Read-Config
  if (-not (Test-Path $AppExe)) { Fail "qagent.exe not found: run .\scripts\qwenchik.ps1 build" }
  New-Item -ItemType Directory -Force -Path $StateDir | Out-Null
  $state = Read-State
  if ($Search) { Start-Search }

  if (-not (Get-Tracked $state.model 'llama-server')) {
    Say 'Starting model...'
    $modelArgs = @('-m', $config.modelPath, '--device', $config.device, '-ngl', '99',
      '--host', '127.0.0.1', '--port', '8000', '--alias', $config.modelAlias,
      '--jinja', '--parallel', '1', '-c', [string]$config.context,
      '-fa', 'on', '-ctk', 'q8_0', '-ctv', 'q8_0') + $ModelPolicyArgs
    $model = Start-Process -FilePath $config.modelBin -ArgumentList ($modelArgs | ForEach-Object { Quote-Arg $_ }) `
      -WindowStyle Hidden -PassThru `
      -RedirectStandardOutput (Join-Path $StateDir 'model.out.log') -RedirectStandardError (Join-Path $StateDir 'model.log')
    $state.model = $model.Id
    Write-State $state
  }
  if (-not (Wait-Url $ModelHealth 180)) { Fail 'The model did not respond within 3 minutes: .\scripts\qwenchik.ps1 logs model' }

  if (-not (Get-Tracked $state.app 'qagent')) {
    Say 'Starting app...'
    # Same environment as the Linux systemd app unit; .env in the project dir is read by qagent itself.
    $env:LLM_CONTEXT_LENGTH = [string]$config.context
    $env:SEARCH_ENABLE_DDG = 'false'
    $app = Start-Process -FilePath $AppExe -ArgumentList 'serve' -WorkingDirectory $ProjectDir `
      -WindowStyle Hidden -PassThru `
      -RedirectStandardOutput (Join-Path $StateDir 'app.out.log') -RedirectStandardError (Join-Path $StateDir 'app.log')
    $state.app = $app.Id
    Write-State $state
  }
  if (-not (Wait-Url "$AppUrl/api/health" 60)) { Fail 'The app did not respond: .\scripts\qwenchik.ps1 logs app' }
  Say "Ready: $AppUrl"
  if (-not $NoOpen) { Start-Process $AppUrl | Out-Null }
}

function Invoke-Stop {
  $state = Read-State
  Say 'Stopping app and model...'
  foreach ($entry in @(@{ Id = $state.app; Name = 'qagent' }, @{ Id = $state.model; Name = 'llama-server' })) {
    $process = Get-Tracked $entry.Id $entry.Name
    if ($process) {
      Stop-Process -Id $process.Id
      if (-not $process.WaitForExit(30000)) { Stop-Process -Id $process.Id -Force }
    }
  }
  Write-State ([pscustomobject]@{ model = 0; app = 0 })
  Stop-Search
  Say 'Everything stopped; RAM and VRAM are free.'
}

function Invoke-Status {
  $state = Read-State
  $rows = @(
    @{ Name = 'model'; Up = [bool](Get-Tracked $state.model 'llama-server') },
    @{ Name = 'app'; Up = [bool](Get-Tracked $state.app 'qagent') },
    @{ Name = 'docker'; Up = (Test-DockerReady) })
  foreach ($row in $rows) {
    $label = if ($row.Up) { 'running' } else { 'stopped' }
    Write-Host ('  {0,-8} {1}' -f $row.Name, $label)
  }
}

function Invoke-Logs {
  $file = switch ($Target) { 'model' { 'model.log' } 'app' { 'app.log' } default { Fail 'logs model|app' } }
  $path = Join-Path $StateDir $file
  if (-not (Test-Path $path)) { Fail "No log yet: $path" }
  Get-Content -Path $path -Tail 80 -Wait
}

switch ($Command) {
  'setup' { Invoke-Setup }
  'build' { Invoke-Build }
  'start' { Invoke-Start }
  'stop' { Invoke-Stop }
  'status' { Invoke-Status }
  'open' { Start-Process $AppUrl | Out-Null }
  'logs' { Invoke-Logs }
  default { Get-Help $PSCommandPath -Detailed }
}
