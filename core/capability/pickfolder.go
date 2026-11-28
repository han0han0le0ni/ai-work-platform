package capability

import (
	"encoding/base64"
	"fmt"
	"strings"
	"unicode/utf16"
)

// csharpDialogSource 是内嵌的 C#：用 [ComImport] 定义 IFileOpenDialog / IShellItem，
// 弹**现代**文件夹选择框（Vista+ 风格，带地址栏、搜索、最近位置、侧边栏）。
//
// 用 C# 而不是 Go 手写 COM vtable：C# 的 [ComImport] 由编译器生成 vtable 调用，
// 不用手算偏移，出错的概率小得多；且不引入第三方 Go 依赖（项目保持零依赖）。
//
// 注意必须是 C# 5.0 语法：PowerShell 5.1 的 Add-Type 用的是 .NET Framework 编译器，
// 不支持内联 out 变量、字符串插值这类 C# 7+ 写法。
const csharpDialogSource = `using System;
using System.Runtime.InteropServices;

public static class FolderDialog {
    [ComImport, Guid("d57c7288-d4ad-4768-be02-9d969532d960"), InterfaceType(ComInterfaceType.InterfaceIsIUnknown)]
    private interface IFileOpenDialog {
        [PreserveSig] int Show(IntPtr hwndOwner);
        void SetFileTypes(uint cFileTypes, IntPtr rgFilterSpec);
        void SetFileTypeIndex(uint iFileType);
        void GetFileTypeIndex(out uint piFileType);
        void Advise(IntPtr pfde, out uint pdwCookie);
        void Unadvise(uint dwCookie);
        void SetOptions(uint fos);
        void GetOptions(out uint pfos);
        void SetDefaultFolder(IShellItem psi);
        void SetFolder(IShellItem psi);
        void GetFolder(out IShellItem ppsi);
        void GetCurrentSelection(out IShellItem ppsi);
        void SetFileName([MarshalAs(UnmanagedType.LPWStr)] string pszName);
        void GetFileName([MarshalAs(UnmanagedType.LPWStr)] out string pszName);
        void SetTitle([MarshalAs(UnmanagedType.LPWStr)] string pszTitle);
        void SetOkButtonLabel([MarshalAs(UnmanagedType.LPWStr)] string pszText);
        void SetFileNameLabel([MarshalAs(UnmanagedType.LPWStr)] string pszLabel);
        void GetResult(out IShellItem ppsi);
        void AddPlace(IShellItem psi, int alignment);
        void SetDefaultExtension([MarshalAs(UnmanagedType.LPWStr)] string pszDefaultExtension);
        void Close(int hr);
        void SetClientGuid(ref Guid guid);
        void ClearClientData();
        void SetFilter(IntPtr pFilter);
        void GetResults(out IntPtr ppenum);
        void GetSelectedItems(out IntPtr ppsai);
    }

    [ComImport, Guid("43826d1e-e718-42ee-bc55-a1e261c37bfe"), InterfaceType(ComInterfaceType.InterfaceIsIUnknown)]
    private interface IShellItem {
        void BindToHandler(IntPtr pbc, ref Guid bhid, ref Guid riid, out IntPtr ppv);
        void GetParent(out IShellItem ppsi);
        void GetDisplayName(uint sigdnName, out IntPtr ppszName);
        void GetAttributes(uint sfgaoMask, out uint psfgaoAttribs);
        void Compare(IShellItem psi, uint hint, out int piOrder);
    }

    [DllImport("shell32.dll", CharSet = CharSet.Unicode)]
    private static extern int SHCreateItemFromParsingName(string pszPath, IntPtr pbc, ref Guid riid, out IShellItem ppv);

    public static string Pick(string title, string initial) {
        IFileOpenDialog dlg = (IFileOpenDialog)Activator.CreateInstance(
            Type.GetTypeFromCLSID(new Guid("DC1C5A9C-E88A-4DDE-A5A1-60F82A20AEF7")));
        dlg.SetOptions(0x20 | 0x40); // FOS_PICKFOLDERS | FOS_FORCEFILESYSTEM
        if (!string.IsNullOrEmpty(title)) { dlg.SetTitle(title); }
        if (!string.IsNullOrEmpty(initial) && System.IO.Directory.Exists(initial)) {
            Guid iid = new Guid("43826d1e-e718-42ee-bc55-a1e261c37bfe");
            IShellItem si;
            if (SHCreateItemFromParsingName(initial, IntPtr.Zero, ref iid, out si) == 0) { dlg.SetFolder(si); }
        }
        // 用一个屏幕外的置顶隐形窗口当 owner。
        //
        // 没有 owner、且调用方是后台进程（核心/插件）时，Windows 的前景锁会让
        // 对话框直接出现在主窗口后面 —— 用户看到的现象就是"点了选择目录没反应"，
        // 而这时候 PowerShell 进程其实正卡在等用户选择。带一个 TopMost 的
        // owner 窗口后，对话框就会浮到最前面。
        System.Windows.Forms.Form owner = new System.Windows.Forms.Form();
        owner.TopMost = true;
        owner.ShowInTaskbar = false;
        owner.FormBorderStyle = System.Windows.Forms.FormBorderStyle.None;
        owner.StartPosition = System.Windows.Forms.FormStartPosition.Manual;
        owner.Size = new System.Drawing.Size(1, 1);
        owner.Location = new System.Drawing.Point(-3000, -3000);
        owner.Show();
        owner.Activate();
        try {
            if (dlg.Show(owner.Handle) != 0) { return null; } // 用户取消（或失败）
            IShellItem item;
            dlg.GetResult(out item);
            IntPtr pszName;
            item.GetDisplayName(0x80058000, out pszName); // SIGDN_FILESYSPATH
            string path = Marshal.PtrToStringUni(pszName);
            Marshal.FreeCoTaskMem(pszName);
            return path;
        } finally {
            owner.Close();
        }
    }
}
`

