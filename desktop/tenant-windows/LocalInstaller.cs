using System.Net;
using System.Runtime.InteropServices;
using System.Security.Cryptography;

namespace CageTenant;

internal sealed class LocalCageException(string message) : Exception(message);

/// <summary>
/// Downloads and verifies the pinned upstream Cage installer. Cross-platform so
/// the logic tests can run it; starting programs lives in LocalCageLauncher.
/// </summary>
internal static class LocalInstaller
{
    public const string FileName = "Cage-0.1.8-windows-amd64-setup.exe";
    public const long Size = 484_402_040;
    public const string Sha256 = "4b069b1e82e5d0ed0fd5a718222428679cf392b2b5f3e7e1a8efddc30634f0c4";
    public static readonly Uri Url = new("https://github.com/Lee-xeo/Claude-Cage/releases/download/v0.1.8/" + FileName);
    public static readonly TimeSpan IdleTimeout = TimeSpan.FromSeconds(60);

    private const string DownloadFailed = "无法下载安装包，请检查网络后重试。";
    private const string ChecksumFailed = "安装包校验失败，请重试。";

    public static bool Supported(Architecture architecture) => architecture == Architecture.X64;

    public static bool Verify(string path) => Verify(path, Size, Sha256);

    internal static bool Verify(string path, long size, string sha256)
    {
        try
        {
            var info = new FileInfo(path);
            if (!info.Exists || info.Length != size || info.Attributes.HasFlag(FileAttributes.ReparsePoint)) return false;
            using var stream = info.OpenRead();
            return Convert.ToHexStringLower(SHA256.HashData(stream)) == sha256;
        }
        catch (Exception e) when (e is IOException or UnauthorizedAccessException)
        {
            return false;
        }
    }

    public static Task<string> PrepareAsync(string folder, HttpMessageHandler? handler = null, CancellationToken cancel = default) =>
        PrepareAsync(folder, Size, Sha256, IdleTimeout, handler, cancel);

    /// <summary>
    /// Network failures and stalls become LocalCageException; an IOException
    /// that escapes comes from writing to disk. Hashing runs off the caller's
    /// thread, and a leftover partial file from an interrupted run is replaced.
    /// </summary>
    internal static async Task<string> PrepareAsync(string folder, long size, string sha256, TimeSpan idle,
        HttpMessageHandler? handler = null, CancellationToken cancel = default)
    {
        Directory.CreateDirectory(folder);
        var target = Path.Combine(folder, FileName);
        var partial = target + ".partial";
        if (await Task.Run(() => Verify(target, size, sha256), cancel).ConfigureAwait(false)) return target;
        File.Delete(partial);
        try
        {
            using var client = handler == null ? new HttpClient() : new HttpClient(handler, disposeHandler: false);
            client.Timeout = Timeout.InfiniteTimeSpan;
            using var watchdog = CancellationTokenSource.CreateLinkedTokenSource(cancel);
            watchdog.CancelAfter(idle);
            using var response = await Network(() => client.GetAsync(Url, HttpCompletionOption.ResponseHeadersRead, watchdog.Token)).ConfigureAwait(false);
            if (response.StatusCode != HttpStatusCode.OK) throw new LocalCageException(DownloadFailed);
            if (response.Content.Headers.ContentLength > size) throw new LocalCageException(ChecksumFailed);
            await using (var output = new FileStream(partial, FileMode.CreateNew, FileAccess.Write, FileShare.None, 1 << 16, useAsync: true))
            await using (var input = await Network(() => response.Content.ReadAsStreamAsync(watchdog.Token)).ConfigureAwait(false))
            {
                var buffer = new byte[1 << 20];
                long received = 0;
                while (true)
                {
                    watchdog.CancelAfter(idle);
                    var read = await Network(() => input.ReadAsync(buffer, watchdog.Token).AsTask()).ConfigureAwait(false);
                    if (read == 0) break;
                    received += read;
                    if (received > size) throw new LocalCageException(ChecksumFailed);
                    await output.WriteAsync(buffer.AsMemory(0, read), cancel).ConfigureAwait(false);
                }
            }
            if (!await Task.Run(() => Verify(partial, size, sha256), cancel).ConfigureAwait(false))
                throw new LocalCageException(ChecksumFailed);
            File.Move(partial, target, overwrite: true);
            return target;
        }
        finally
        {
            try { File.Delete(partial); }
            catch (Exception e) when (e is IOException or UnauthorizedAccessException) { }
        }
    }

    // Reading the response can fail with HttpIOException (an IOException), so
    // network errors are mapped here rather than by type at the caller.
    private static async Task<T> Network<T>(Func<Task<T>> operation)
    {
        try
        {
            return await operation().ConfigureAwait(false);
        }
        catch (Exception e) when (e is HttpRequestException or IOException or OperationCanceledException)
        {
            throw new LocalCageException(DownloadFailed);
        }
    }
}
