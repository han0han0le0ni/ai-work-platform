<#
  pack-release.ps1 —— 打一个可以发给别人的下载包

  用法:
    powershell -ExecutionPolicy Bypass -File tools\pack-release.ps1
    powershell -ExecutionPolicy Bypass -File tools\pack-release.ps1 -Version 1.1.0
    powershell -ExecutionPolicy Bypass -File tools\pack-release.ps1 -SkipBuild   # 用现有 runtime 产物直接打包

  产物: dist\ai-work-platform-<版本>.zip  (+ 同名 .sha256.txt)

  包里有什么:
    <产品名>.exe            启动器（带图标）
    runtime\core.exe        核心
    runtime\app.ico         图标
    runtime\plugins\*.plugin  白名单里的官方插件
    runtime\builtin\*.zip   内置界面包（格式化后能装回界面）
    install.ps1             安装脚本（选目录 + 建快捷方式）
    uninstall.ps1           卸载脚本
    安装说明.txt

  包里没有什么（打包脚本会硬校验，命中就报错中止）:
    core.json              含本机控制令牌 -> 不含，首次启动由核心现场生成随机令牌
    plugin-configs\*       含模型 API Key、会话记录、工作区路径 -> 不含
    logs\*                 运行日志 -> 不含
    任何非白名单插件        我本地装的第三方/试验插件不会被带走

  本文件保存为 UTF-8 with BOM，原因同 tools\release\install.ps1。
#>

param(
  [string]$Version   = "1.0.0",
  [string]$OutDir    = "dist",
  [switch]$SkipBuild
)

$ErrorActionPreference = "Stop"
$root = Split-Path -Parent $PSScriptRoot
Set-Location $root

$rt      = Join-Path $root "runtime"
$appName = (Get-Content -Raw -Encoding UTF8 (Join-Path $root "launcher\name.txt")).Trim()

# 与 build.ps1 / Makefile 的 GO_PLUGINS 保持一致：只有这些会被打进发布包
$plugins = @(
  "fs-tool", "llm", "tool-executor", "agent", "session", "conversation", "roleplay",
  "workbench", "plugin-manager", "workspace", "builder", "imagegen",
  "svgdraw", "usage-monitor"
)

# ---------------------------------------------------------------------------
# 0. 先构建，保证打包的是当前源码
# ---------------------------------------------------------------------------

if (-not $SkipBuild) {
  Write-Host "==> 构建（build.ps1）"
  & (Join-Path $root "build.ps1")
  if ($LASTEXITCODE -ne 0) { throw "构建失败" }
}

# ---------------------------------------------------------------------------
# 1. 搭一个干净的 stage
# ---------------------------------------------------------------------------

$stage = Join-Path $root (Join-Path $OutDir "_stage")
Remove-Item -LiteralPath $stage -Recurse -Force -ErrorAction SilentlyContinue
New-Item -ItemType Directory -Force -Path (Join-Path $stage "runtime\plugins") | Out-Null
New-Item -ItemType Directory -Force -Path (Join-Path $stage "runtime\builtin") | Out-Null

Write-Host "==> 收集文件"

$launcher = Join-Path $root ($appName + ".exe")
if (-not (Test-Path -LiteralPath $launcher)) { throw "缺少启动器: $launcher" }
Copy-Item -LiteralPath $launcher -Destination $stage -Force

foreach ($f in @("core.exe", "app.ico")) {
  $p = Join-Path $rt $f
  if (-not (Test-Path -LiteralPath $p)) { throw "缺少 $f，先跑一次 build.ps1" }
  Copy-Item -LiteralPath $p -Destination (Join-Path $stage "runtime") -Force
}

Get-ChildItem -LiteralPath (Join-Path $rt "builtin") -Filter "*.zip" -File |
  ForEach-Object { Copy-Item -LiteralPath $_.FullName -Destination (Join-Path $stage "runtime\builtin") -Force }

