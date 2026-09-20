<#
  install-shortcut.ps1

  Puts a shortcut to the launcher on the desktop (and optionally in the
  Start Menu).

  Why not drag runtime\core.exe to the desktop directly: a shortcut carries its
  own display name and icon. Pointing it at the launcher means the icon comes
  from the launcher's embedded resources, so replacing the artwork only needs a
  rebuild - the desktop shortcut is not touched.

  Usage:
    powershell -ExecutionPolicy Bypass -File tools\install-shortcut.ps1
    powershell -ExecutionPolicy Bypass -File tools\install-shortcut.ps1 -StartMenu

  NOTE: kept ASCII-only on purpose. Windows PowerShell 5.1 reads BOM-less
  scripts using the ANSI code page, and a non-ASCII byte pair can decode to a
  quote character, which breaks parsing. The product name comes from
  launcher/name.txt, which is UTF-8 and read with -Encoding UTF8.
#>

param(
  [string]$Root = "",
  [switch]$StartMenu
)

$ErrorActionPreference = "Stop"

if (-not $Root) {
  $Root = Split-Path -Parent $PSScriptRoot
}
$Root = (Resolve-Path -LiteralPath $Root).Path

$appName = (Get-Content -Raw -Encoding UTF8 (Join-Path $Root "launcher\name.txt")).Trim()
$target  = Join-Path $Root ($appName + ".exe")

if (-not (Test-Path -LiteralPath $target)) {
  throw ("not found: " + $target + "  -- run build.ps1 first")
}

function New-AppShortcut([string]$dir) {
  New-Item -ItemType Directory -Force -Path $dir | Out-Null
  $path = Join-Path $dir ($appName + ".lnk")

  $shell = New-Object -ComObject WScript.Shell
  $lnk = $shell.CreateShortcut($path)
  $lnk.TargetPath       = $target
  $lnk.WorkingDirectory = $Root
  $lnk.Description      = $appName
  $lnk.IconLocation     = $target + ",0"
  $lnk.WindowStyle      = 1        # 1 = normal window, 3 = maximized
  $lnk.Save()

  [void][Runtime.InteropServices.Marshal]::ReleaseComObject($shell)
  Write-Host ("  shortcut: " + $path)
}

New-AppShortcut ([Environment]::GetFolderPath("Desktop"))
if ($StartMenu) {
  New-AppShortcut (Join-Path ([Environment]::GetFolderPath("Programs")) $appName)
}
