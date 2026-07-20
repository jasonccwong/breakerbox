# BreakerBox agent installer for Windows.
# Run from an elevated PowerShell:
#   irm https://raw.githubusercontent.com/jasonccwong/breakerbox/main/scripts/install-agent.ps1 -OutFile install-agent.ps1
#   .\install-agent.ps1 -Hub https://your-hub -Token <ENROLL_TOKEN>
#
# Installs the agent to Program Files, enrolls it with your hub, and registers
# a Windows Service (auto-start). State lives in %ProgramData%\breakerbox-agent.

[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)][string]$Hub,
    [Parameter(Mandatory = $true)][string]$Token,
    [string]$Name = $env:COMPUTERNAME,
    [string]$Version = "latest"
)

$ErrorActionPreference = "Stop"
$serviceName = "breakerbox-agent"
$installDir = Join-Path $env:ProgramFiles "BreakerBox"
$exePath = Join-Path $installDir "breakerbox-agent.exe"

if (-not ([Security.Principal.WindowsPrincipal][Security.Principal.WindowsIdentity]::GetCurrent()
        ).IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)) {
    Write-Error "Run this script from an elevated (Administrator) PowerShell."
}

$arch = if ([Environment]::Is64BitOperatingSystem) {
    if ($env:PROCESSOR_ARCHITECTURE -eq "ARM64") { "arm64" } else { "amd64" }
} else {
    Write-Error "32-bit Windows is not supported."
}

# Resolve release tag.
$repo = "jasonccwong/breakerbox"
if ($Version -eq "latest") {
    $release = Invoke-RestMethod "https://api.github.com/repos/$repo/releases/latest"
    $tag = $release.tag_name
} else {
    $tag = $Version
}
$ver = $tag.TrimStart("v")
$asset = "breakerbox-agent_${ver}_windows_${arch}.tar.gz"
$url = "https://github.com/$repo/releases/download/$tag/$asset"

Write-Host "Downloading $asset ($tag)..."
$tmp = Join-Path $env:TEMP "breakerbox-install"
New-Item -ItemType Directory -Force -Path $tmp | Out-Null
$archive = Join-Path $tmp $asset
Invoke-WebRequest -Uri $url -OutFile $archive

Write-Host "Installing to $installDir..."
New-Item -ItemType Directory -Force -Path $installDir | Out-Null
# Stop an existing service before replacing the binary.
if (Get-Service $serviceName -ErrorAction SilentlyContinue) {
    Stop-Service $serviceName -Force -ErrorAction SilentlyContinue
}
tar -xzf $archive -C $tmp
Copy-Item (Join-Path $tmp "breakerbox-agent.exe") $exePath -Force

Write-Host "Enrolling with $Hub..."
& $exePath enroll --hub $Hub --token $Token --name $Name
if ($LASTEXITCODE -ne 0) { Write-Error "Enrollment failed." }

if (-not (Get-Service $serviceName -ErrorAction SilentlyContinue)) {
    Write-Host "Registering Windows Service..."
    New-Service -Name $serviceName -BinaryPathName "`"$exePath`" run" `
        -DisplayName "BreakerBox Agent" -StartupType Automatic `
        -Description "BreakerBox app control panel agent" | Out-Null
    # Restart on failure (5s delay), reset failure count daily.
    sc.exe failure $serviceName reset= 86400 actions= restart/5000/restart/5000/restart/5000 | Out-Null
}
Start-Service $serviceName

Remove-Item -Recurse -Force $tmp
Write-Host ""
Write-Host "BreakerBox agent installed and running as a Windows Service."
Write-Host "State dir: $env:ProgramData\breakerbox-agent"
Write-Host "Logs:      Windows Event Viewer -> Application -> breakerbox-agent"
Write-Host ""
Write-Host "Note: supervised apps run in session 0 (no visible windows);"
Write-Host "BreakerBox targets server-style workloads on Windows."
