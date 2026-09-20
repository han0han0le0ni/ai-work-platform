<#
  set-windows-resources.ps1

  Stamps an icon and version info into an already compiled Windows executable.

  Why "stamp" and not "compile": Go's linker does not write a PE .rsrc section,
  so a freshly built exe has no resources at all - Explorer shows the default
  icon and Properties -> Details is completely empty. This script fills that gap
  with the BeginUpdateResource / UpdateResource / EndUpdateResource API that
  ships with Windows: no third-party tool, no change to the Go sources.

  Usage (normally called by build.ps1, not typed by hand):
    powershell -ExecutionPolicy Bypass -File tools/set-windows-resources.ps1 `
      -Exe runtime/core.exe, "MyApp.exe" -Ico runtime/app.ico `
      -ProductName "..." -CompanyName "..." -FileVersion 1.0.0.0

  NOTE: kept ASCII-only on purpose. Windows PowerShell 5.1 reads BOM-less
  scripts using the ANSI code page. In CP936 a multi-byte UTF-8 character can
  swallow the ASCII byte that follows it, so a stray non-ASCII character inside
  a comment or a string literal can break parsing in a way that is very hard to
  see. Non-ASCII values (product names) are passed in as parameters instead.
#>

param(
  [Parameter(Mandatory = $true)][string[]]$Exe,
  [Parameter(Mandatory = $true)][string]$Ico,
  [string]$ProductName = "",
  [string]$FileDescription = "",
  [string]$CompanyName = "",
  [string]$FileVersion = "1.0.0.0"
)

$ErrorActionPreference = "Stop"

$csharp = @'
using System;
using System.Collections.Generic;
using System.ComponentModel;
using System.IO;
using System.Runtime.InteropServices;
using System.Text;

public static class AppResourceStamper
{
    private const int RT_ICON = 3;
    private const int RT_GROUP_ICON = 14;
    private const int RT_VERSION = 16;
    private const ushort LANG = 0x0409; // en-US，版本信息里的 040904B0 要对上

    [DllImport("kernel32.dll", SetLastError = true, CharSet = CharSet.Unicode)]
    private static extern IntPtr BeginUpdateResource(string pFileName, bool bDeleteExistingResources);

    [DllImport("kernel32.dll", SetLastError = true)]
    private static extern bool UpdateResource(IntPtr hUpdate, IntPtr lpType, IntPtr lpName, ushort wLanguage, byte[] lpData, uint cbData);

    [DllImport("kernel32.dll", SetLastError = true)]
    private static extern bool EndUpdateResource(IntPtr hUpdate, bool fDiscard);

    // ------------------------------------------------------------------
    // 入口
    // ------------------------------------------------------------------

    public static int Apply(string exePath, string icoPath, byte[] versionInfo)
    {
        if (!File.Exists(exePath)) throw new FileNotFoundException(exePath);
        if (!File.Exists(icoPath)) throw new FileNotFoundException(icoPath);

        List<byte[]> images = new List<byte[]>();
        List<byte[]> entries = new List<byte[]>();
        ReadIco(icoPath, images, entries);

        IntPtr h = BeginUpdateResource(exePath, false);
        if (h == IntPtr.Zero)
            throw new Win32Exception(Marshal.GetLastWin32Error(), "BeginUpdateResource failed: " + exePath);

        try
        {
            for (int i = 0; i < images.Count; i++)
            {
                byte[] image = images[i];
                if (!UpdateResource(h, (IntPtr)RT_ICON, (IntPtr)(i + 1), LANG, image, (uint)image.Length))
                    throw new Win32Exception(Marshal.GetLastWin32Error(), "UpdateResource RT_ICON failed");
            }

            byte[] group = BuildGroup(entries);
            if (!UpdateResource(h, (IntPtr)RT_GROUP_ICON, (IntPtr)1, LANG, group, (uint)group.Length))
                throw new Win32Exception(Marshal.GetLastWin32Error(), "UpdateResource RT_GROUP_ICON failed");

            if (versionInfo != null && versionInfo.Length > 0)
            {
                if (!UpdateResource(h, (IntPtr)RT_VERSION, (IntPtr)1, LANG, versionInfo, (uint)versionInfo.Length))
                    throw new Win32Exception(Marshal.GetLastWin32Error(), "UpdateResource RT_VERSION failed");
            }

            if (!EndUpdateResource(h, false))
                throw new Win32Exception(Marshal.GetLastWin32Error(), "EndUpdateResource failed");
            h = IntPtr.Zero;
        }
        finally
        {
            if (h != IntPtr.Zero) EndUpdateResource(h, true); // 出错就丢掉，别留下半截资源
        }

        return images.Count;
    }

