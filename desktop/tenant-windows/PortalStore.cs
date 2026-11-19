using System.Security.Cryptography;
using System.Text;

namespace CageTenant;

/// <summary>
/// Owns everything the client writes under its data folder
/// (<c>%LOCALAPPDATA%\Cage Tenant</c>): the portal origin, which is the only
/// value the client itself persists, and one WebView2 user data folder per
/// portal origin under <c>Sessions</c>. The client never reads the session
/// folders; WebView2 keeps cookies and site storage there.
/// </summary>
public sealed class PortalStore(string root)
{
    public const string OriginFileName = "portal-origin.txt";
    public const string SessionsFolderName = "Sessions";
    private const int MaxOriginFileBytes = 4096;

    public string Root { get; } = Path.GetFullPath(root);
    public string OriginFile => Path.Combine(Root, OriginFileName);
    public string SessionsRoot => Path.Combine(Root, SessionsFolderName);

    public static PortalStore ForCurrentUser() => new(Path.Combine(
        Environment.GetFolderPath(Environment.SpecialFolder.LocalApplicationData, Environment.SpecialFolderOption.Create),
        "Cage Tenant"));

    /// <summary>
    /// Returns the stored origin after validating it again. A value that no
    /// longer validates (for example, edited by hand) is removed.
    /// </summary>
    public PortalOrigin? Load()
    {
        var info = new FileInfo(OriginFile);
        if (!info.Exists) return null;
        if (info.LinkTarget != null || info.Length > MaxOriginFileBytes)
        {
            Clear();
            return null;
        }
        string text;
        try { text = File.ReadAllText(OriginFile, new UTF8Encoding(false, true)); }
        catch (DecoderFallbackException) { Clear(); return null; }
        if (PortalAddress.TryValidate(text, out var origin, out _)) return origin;
        Clear();
        return null;
    }

    /// <summary>Writes the normalized origin, replacing the file atomically.</summary>
    public void Save(PortalOrigin origin)
    {
        Directory.CreateDirectory(Root);
        var temp = Path.Combine(Root, "." + OriginFileName + "." + Guid.NewGuid().ToString("N") + ".tmp");
        try
        {
            File.WriteAllText(temp, origin.Origin + "\n", new UTF8Encoding(false));
            File.Move(temp, OriginFile, overwrite: true);
        }
        finally
        {
            if (File.Exists(temp)) File.Delete(temp);
        }
    }

    public void Clear()
    {
        if (File.Exists(OriginFile) || new FileInfo(OriginFile).LinkTarget != null) File.Delete(OriginFile);
    }

    /// <summary>Stable, per-origin folder name; different servers never share a session.</summary>
    public static string SessionKey(PortalOrigin origin) =>
        Convert.ToHexStringLower(SHA256.HashData(Encoding.ASCII.GetBytes(origin.Origin)))[..32];

    public string SessionFolder(PortalOrigin origin) => Path.Combine(SessionsRoot, SessionKey(origin));

    /// <summary>
    /// Deletes every session folder except the one for <paramref name="keep"/>.
    /// Run at startup so data from a previous server is removed even if an
    /// earlier deletion failed. Returns the folders that could not be removed.
    /// </summary>
    public IReadOnlyList<string> RemoveSessionsExcept(PortalOrigin? keep)
    {
        var failed = new List<string>();
        if (!Directory.Exists(SessionsRoot)) return failed;
        var keepName = keep == null ? null : SessionKey(keep);
        foreach (var entry in new DirectoryInfo(SessionsRoot).EnumerateFileSystemInfos())
        {
            if (entry.Name == keepName && entry is DirectoryInfo && entry.LinkTarget == null) continue;
            if (!TryDelete(entry)) failed.Add(entry.FullName);
        }
        return failed;
    }

    /// <summary>Deletes one origin's session folder. False if it is still in use.</summary>
    public bool RemoveSession(PortalOrigin origin)
    {
        var folder = new DirectoryInfo(SessionFolder(origin));
        return !folder.Exists && folder.LinkTarget == null || TryDelete(folder);
    }

    private static bool TryDelete(FileSystemInfo entry)
    {
        try
        {
            // A link is removed without following it.
            if (entry.LinkTarget != null || entry is FileInfo) entry.Delete();
            else ((DirectoryInfo)entry).Delete(recursive: true);
            return true;
        }
        catch (IOException) { return false; }
        catch (UnauthorizedAccessException) { return false; }
    }
}