foreach ($p in $plugins) {
  $dir = Join-Path $rt ("plugins\" + $p + ".plugin")
  if (-not (Test-Path -LiteralPath $dir)) {
    Write-Warning ("插件缺失，跳过: " + $p)
    continue
  }
  Copy-Item -LiteralPath $dir -Destination (Join-Path $stage "runtime\plugins") -Recurse -Force
}

foreach ($f in @("install.ps1", "uninstall.ps1")) {
  $p = Join-Path $root ("tools\release\" + $f)
  if (-not (Test-Path -LiteralPath $p)) { throw "缺少 tools\release\$f" }
  Copy-Item -LiteralPath $p -Destination $stage -Force
}

# 产品名（UTF-8 无 BOM 即可，安装脚本按 UTF8 读）
[System.IO.File]::WriteAllText(
  (Join-Path $stage "app-name.txt"), $appName,
  (New-Object System.Text.UTF8Encoding($false)))

$readme = @"
$appName 安装说明
========================================

1. 把整个压缩包解压到任意位置（桌面、下载目录都行）。
2. 右键 install.ps1 -> "使用 PowerShell 运行"。
   若提示脚本被禁止运行，用这条命令：
       powershell -ExecutionPolicy Bypass -File install.ps1
3. 弹出的窗口里选安装到哪（C 盘、D 盘、E 盘都行，也可以"新建文件夹"）。
4. 装完会自动在桌面和开始菜单建快捷方式，双击图标即可启动。

卸载：运行安装目录里的 uninstall.ps1。

隐私说明
----------------------------------------
本包不含任何个人信息：没有会话/聊天记录，没有插件配置，
没有模型 API Key，没有控制令牌。
- 控制令牌（core.json）在你首次启动时现场随机生成，只属于你这台机器。
- 模型的 API Key 需要你自己填：启动后在界面里配置，
  或者手动写 <安装目录>\runtime\plugin-configs\com.official.llm.json。

目录结构
----------------------------------------
<安装目录>\
  $appName.exe        启动器（双击这个）
  runtime\
    core.exe          核心
    plugins\          插件
    builtin\          内置界面包（格式化后可装回）
    plugin-configs\   插件配置（首次运行后才有）
    logs\             日志（首次运行后才有）
"@
[System.IO.File]::WriteAllText(
  (Join-Path $stage "安装说明.txt"), $readme,
  (New-Object System.Text.UTF8Encoding($false)))

# ---------------------------------------------------------------------------
# 2. 隐私校验：命中任何一条就直接中止，不许出包
# ---------------------------------------------------------------------------

Write-Host "==> 隐私校验"

$problems = @()

# 2.1 不该出现的目录 / 文件
foreach ($bad in @("core.json", "plugin-configs", "logs", "pending-delete")) {
  $p = Join-Path (Join-Path $stage "runtime") $bad
  if (Test-Path -LiteralPath $p) { $problems += ("包内混入了 " + $bad) }
}

# 2.2 本机控制令牌
$token = ""
$coreJson = Join-Path $rt "core.json"
if (Test-Path -LiteralPath $coreJson) {
  try {
    $cfg = Get-Content -LiteralPath $coreJson -Raw -Encoding UTF8 | ConvertFrom-Json
    if ($cfg.controlToken) { $token = $cfg.controlToken }
  } catch { }
}

# 2.3 扫描包内所有文本文件
$patterns = @()
$patterns += [regex]::Escape($root)          # 我的仓库绝对路径
$patterns += "万物皆插件"                      # 仓库目录名
$patterns += '"controlToken"\s*:\s*"[^"]+"'  # 任何控制令牌
$patterns += '"apiKey"\s*:\s*"[^"]+"'        # 任何模型密钥
$patterns += '"root"\s*:\s*"[^"]+"'          # 工作区路径
if ($token) { $patterns += [regex]::Escape($token) }

# 只扫文本：exe 里天然带 "apiKey" 这类字段名字面量，扫它只会全是误报。
$textExt = @(".json", ".txt", ".ps1", ".md", ".py", ".csv", ".yml", ".yaml", ".xml", ".ini")
Get-ChildItem -LiteralPath $stage -Recurse -File |
  Where-Object { $textExt -contains $_.Extension.ToLowerInvariant() } |
  ForEach-Object {
    $text = Get-Content -LiteralPath $_.FullName -Raw -Encoding UTF8 -ErrorAction SilentlyContinue
    if (-not $text) { return }
    foreach ($pat in $patterns) {
      if ($text -match $pat) {
        $problems += ($_.FullName.Substring($stage.Length) + " 命中: " + $pat)
      }
    }
  }

# 2.4 二进制里残留的本机路径（加了 -trimpath 之后不该再有）
# 字节比较走 Latin1：每个字节一个字符，IndexOf 是原生搜索，比逐字节循环快得多
$latin1 = [System.Text.Encoding]::GetEncoding("iso-8859-1")
$needles = @(
  $latin1.GetString([System.Text.Encoding]::UTF8.GetBytes($root)),
  $latin1.GetString([System.Text.Encoding]::Unicode.GetBytes($root))
)
Get-ChildItem -LiteralPath $stage -Recurse -File -Filter "*.exe" | ForEach-Object {
  $blob = $latin1.GetString([System.IO.File]::ReadAllBytes($_.FullName))
  foreach ($needle in $needles) {
    if ($blob.IndexOf($needle, [StringComparison]::Ordinal) -ge 0) {
      $problems += ($_.Name + " 里残留本机路径（构建时缺 -trimpath）")
      break
    }
  }
}

if ($problems.Count -gt 0) {
  Write-Host ""
  Write-Host "!! 发布包校验失败，已中止（未生成 zip）:" -ForegroundColor Red
  $problems | ForEach-Object { Write-Host ("   - " + $_) -ForegroundColor Red }
  Write-Host ""
  throw "发布包含有本机私人数据"
}

Write-Host "  通过：无令牌、无密钥、无会话记录、无本机路径"

# ---------------------------------------------------------------------------
# 3. 压缩
# ---------------------------------------------------------------------------

New-Item -ItemType Directory -Force -Path (Join-Path $root $OutDir) | Out-Null
$zipName = "ai-work-platform-" + $Version + ".zip"
$zip     = Join-Path (Join-Path $root $OutDir) $zipName
Remove-Item -LiteralPath $zip -Force -ErrorAction SilentlyContinue

Write-Host ("==> 压缩 -> " + $zip)
Compress-Archive -Path (Join-Path $stage "*") -DestinationPath $zip -Force

$hash = (Get-FileHash -LiteralPath $zip -Algorithm SHA256).Hash
$hashFile = $zip + ".sha256.txt"
[System.IO.File]::WriteAllText($hashFile, ($hash + "  " + $zipName),
  (New-Object System.Text.UTF8Encoding($false)))

Remove-Item -LiteralPath $stage -Recurse -Force

$size = [math]::Round((Get-Item -LiteralPath $zip).Length / 1MB, 1)
Write-Host ""
Write-Host ("发布包: " + $zip + "  (" + $size + " MB)")
Write-Host ("校验值: " + $hashFile)
Write-Host ""
Write-Host "发出去之前建议自己先试装一次：解压到别处，跑 install.ps1 选个临时目录。"
