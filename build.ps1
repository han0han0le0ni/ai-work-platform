# AI Work Platform Core - Windows build script
# Usage: powershell -ExecutionPolicy Bypass -File build.ps1
#
# NOTE: kept ASCII-only on purpose: Windows PowerShell 5.1 reads BOM-less
# scripts using the ANSI code page, which corrupts non-ASCII characters.

$ErrorActionPreference = "Stop"
$root = $PSScriptRoot
Set-Location $root

$rt  = Join-Path $root "runtime"
$bin = ".exe"

Write-Host "==> preparing runtime/"
foreach ($d in @("plugins", "plugin-configs", "logs", "pending-delete", "builtin")) {
  New-Item -ItemType Directory -Force -Path (Join-Path $rt $d) | Out-Null
}

# ---------------------------------------------------------------------------
# Go: core + go plugins
# ---------------------------------------------------------------------------

Write-Host "==> building core"
# -trimpath: keep the absolute build path out of the binaries, so a released
# package does not carry the developer's directory layout.
go build -trimpath -o (Join-Path $rt ("core" + $bin)) ./core

function Build-GoPlugin($name) {
  $dir = Join-Path $rt ("plugins\" + $name + ".plugin")
  New-Item -ItemType Directory -Force -Path $dir | Out-Null
  Write-Host ("==> building go plugin: " + $name)
  go build -trimpath -o (Join-Path $dir ("main" + $bin)) ("./plugins/" + $name)
  Copy-Item (Join-Path $root ("plugins\" + $name + "\manifest.json")) (Join-Path $dir "manifest.json") -Force
  return $dir
}

# 文件工具：AI 读写文件的能力来源
Build-GoPlugin "fs-tool" | Out-Null

# AI 场景四件套：模型 -> 工具目录 -> 编排 -> 会话
Build-GoPlugin "llm" | Out-Null
Build-GoPlugin "tool-executor" | Out-Null
Build-GoPlugin "agent" | Out-Null
Build-GoPlugin "session" | Out-Null

# 链式对话：消息树 / 分支版本 / 多会话（自建插件）
Build-GoPlugin "conversation" | Out-Null

# 角色扮演：角色卡 / 我的身份 / 长期记忆（拼成系统提示词推给 agent）
Build-GoPlugin "roleplay" | Out-Null

# Plugin lifecycle: list / inspect / delete plugins (delete also purges config + log).
Build-GoPlugin "plugin-manager" | Out-Null

# Image generation: text prompt in, saved image path out (kept separate from llm).
Build-GoPlugin "imagegen" | Out-Null

# Vector drawing: the text model writes SVG, no external image service needed.
Build-GoPlugin "svgdraw" | Out-Null

# Workspace files: pick a landing folder for relative paths; absolute paths reach the whole disk.
Build-GoPlugin "workspace" | Out-Null

# Builder: compiles AI-written Go plugin sources (the only plugin that may spawn).
Build-GoPlugin "builder" | Out-Null

# Usage monitor: DeepSeek balance + token/cache-hit overlay (reads llm.usage events).
Build-GoPlugin "usage-monitor" | Out-Null

# Default UI: the clickable workbench.
# minimal-ui also provides ui.v1; installing both would make them fight over the
# terminal (whoever registers first wins), so only workbench goes into plugins/.
# minimal-ui is kept as the fallback UI packaged under builtin/ instead.
Build-GoPlugin "workbench" | Out-Null

# ---------------------------------------------------------------------------
# 内置官方 UI 包：产物只落在 runtime/builtin/
# ---------------------------------------------------------------------------

Write-Host "==> packaging builtin UI packages -> runtime/builtin/"

# 两个 UI 都打进 builtin：格式化后引导菜单能装回保底界面，
# 也能用 install <路径> 把工作台装回来。
function Pack-BuiltinUI($name) {
  $stage = Join-Path $rt ("builtin\_stage_" + $name)
  Remove-Item $stage -Recurse -Force -ErrorAction SilentlyContinue
  New-Item -ItemType Directory -Force -Path $stage | Out-Null
  Write-Host ("==> building builtin UI: " + $name)
  go build -trimpath -o (Join-Path $stage ("main" + $bin)) ("./plugins/" + $name)
  Copy-Item (Join-Path $root ("plugins\" + $name + "\manifest.json")) $stage -Force
  $f = Join-Path $rt ("builtin\" + $name + ".zip")
  Remove-Item $f -ErrorAction SilentlyContinue
  Compress-Archive -Path (Join-Path $stage "*") -DestinationPath $f -Force
  Remove-Item $stage -Recurse -Force
  return $f
}

$zip     = Pack-BuiltinUI "minimal-ui"
$wbZip   = Pack-BuiltinUI "workbench"

# ---------------------------------------------------------------------------
# Icon + launcher
# ---------------------------------------------------------------------------
# The core hides in runtime/, which is a bad place to double-click. So the repo
# root gets a launcher named after the product, carrying the icon: it only opens
# the console, starts the core, and forwards the exit code.
#
# Icon and version info are stamped into both exes. Go's linker does not write a
# PE .rsrc section, so without this the binaries show a blank icon in Explorer
# and empty Properties -> Details. tools/set-windows-resources.ps1 does it with
# the UpdateResource API that ships with Windows - no third-party tool needed.

Write-Host "==> building icon + launcher"

$ico = Join-Path $rt "app.ico"
# Product name lives in name.txt, not here: this script stays ASCII-only.
$appName = (Get-Content -Raw -Encoding UTF8 (Join-Path $root "launcher\name.txt")).Trim()
$exePath = Join-Path $root ($appName + $bin)

go run ./tools/icon2ico -in (Join-Path $root "launcher\assets\appicon.png") -out $ico
go build -trimpath -o $exePath ./launcher

try {
  # Launcher first: it is never running while you build, so the entry point
  # always gets its icon even when the platform itself is still open.
  & (Join-Path $root "tools\set-windows-resources.ps1") `
      -Exe $exePath, (Join-Path $rt ("core" + $bin)) `
      -Ico $ico -ProductName $appName -CompanyName "AI Work Platform" -FileVersion "1.0.0.0"
} catch {
  # Belt and braces: the script already skips locked files by itself.
  Write-Warning ("resources not written: " + $_.Exception.Message)
}

Write-Host ""
Write-Host "build done."
Write-Host ("  launcher: " + $exePath)
Write-Host "            ^^ double-click this"
Write-Host ("  core    : " + $rt + "\core" + $bin)
Write-Host ("  plugins : " + $rt + "\plugins\")
Get-ChildItem (Join-Path $rt "plugins") -Directory | ForEach-Object { Write-Host ("            " + $_.Name) }
Write-Host ("  builtin : " + $zip)
Write-Host ("            " + $wbZip)
Write-Host ""
Write-Host ("run     : " + $exePath + "   (or " + $rt + "\core" + $bin + " directly)")
Write-Host "format  : POST http://127.0.0.1:9123/format  (header X-Format-Token from runtime/core.json)"
Write-Host "shortcut: powershell -ExecutionPolicy Bypass -File tools\install-shortcut.ps1"