// pickFolderScript 生成一段 PowerShell：Add-Type 编译上面的 C#，再弹对话框。
//
// title / initial 直接写死在脚本里（用 psQuote 转义单引号），
// 整个脚本经 -EncodedCommand（UTF-16LE base64）传递，绕开命令行引号与中文转义。
//
// 选中的路径同样不以明文写出，而是先编成 UTF-8 的 base64 —— 见 decodePickedPath。
func pickFolderScript(title, initial string) string {
	title = psQuote(title)
	initial = psQuote(initial)
	return fmt.Sprintf(`$src = @'
%s
'@
Add-Type -TypeDefinition $src -ReferencedAssemblies System.Windows.Forms, System.Drawing
$r = [FolderDialog]::Pick('%s', '%s')
if ($r) { [Console]::Out.Write([Convert]::ToBase64String([System.Text.Encoding]::UTF8.GetBytes($r))) }
`, csharpDialogSource, title, initial)
}

// psQuote 把字符串安全地放进 PowerShell 的单引号字面量：
// 单引号字符串里唯一的转义就是把 ' 写成 ''。
func psQuote(s string) string {
	return strings.ReplaceAll(s, "'", "''")
}

// psEncodedCommand 把脚本编成 -EncodedCommand 要的格式：UTF-16LE 的 base64。
//
// 用 -EncodedCommand 而不是把脚本塞进命令行参数：脚本里带引号、换行、中文时，
// 直接拼参数要经过一层又一层转义；base64 把这一切都绕开了。
func psEncodedCommand(script string) string {
	units := utf16.Encode([]rune(script))
	b := make([]byte, 0, len(units)*2)
	for _, u := range units {
		b = append(b, byte(u), byte(u>>8))
	}
	return base64.StdEncoding.EncodeToString(b)
}