    // ------------------------------------------------------------------
    // .ico 解析
    // ------------------------------------------------------------------

    private static void ReadIco(string path, List<byte[]> images, List<byte[]> entries)
    {
        byte[] all = File.ReadAllBytes(path);
        if (all.Length < 6) throw new InvalidDataException("icon file too small: " + path);

        int type = BitConverter.ToUInt16(all, 2);
        int count = BitConverter.ToUInt16(all, 4);
        if (type != 1) throw new InvalidDataException("not an icon file: " + path);
        if (count <= 0 || 6 + count * 16 > all.Length) throw new InvalidDataException("broken icon directory: " + path);

        for (int i = 0; i < count; i++)
        {
            int off = 6 + i * 16;
            int size = (int)BitConverter.ToUInt32(all, off + 8);
            int start = (int)BitConverter.ToUInt32(all, off + 12);
            if (start + size > all.Length) throw new InvalidDataException("broken icon entry " + i);

            byte[] entry = new byte[12];            // GRPICONDIRENTRY 的前 12 字节与 ICONDIRENTRY 相同
            Array.Copy(all, off, entry, 0, 12);     // 后 4 字节是 dwImageOffset，资源里要换成 nId
            entries.Add(entry);

            byte[] image = new byte[size];
            Array.Copy(all, start, image, 0, size);
            images.Add(image);
        }
    }

    // RT_GROUP_ICON 的载荷：GRPICONDIR + 每帧 GRPICONDIRENTRY(14 字节)
    private static byte[] BuildGroup(List<byte[]> entries)
    {
        MemoryStream ms = new MemoryStream();
        BinaryWriter w = new BinaryWriter(ms);
        w.Write((ushort)0);                 // idReserved
        w.Write((ushort)1);                 // idType
        w.Write((ushort)entries.Count);     // idCount
        for (int i = 0; i < entries.Count; i++)
        {
            w.Write(entries[i]);            // 12 字节
            w.Write((ushort)(i + 1));       // nId，与 RT_ICON 的序号对上
        }
        w.Flush();
        return ms.ToArray();
    }

    // ------------------------------------------------------------------
    // VS_VERSIONINFO
    // ------------------------------------------------------------------

    public static byte[] BuildVersion(string productName, string fileDescription, string companyName, string fileVersion, string originalFilename)
    {
        if (productName == null || productName.Length == 0) productName = originalFilename;
        if (fileDescription == null || fileDescription.Length == 0) fileDescription = productName;
        if (fileVersion == null || fileVersion.Length == 0) fileVersion = "1.0.0.0";

        uint[] v = ParseVersion(fileVersion);
        byte[] fixedInfo = BuildFixedFileInfo(v);

        List<byte[]> strings = new List<byte[]>();
        AddIfNotEmpty(strings, "CompanyName", companyName);
        AddIfNotEmpty(strings, "FileDescription", fileDescription);
        AddIfNotEmpty(strings, "FileVersion", fileVersion);
        AddIfNotEmpty(strings, "InternalName", originalFilename);
        AddIfNotEmpty(strings, "OriginalFilename", originalFilename);
        AddIfNotEmpty(strings, "ProductName", productName);
        AddIfNotEmpty(strings, "ProductVersion", fileVersion);

        byte[] stringTable = VerBlock("040904B0", 1, null, strings.ToArray());
        byte[] stringFileInfo = VerBlock("StringFileInfo", 1, null, new byte[][] { stringTable });

        byte[] translation = VerBlock("Translation", 0, new byte[] { 0x09, 0x04, 0xB0, 0x04 }, null);
        byte[] varFileInfo = VerBlock("VarFileInfo", 1, null, new byte[][] { translation });

        return VerBlock("VS_VERSION_INFO", 0, fixedInfo, new byte[][] { stringFileInfo, varFileInfo });
    }

    private static void AddIfNotEmpty(List<byte[]> list, string key, string value)
    {
        if (!string.IsNullOrEmpty(value)) list.Add(VerString(key, value));
    }

    private static uint[] ParseVersion(string s)
    {
        string[] parts = (s ?? "").Split('.');
        uint[] r = new uint[] { 0, 0, 0, 0 };
        for (int i = 0; i < 4 && i < parts.Length; i++)
        {
            uint n;
            if (uint.TryParse(parts[i], out n)) r[i] = n;
        }
        return r;
    }

