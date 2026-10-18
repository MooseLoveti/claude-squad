# Shows the Explorer-style folder dialog (IFileOpenDialog with
# FOS_PICKFOLDERS) and prints the chosen path, or nothing when cancelled.
# $title and $initial are set by the caller.
[Console]::OutputEncoding = [Text.Encoding]::UTF8
$ErrorActionPreference = 'Stop'

Add-Type -TypeDefinition @'
using System;
using System.Runtime.InteropServices;

public static class ClaudeSquadFolderPicker {
    [ComImport, Guid("DC1C5A9C-E88A-4dde-A5A1-60F82A20AEF7")]
    class FileOpenDialog {}

    [ComImport, Guid("42f85136-db7e-439c-85f1-e4075d135fc8"), InterfaceType(ComInterfaceType.InterfaceIsIUnknown)]
    interface IFileDialog {
        [PreserveSig] int Show(IntPtr parent);
        void SetFileTypes(uint count, IntPtr specs);
        void SetFileTypeIndex(uint index);
        void GetFileTypeIndex(out uint index);
        void Advise(IntPtr events, out uint cookie);
        void Unadvise(uint cookie);
        void SetOptions(uint options);
        void GetOptions(out uint options);
        void SetDefaultFolder(IShellItem item);
        void SetFolder(IShellItem item);
        void GetFolder(out IShellItem item);
        void GetCurrentSelection(out IShellItem item);
        void SetFileName([MarshalAs(UnmanagedType.LPWStr)] string name);
        void GetFileName([MarshalAs(UnmanagedType.LPWStr)] out string name);
        void SetTitle([MarshalAs(UnmanagedType.LPWStr)] string title);
        void SetOkButtonLabel([MarshalAs(UnmanagedType.LPWStr)] string text);
        void SetFileNameLabel([MarshalAs(UnmanagedType.LPWStr)] string label);
        void GetResult(out IShellItem item);
    }

    [ComImport, Guid("43826D1E-E718-42EE-BC55-A1E261C37BFE"), InterfaceType(ComInterfaceType.InterfaceIsIUnknown)]
    interface IShellItem {
        void BindToHandler(IntPtr bc, ref Guid bhid, ref Guid riid, out IntPtr ppv);
        void GetParent(out IShellItem parent);
        void GetDisplayName(uint sigdn, [MarshalAs(UnmanagedType.LPWStr)] out string name);
    }

    [DllImport("shell32.dll", CharSet = CharSet.Unicode, PreserveSig = false)]
    static extern void SHCreateItemFromParsingName(string path, IntPtr bc, ref Guid riid, out IShellItem item);

    const uint FOS_PICKFOLDERS = 0x20;
    const uint FOS_FORCEFILESYSTEM = 0x40;
    const uint SIGDN_FILESYSPATH = 0x80058000;

    public static string Pick(string title, string initial, IntPtr owner) {
        IFileDialog dialog = (IFileDialog)new FileOpenDialog();
        uint options;
        dialog.GetOptions(out options);
        dialog.SetOptions(options | FOS_PICKFOLDERS | FOS_FORCEFILESYSTEM);
        dialog.SetTitle(title);
        if (!String.IsNullOrEmpty(initial)) {
            try {
                Guid id = typeof(IShellItem).GUID;
                IShellItem folder;
                SHCreateItemFromParsingName(initial, IntPtr.Zero, ref id, out folder);
                dialog.SetFolder(folder);
            } catch (Exception) {
                // An unreachable starting folder just starts elsewhere.
            }
        }
        if (dialog.Show(owner) != 0) {
            return null;
        }
        IShellItem result;
        dialog.GetResult(out result);
        string path;
        result.GetDisplayName(SIGDN_FILESYSPATH, out path);
        return path;
    }
}
'@

# An invisible topmost window owns the dialog, so it opens in front of the
# terminal instead of behind it.
Add-Type -AssemblyName System.Windows.Forms
$owner = New-Object System.Windows.Forms.Form
$owner.TopMost = $true
$owner.ShowInTaskbar = $false
$owner.Opacity = 0
$owner.StartPosition = 'CenterScreen'
$owner.Show()
$owner.Activate()
try {
    $path = [ClaudeSquadFolderPicker]::Pick($title, $initial, $owner.Handle)
} finally {
    $owner.Close()
}
if ($path) {
    [Console]::Out.Write($path)
}
