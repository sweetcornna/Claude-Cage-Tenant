using System.Diagnostics;
using System.Runtime.InteropServices;
using Microsoft.Win32;

namespace CageTenant;

/// <summary>
/// The start page's local option: opens the installed upstream Cage, or its
/// pinned installer. The portal WebView has no access to this.
/// </summary>
internal static class LocalCageLauncher
{
    private const string UninstallKey = @"Software\Microsoft\Windows\CurrentVersion\Uninstall\ClaudeCage";

    public static string? InstalledApp()
    {
        var candidates = new List<string>();
        using (var key = Registry.CurrentUser.OpenSubKey(UninstallKey))
        {
            if (key?.GetValue("InstallLocation") is string location && Path.IsPathFullyQualified(location))
                candidates.Add(Path.Combine(location, "Cage.exe"));
        }
        candidates.Add(Path.Combine(Environment.GetFolderPath(Environment.SpecialFolder.LocalApplicationData), "Programs", "Cage", "Cage.exe"));
        return candidates.FirstOrDefault(File.Exists);
    }

    /// <summary>Returns a status message, or null when Cage itself was opened.</summary>
    public static async Task<string?> OpenAsync()
    {
        if (InstalledApp() is { } app)
        {
            Start(app);
            return null;
        }
        if (!LocalInstaller.Supported(RuntimeInformation.OSArchitecture))
            throw new LocalCageException("本地安装需要 x64 版 Windows 10 或 11。");
        var folder = Path.Combine(Environment.GetFolderPath(Environment.SpecialFolder.LocalApplicationData), "Cage Tenant", "Downloads");
        Start(await LocalInstaller.PrepareAsync(folder));
        return "已打开安装程序，请按提示完成安装。";
    }

    private static void Start(string path)
    {
        try
        {
            using var _ = Process.Start(new ProcessStartInfo(path) { UseShellExecute = true, WorkingDirectory = Path.GetDirectoryName(path)! });
        }
        catch (System.ComponentModel.Win32Exception)
        {
            throw new LocalCageException("无法打开本地 Cage。");
        }
    }
}
