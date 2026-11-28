package capability

import (
	"fmt"
	"strings"
)

// csharpFilesSource 用 IFileOpenDialog 弹一个**可多选文件**的现代选择框。
//
// 与 pickfolder.go 里的 FolderDialog 是同一套 COM 接口，区别只在选项：
// 不带 FOS_PICKFOLDERS，加上 FOS_ALLOWMULTISELECT / FOS_FILEMUSTEXIST。
// 为什么不复用同一个类型：两个对话框的 GetResults/GetResult 取值方式不同，
// 分开写反而更不容易在 vtable 顺序上出错。
const csharpFilesSource = `using System;
using System.Text;
using System.Runtime.InteropServices;

public static class FileDialog {
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
        void GetResults(out IShellItemArray ppenum);
        void GetSelectedItems(out IntPtr ppsai);
    }

    [ComImport, Guid("b63ea76d-1f85-456f-a19c-48159efa858b"), InterfaceType(ComInterfaceType.InterfaceIsIUnknown)]
    private interface IShellItemArray {
        void BindToHandler(IntPtr pbc, ref Guid bhid, ref Guid riid, out IntPtr ppvOut);
        void GetPropertyStore(int flags, ref Guid riid, out IntPtr ppv);
        void GetPropertyDescriptionList(IntPtr keyType, ref Guid riid, out IntPtr ppv);
        void GetAttributes(uint attribFlags, uint sfgaoMask, out uint psfgaoAttribs);
        void GetCount(out uint pdwNumItems);
        void GetItemAt(uint dwIndex, out IShellItem ppsi);
        void EnumItems(out IntPtr ppenumShellItems);
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
        // FOS_FORCEFILESYSTEM | FOS_ALLOWMULTISELECT | FOS_FILEMUSTEXIST | FOS_PATHMUSTEXIST
        dlg.SetOptions(0x40 | 0x200 | 0x1000 | 0x800);
        if (!string.IsNullOrEmpty(title)) { dlg.SetTitle(title); }
        if (!string.IsNullOrEmpty(initial) && System.IO.Directory.Exists(initial)) {
            Guid iid = new Guid("43826d1e-e718-42ee-bc55-a1e261c37bfe");
            IShellItem si;
            if (SHCreateItemFromParsingName(initial, IntPtr.Zero, ref iid, out si) == 0) { dlg.SetFolder(si); }
        }
        // 和选目录一样：后台进程没有前台权限，必须给对话框一个置顶的隐形 owner，
        // 否则它会藏在主窗口后面，用户以为点了没反应。
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
            if (dlg.Show(owner.Handle) != 0) { return null; }
            IShellItemArray arr;
            dlg.GetResults(out arr);
            uint count;
            arr.GetCount(out count);
            StringBuilder sb = new StringBuilder();
            for (uint i = 0; i < count; i++) {
                IShellItem item;
                arr.GetItemAt(i, out item);
                IntPtr pszName;
                item.GetDisplayName(0x80058000, out pszName); // SIGDN_FILESYSPATH
                string p = Marshal.PtrToStringUni(pszName);
                Marshal.FreeCoTaskMem(pszName);
                if (i > 0) { sb.Append('\n'); }
                sb.Append(p);
            }
            return sb.ToString();
        } finally {
            owner.Close();
        }
    }
}
`

// pickFilesScript 生成弹多选文件对话框的 PowerShell。
//
// 与选目录相同的两条约定：脚本走 -EncodedCommand 传，结果先 UTF-8 + base64
// 再输出（PowerShell 管道的代码页不可控，中文路径直接写会变乱码）。
func pickFilesScript(title, initial string) string {
	title = psQuote(title)
	initial = psQuote(initial)
	return fmt.Sprintf(`$src = @'
%s
'@
Add-Type -TypeDefinition $src -ReferencedAssemblies System.Windows.Forms, System.Drawing
$r = [FileDialog]::Pick('%s', '%s')
if ($r) { [Console]::Out.Write([Convert]::ToBase64String([System.Text.Encoding]::UTF8.GetBytes($r))) }
`, csharpFilesSource, title, initial)
}

// splitPickedPaths 拆分一行行路径（C# 侧用 \n 连接，Windows 路径里不可能有换行）。
func splitPickedPaths(s string) []string {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	out := make([]string, 0, 4)
	for _, line := range strings.Split(s, "\n") {
		if p := strings.TrimSpace(line); p != "" {
			out = append(out, p)
		}
	}
	return out
}