    private static byte[] BuildFixedFileInfo(uint[] v)
    {
        MemoryStream ms = new MemoryStream();
        BinaryWriter w = new BinaryWriter(ms);
        w.Write((uint)0xFEEF04BD);                  // dwSignature，固定魔数
        w.Write((uint)0x00010000);                  // dwStrucVersion
        w.Write((uint)((v[0] << 16) | v[1]));       // dwFileVersionMS
        w.Write((uint)((v[2] << 16) | v[3]));       // dwFileVersionLS
        w.Write((uint)((v[0] << 16) | v[1]));       // dwProductVersionMS
        w.Write((uint)((v[2] << 16) | v[3]));       // dwProductVersionLS
        w.Write((uint)0x3F);                        // dwFileFlagsMask
        w.Write((uint)0);                           // dwFileFlags
        w.Write((uint)0x00000004);                  // dwFileOS = VOS__WINDOWS32
        w.Write((uint)1);                           // dwFileType = VFT_APP
        w.Write((uint)0);                           // dwFileSubtype
        w.Write((uint)0);                           // dwFileDateMS
        w.Write((uint)0);                           // dwFileDateLS
        w.Flush();
        return ms.ToArray();                        // 13 个 DWORD = 52 字节
    }

    private static byte[] VerString(string key, string value)
    {
        MemoryStream ms = new MemoryStream();
        BinaryWriter w = new BinaryWriter(ms, Encoding.Unicode);
        w.Write((ushort)0);                         // wLength，最后回填
        w.Write((ushort)0);                         // wValueLength，最后回填
        w.Write((ushort)1);                         // wType = 文本
        WriteKey(w, key);
        long vs = w.BaseStream.Position;
        WriteChars(w, value);
        w.Write((ushort)0);                         // 值也要以 NUL 结尾
        long ve = w.BaseStream.Position;
        Align4(w);
        return Finish(ms, vs, ve);
    }

    private static byte[] VerBlock(string key, ushort type, byte[] value, byte[][] children)
    {
        MemoryStream ms = new MemoryStream();
        BinaryWriter w = new BinaryWriter(ms, Encoding.Unicode);
        w.Write((ushort)0);                         // wLength，最后回填
        w.Write((ushort)0);                         // wValueLength，最后回填
        w.Write(type);
        WriteKey(w, key);
        long vs = w.BaseStream.Position;
        if (value != null && value.Length > 0)
        {
            w.Write(value);
            long ve = w.BaseStream.Position;
            Align4(w);
            if (children != null)
                foreach (byte[] c in children) { w.Write(c); Align4(w); }
            return Finish(ms, vs, ve);
        }
        if (children != null)
            foreach (byte[] c in children) { w.Write(c); Align4(w); }
        return Finish(ms, vs, vs);
    }

    private static void WriteKey(BinaryWriter w, string key)
    {
        WriteChars(w, key);
        w.Write((ushort)0);
        Align4(w);
    }

    private static void WriteChars(BinaryWriter w, string s)
    {
        foreach (char c in s) w.Write(c);
    }

    private static void Align4(BinaryWriter w)
    {
        long pos = w.BaseStream.Position;
        int pad = (int)((4 - (pos % 4)) % 4);
        if (pad > 0) w.Write(new byte[pad]);
    }

    // 回填 wLength / wValueLength，然后交出完整字节
    private static byte[] Finish(MemoryStream ms, long valueStart, long valueEnd)
    {
        byte[] data = ms.ToArray();
        int len = data.Length;
        data[0] = (byte)(len & 0xFF);
        data[1] = (byte)((len >> 8) & 0xFF);
        int vlen = (int)(valueEnd - valueStart);
        data[2] = (byte)(vlen & 0xFF);
        data[3] = (byte)((vlen >> 8) & 0xFF);
        return data;
    }
}
'@

Add-Type -TypeDefinition $csharp -Language CSharp

$icoFull = (Resolve-Path -LiteralPath $Ico).Path

$failed = @()

foreach ($one in $Exe) {
  $exeFull = (Resolve-Path -LiteralPath $one).Path
  $fileName = Split-Path $exeFull -Leaf

  try {
    $version = [AppResourceStamper]::BuildVersion($ProductName, $FileDescription, $CompanyName, $FileVersion, $fileName)
    $frames = [AppResourceStamper]::Apply($exeFull, $icoFull, $version)
    Write-Host ("  resources: " + $fileName + "  (" + $frames + " icon frames + version info)")
  } catch {
    # The usual reason is that the exe is currently running, so Windows has the
    # file locked and EndUpdateResource cannot replace it. One locked binary
    # must not stop the others from being stamped.
    $failed += $fileName
    Write-Warning ("  skipped " + $fileName + ": " + $_.Exception.Message)
  }
}

if ($failed.Count -gt 0) {
  Write-Warning ("resources missing for: " + ($failed -join ", ") + "  -- close the platform and build again")
}
