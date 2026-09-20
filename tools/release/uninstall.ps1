<#
  uninstall.ps1 —— 卸载脚本（安装目录里也有一份）

  用法：
    powershell -ExecutionPolicy Bypass -File uninstall.ps1

  默认卸载 $PSScriptRoot（脚本所在的那个安装目录），也可以用 -Dir 指定别处。
  会话记录、插件配置、日志属于用户数据，默认会问一句要不要连同目录一起删。

  本文件保存为 UTF-8 with BOM，原因同 install.ps1。
#>

param(
  [string]$Dir = ""
)

$ErrorActionPreference = "Stop"

function Get-AppName([string]$dir) {
  $f = Join-Path $dir "app-name.txt"
  if (Test-Path -LiteralPath $f) {
    $n = (Get-Content -LiteralPath $f -Raw -Encoding UTF8).Trim()
    if ($n) { return $n }
  }
  return "AI 工作平台"
}

if ([string]::IsNullOrWhiteSpace($Dir)) { $Dir = $PSScriptRoot }
$Dir = $Dir.Trim().Trim('"')

$appName = Get-AppName $Dir
Write-Host ""
Write-Host ("== 卸载 " + $appName + " ==")
Write-Host ("安装目录: " + $Dir)
Write-Host ""

# 1. 快捷方式
Write-Host "==> 删除快捷方式"
$desktop = Join-Path ([Environment]::GetFolderPath("Desktop")) ($appName + ".lnk")
$startm  = Join-Path (Join-Path ([Environment]::GetFolderPath("Programs")) $appName) ($appName + ".lnk")
foreach ($lnk in @($desktop, $startm)) {
  if (Test-Path -LiteralPath $lnk) {
    Remove-Item -LiteralPath $lnk -Force
    Write-Host ("  已删除: " + $lnk)
  }
}
$startDir = Join-Path ([Environment]::GetFolderPath("Programs")) $appName
if ((Test-Path -LiteralPath $startDir) -and (Get-ChildItem -LiteralPath $startDir -Force | Measure-Object).Count -eq 0) {
  Remove-Item -LiteralPath $startDir -Force
}

# 2. 安装目录
if (Test-Path -LiteralPath $Dir) {
  $ans = Read-Host "删除整个安装目录（含会话记录 / 配置 / 日志）？[y/N]"
  if ($ans -match '^(y|Y)') {
    Remove-Item -LiteralPath $Dir -Recurse -Force
    Write-Host ("  已删除: " + $Dir)
  } else {
    Write-Host ("  保留目录: " + $Dir)
  }
}

Write-Host ""
Write-Host "卸载完成。"
