import AppKit

// NSApplication holds its delegate weakly; this global keeps it alive for the
// life of the process.
let delegate = AppDelegate()
NSApplication.shared.delegate = delegate
_ = NSApplicationMain(CommandLine.argc, CommandLine.unsafeArgv)
