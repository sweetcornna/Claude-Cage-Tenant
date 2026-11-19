import AppKit
import CryptoKit
import Security

/// The startup button can hand off to the upstream client or its pinned installer.
/// The portal WebView has no access to these operations.
enum LocalCage {
    private static let installerName = "Cage-0.1.8-macos-arm64.dmg"
    private static let installerSize = 207_154_169
    private static let installerDigest = "a6e8c6d241a4831e118e692f66586fdad322c1578f9bf16b3da9b212a7d35847"
    private static let installerURL = URL(string: "https://github.com/Lee-xeo/Claude-Cage/releases/download/v0.1.8/\(installerName)")!
    private static let installedRequirement = """
        identifier "io.github.lee-xeo.claude-cage" and anchor apple generic and \
        certificate 1[field.1.2.840.113635.100.6.2.6] exists and \
        certificate leaf[field.1.2.840.113635.100.6.1.13] exists and \
        certificate leaf[subject.OU] = "WBR2J2NFHN"
        """

    enum Failure: LocalizedError {
        case unsupported, download, checksum, open

        var errorDescription: String? {
            switch self {
            case .unsupported: "本地安装需要 Apple Silicon Mac。"
            case .download: "无法下载安装包，请检查网络后重试。"
            case .checksum: "安装包校验失败，请重试。"
            case .open: "无法打开本地 Cage，请重试。"
            }
        }
    }

    @MainActor
    static func open() async throws {
        if let app = await Task.detached(priority: .userInitiated, operation: installedApp).value {
            do {
                _ = try await NSWorkspace.shared.openApplication(at: app, configuration: .init())
            } catch {
                throw Failure.open
            }
            return
        }
        var supported: Int32 = 0
        var length = MemoryLayout<Int32>.size
        guard sysctlbyname("hw.optional.arm64", &supported, &length, nil, 0) == 0, supported == 1 else {
            throw Failure.unsupported
        }
        let installer = try await Task.detached(priority: .userInitiated) { try await prepareInstaller() }.value
        guard NSWorkspace.shared.open(installer) else { throw Failure.open }
    }

    private static func installedApp() -> URL? {
        var requirement: SecRequirement?
        guard SecRequirementCreateWithString(installedRequirement as CFString, [], &requirement) == errSecSuccess,
              let requirement else { return nil }
        // The App Sandbox can read /Applications, but not the user's ~/Applications.
        let app = URL(fileURLWithPath: "/Applications/Cage.app", isDirectory: true)
        let flags = SecCSFlags(rawValue: kSecCSStrictValidate | kSecCSCheckNestedCode)
        var code: SecStaticCode?
        guard SecStaticCodeCreateWithPath(app as CFURL, [], &code) == errSecSuccess, let code,
              SecStaticCodeCheckValidity(code, flags, requirement) == errSecSuccess else { return nil }
        return app
    }

    private static func prepareInstaller() async throws -> URL {
        let manager = FileManager.default
        let cache = try manager.url(for: .cachesDirectory, in: .userDomainMask, appropriateFor: nil, create: true)
            .appendingPathComponent("LocalCage", isDirectory: true)
        try manager.createDirectory(at: cache, withIntermediateDirectories: true, attributes: [.posixPermissions: 0o700])
        let installer = cache.appendingPathComponent(installerName)
        if (try? verifyInstaller(installer)) == true { return installer }

        // A fixed name, so a file left by an interrupted run is replaced.
        let partial = cache.appendingPathComponent(installerName + ".partial")
        defer { try? manager.removeItem(at: partial) }
        try await CappedDownload(limit: Int64(installerSize), destination: partial).run(installerURL)
        guard (try? verifyInstaller(partial)) == true else { throw Failure.checksum }
        if manager.fileExists(atPath: installer.path) {
            _ = try manager.replaceItemAt(installer, withItemAt: partial)
        } else {
            try manager.moveItem(at: partial, to: installer)
        }
        return installer
    }

    private static func verifyInstaller(_ url: URL) throws -> Bool {
        try verify(url, size: installerSize, digest: installerDigest)
    }

    static func verify(_ url: URL, size: Int, digest: String) throws -> Bool {
        let values = try url.resourceValues(forKeys: [.isRegularFileKey, .fileSizeKey])
        guard values.isRegularFile == true, values.fileSize == size else { return false }
        let file = try FileHandle(forReadingFrom: url)
        defer { try? file.close() }
        var hash = SHA256()
        while let data = try file.read(upToCount: 1024 * 1024), !data.isEmpty {
            hash.update(data: data)
        }
        return hash.finalize().map { String(format: "%02x", $0) }.joined() == digest
    }
}

/// Downloads to a fixed path and cancels as soon as the body is larger than
/// the pinned installer. URLSession's default 60-second idle timeout ends a
/// stalled transfer. All callbacks arrive on one serial queue.
private final class CappedDownload: NSObject, URLSessionDownloadDelegate, @unchecked Sendable {
    private let limit: Int64
    private let destination: URL
    private var failure: LocalCage.Failure?
    private var continuation: CheckedContinuation<Void, Error>?

    init(limit: Int64, destination: URL) {
        self.limit = limit
        self.destination = destination
    }

    func run(_ url: URL) async throws {
        let queue = OperationQueue()
        queue.maxConcurrentOperationCount = 1
        let session = URLSession(configuration: .ephemeral, delegate: self, delegateQueue: queue)
        defer { session.finishTasksAndInvalidate() }
        try await withCheckedThrowingContinuation { (continuation: CheckedContinuation<Void, Error>) in
            queue.addOperation {
                self.continuation = continuation
                session.downloadTask(with: url).resume()
            }
        }
    }

    func urlSession(_ session: URLSession, downloadTask: URLSessionDownloadTask, didWriteData bytesWritten: Int64,
                    totalBytesWritten: Int64, totalBytesExpectedToWrite: Int64) {
        if failure == nil, totalBytesWritten > limit || totalBytesExpectedToWrite > limit {
            failure = .checksum
            downloadTask.cancel()
        }
    }

    func urlSession(_ session: URLSession, downloadTask: URLSessionDownloadTask, didFinishDownloadingTo location: URL) {
        guard failure == nil else { return }
        guard (downloadTask.response as? HTTPURLResponse)?.statusCode == 200 else {
            failure = .download
            return
        }
        do {
            try? FileManager.default.removeItem(at: destination)
            try FileManager.default.moveItem(at: location, to: destination)
        } catch {
            failure = .download
        }
    }

    func urlSession(_ session: URLSession, task: URLSessionTask, didCompleteWithError error: Error?) {
        if let failure = failure ?? (error == nil ? nil : .download) {
            continuation?.resume(throwing: failure)
        } else {
            continuation?.resume()
        }
        continuation = nil
    }
}
