# Installs the sourcelens binary for Windows (x86-64 or ARM64) from the latest
# GitHub release, into %LOCALAPPDATA%\sourcelens, and adds it to the user PATH:
#
#   irm https://raw.githubusercontent.com/bhagesh-h/sourcelens/main/install.ps1 | iex
$ErrorActionPreference = "Stop"
$arch = if ([System.Runtime.InteropServices.RuntimeInformation]::OSArchitecture -eq "Arm64") { "arm64" } else { "amd64" }
$url = "https://github.com/bhagesh-h/sourcelens/releases/latest/download/sourcelens_windows_$arch.exe"
$dir = Join-Path $env:LOCALAPPDATA "sourcelens"
New-Item -ItemType Directory -Force -Path $dir | Out-Null
Write-Host "downloading $url"
Invoke-WebRequest -Uri $url -OutFile (Join-Path $dir "sourcelens.exe")
$path = [Environment]::GetEnvironmentVariable("Path", "User")
if (-not ($path -split ";" | Where-Object { $_ -eq $dir })) {
    [Environment]::SetEnvironmentVariable("Path", "$path;$dir", "User")
    Write-Host "added $dir to your PATH; open a new terminal to use it"
}
& (Join-Path $dir "sourcelens.exe") version
