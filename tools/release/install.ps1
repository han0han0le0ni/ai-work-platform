<#
  install.ps1 —— 安装脚本（随发布包一起分发）

  用法：
    在解压出来的文件夹里，右键 install.ps1 -> "使用 PowerShell 运行"
    或： powershell -ExecutionPolicy Bypass -File install.ps1
    无人值守：
         powershell -ExecutionPolicy Bypass -File install.ps1 -Dir "D:\AI-work-platform"

  参数：
    -Dir <路径>        直接指定安装目录，不再弹选择窗口
    -NoShortcut        不创建桌面快捷方式
    -NoStartMenu       不创建开始菜单快捷方式
    -NoLaunchPrompt    装完不询问是否立即启动

  关于隐私：本发布包不含任何个人数据 —— 没有会话记录、没有插件配置、
  没有模型 API Key、没有控制令牌。core.json 由核心在首次启动时现场生成
  随机令牌，API Key 请用户装好后自己在界面里填。

  本文件保存为 UTF-8 with BOM：Windows PowerShell 5.1 读取无 BOM 的脚本时
  会按 ANSI 代码页解码，中文会乱码甚至解析失败。
#>

param(
  [string]$Dir = "",
  [switch]$NoShortcut,
  [switch]$NoStartMenu,
  [switch]$NoLaunchPrompt
)

$ErrorActionPreference = "Stop"

$src = $PSScriptRoot

function Get-AppName {
  $f = Join-Path $src "app-name.txt"
  if (Test-Path -LiteralPath $f) {
    $n = (Get-Content -LiteralPath $f -Raw -Encoding UTF8).Trim()
    if ($n) { return $n }
  }
  return "AI 工作平台"
}

function Select-Folder([string]$Default, [string]$Title) {
  try {
    Add-Type -AssemblyName System.Windows.Forms -ErrorAction Stop | Out-Null
    $dlg = New-Object System.Windows.Forms.FolderBrowserDialog
    $dlg.Description        = $Title
    $dlg.SelectedPath       = $Default
    $dlg.ShowNewFolderButton = $true
    if ($dlg.ShowDialog() -eq [System.Windows.Forms.DialogResult]::OK) {
      return $dlg.SelectedPath
    }
    return $null
  } catch {
    return $null
  }
}

function New-AppShortcut([string]$Target, [string]$WorkDir, [string]$LnkDir, [string]$AppName) {
  New-Item -ItemType Directory -Force -Path $LnkDir | Out-Null
  $path  = Join-Path $LnkDir ($AppName + ".lnk")
  $shell = New-Object -ComObject WScript.Shell
  $lnk   = $shell.CreateShortcut($path)
  $lnk.TargetPath       = $Target
  $lnk.WorkingDirectory = $WorkDir
  $lnk.Description      = $AppName
  $lnk.IconLocation     = $Target + ",0"
  $lnk.WindowStyle      = 1
  $lnk.Save()
  [void][Runtime.InteropServices.Marshal]::ReleaseComObject($shell)
  Write-Host ("  快捷方式: " + $path)
}

$appName = Get-AppName
$launcher = Join-Path $src ($appName + ".exe")
if (-not (Test-Path -LiteralPath $launcher)) {
  throw "包内找不到启动器: $launcher （压缩包是否完整解压？）"
}

Write-Host ""
Write-Host ("== " + $appName + " 安装 ==")
Write-Host ("来源: " + $src)
Write-Host ""

# ---------------------------------------------------------------------------
# 1. 选安装位置
# ---------------------------------------------------------------------------

if ([string]::IsNullOrWhiteSpace($Dir)) {
  $default = Join-Path $env:LOCALAPPDATA $appName
  Write-Host "请选择安装位置（可以选 D 盘、E 盘，或点“新建文件夹”）。"
  Write-Host ("直接确定则安装到: " + $default)
  $picked = Select-Folder $default ("选择 " + $appName + " 的安装目录")
  if (-not $picked) {
    # 窗口没弹出来（无桌面会话 / 远程会话），退回命令行输入
    $picked = Read-Host "安装到哪个目录？（直接回车用默认路径）"
    if ([string]::IsNullOrWhiteSpace($picked)) { $picked = $default }
  }
  $Dir = $picked
}

$Dir = $Dir.Trim().Trim('"')
if ($Dir -eq $src) {
  throw "不能把文件装到压缩包自己所在目录，请换一个目录。"
}

Write-Host ("安装到: " + $Dir)

# 已经装过：问一句要不要覆盖
if ((Test-Path -LiteralPath $Dir) -and (Get-ChildItem -LiteralPath $Dir -Force | Measure-Object).Count -gt 0) {
  $ans = Read-Host "该目录已存在且不为空，覆盖安装？[y/N]"
  if ($ans -notmatch '^(y|Y)') {
    Write-Host "已取消，什么都没改。"
    exit 0
  }
}

# ---------------------------------------------------------------------------
# 2. 拷文件
# ---------------------------------------------------------------------------

Write-Host ""
Write-Host "==> 复制文件"
New-Item -ItemType Directory -Force -Path $Dir | Out-Null

Get-ChildItem -LiteralPath $src -Force | ForEach-Object {
  if ($_.Name -eq "install.ps1") { return }   # 安装脚本本身不必留在安装目录
  Copy-Item -LiteralPath $_.FullName -Destination $Dir -Recurse -Force
}

# 首次运行会自己建的运行时目录，先建好，免得用户看到空目录发懵
foreach ($sub in @("logs", "plugin-configs", "pending-delete")) {
  New-Item -ItemType Directory -Force -Path (Join-Path (Join-Path $Dir "runtime") $sub) | Out-Null
}

# ---------------------------------------------------------------------------
# 3. 快捷方式
# ---------------------------------------------------------------------------

$target = Join-Path $Dir ($appName + ".exe")

if (-not $NoShortcut) {
  Write-Host ""
  Write-Host "==> 创建快捷方式"
  New-AppShortcut $target $Dir ([Environment]::GetFolderPath("Desktop")) $appName
  if (-not $NoStartMenu) {
    New-AppShortcut $target $Dir (Join-Path ([Environment]::GetFolderPath("Programs")) $appName) $appName
  }
}

# ---------------------------------------------------------------------------
# 4. 收尾
# ---------------------------------------------------------------------------

Write-Host ""
Write-Host ("安装完成: " + $Dir)
Write-Host ""
Write-Host "首次启动会生成你自己的控制令牌，聊天记录和配置都只存在这台机器上。"
Write-Host "模型的 API Key 请启动后在界面里填自己的（本包不含任何 Key）。"
Write-Host ""

if (-not $NoLaunchPrompt) {
  $run = Read-Host "现在启动？[y/N]"
  if ($run -match '^(y|Y)') {
    Start-Process -FilePath $target -WorkingDirectory $Dir
  }
}
