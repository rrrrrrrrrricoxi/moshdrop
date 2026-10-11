import AppKit
import Foundation
import UserNotifications
import Darwin

let bundleID = "io.github.rrrrrrrrrricoxi.moshdrop.notifier"
let readyCategory = "MOSHDROP_UPLOAD_READY"
let copyAction = "COPY_UPLOAD_PATHS"
let maxRecordBytes = 8 * 1024 * 1024
let retention: TimeInterval = 7 * 24 * 60 * 60

struct SavedUpload: Decodable {
    let version: Int
    let id: String
    let target: String
    let files: [String]
    let clipboard: String?
    let paths: [String]?
    let created: String
    let ok: Bool
}

enum HelperError: Error, CustomStringConvertible {
    case invalid(String)
    var description: String { switch self { case .invalid(let reason): return reason } }
}
func validID(_ value: String) -> Bool {
    value.utf8.count == 32 && value.utf8.allSatisfy { (48...57).contains($0) || (97...102).contains($0) }
}
func escapedPaths(_ paths: [String]) -> String {
    paths.map { path in
        path.unicodeScalars.map { scalar in
            let v = scalar.value
            let safe = [47, 46, 45, 95, 126].contains(v) || (48...57).contains(v) || (65...90).contains(v) || (97...122).contains(v) || v > 127
            return (safe ? "" : "\\") + String(scalar)
        }.joined()
    }.joined(separator: " ")
}
func privateDirectory(_ state: String) throws -> String {
    guard state.hasPrefix("/") else { throw HelperError.invalid("state directory must be absolute") }
    let dir = URL(fileURLWithPath: state).appendingPathComponent("pending").path
    var info = stat()
    guard lstat(dir, &info) == 0, info.st_mode & S_IFMT == S_IFDIR,
          info.st_mode & 0o777 == 0o700, info.st_uid == getuid() else {
        throw HelperError.invalid("pending directory is not owned and private")
    }
    return dir
}
func readUpload(state: String, id: String) throws -> SavedUpload {
    guard validID(id) else { throw HelperError.invalid("invalid upload ID") }
    let dir = try privateDirectory(state)
    let path = URL(fileURLWithPath: dir).appendingPathComponent(id + ".json").path
    let fd = Darwin.open(path, O_RDONLY | O_NOFOLLOW)
    guard fd >= 0 else { throw HelperError.invalid("saved upload is unavailable") }
    defer { Darwin.close(fd) }
    var info = stat()
    guard fstat(fd, &info) == 0, info.st_mode & S_IFMT == S_IFREG,
          info.st_mode & 0o777 == 0o600, info.st_uid == getuid(), info.st_nlink == 1,
          info.st_size <= maxRecordBytes else { throw HelperError.invalid("saved upload is not a private regular file") }
    let handle = FileHandle(fileDescriptor: fd, closeOnDealloc: false)
    let data = try handle.read(upToCount: maxRecordBytes + 1) ?? Data()
    guard data.count <= maxRecordBytes else { throw HelperError.invalid("saved upload exceeds size limit") }
    let result = try JSONDecoder().decode(SavedUpload.self, from: data)
    guard result.version == 1, result.id == id, result.ok,
          let text = result.clipboard, !text.isEmpty,
          let paths = result.paths, !paths.isEmpty, text == escapedPaths(paths) else { throw HelperError.invalid("no successful paths for this upload") }
    let parser = ISO8601DateFormatter()
    parser.formatOptions = [.withInternetDateTime, .withFractionalSeconds]
    var created = parser.date(from: result.created)
    if created == nil { parser.formatOptions = [.withInternetDateTime]; created = parser.date(from: result.created) }
    guard let created = created, Date().timeIntervalSince(created) <= retention else { throw HelperError.invalid("upload result expired from local history") }
    return result
}
func receipt(state: String, token: String, ok: Bool, error: String) {
    guard validID(token), let dir = try? privateDirectory(state),
          let data = try? JSONSerialization.data(withJSONObject: ["ok": ok, "error": error]) else { return }
    let path = URL(fileURLWithPath: dir).appendingPathComponent(".receipt-" + token + ".json").path
    // The Go caller creates and owns this short-lived receipt. Do not recreate
    // it after a caller timeout: that would leave unbounded orphan receipts.
    let fd = Darwin.open(path, O_WRONLY | O_NOFOLLOW)
    guard fd >= 0 else { return }
    var info = stat()
    guard fstat(fd, &info) == 0, info.st_mode & S_IFMT == S_IFREG,
          info.st_mode & 0o777 == 0o600, info.st_uid == getuid(), info.st_nlink == 1 else {
        Darwin.close(fd)
        return
    }
    _ = data.withUnsafeBytes { Darwin.write(fd, $0.baseAddress, $0.count) }
    _ = fsync(fd)
    _ = Darwin.close(fd)
}

final class Notifier: NSObject, NSApplicationDelegate, UNUserNotificationCenterDelegate {
    let args: [String]
    var center: UNUserNotificationCenter!
    init(args: [String]) { self.args = args }
    func applicationWillFinishLaunching(_ notification: Notification) {
        center = UNUserNotificationCenter.current()
        center.delegate = self
        let action = UNNotificationAction(identifier: copyAction, title: "Copy path", options: [])
        center.setNotificationCategories([UNNotificationCategory(identifier: readyCategory, actions: [action], intentIdentifiers: [], options: [.customDismissAction])])
    }
    func applicationDidFinishLaunching(_ notification: Notification) {
        // On-demand action handling, never an always-running service.
        if args.first != "--authorize" {
            DispatchQueue.main.asyncAfter(deadline: .now() + 15) { NSApp.terminate(nil) }
        }
        switch args.first ?? "--listen" {
        case "--notify":
            guard args.count == 4 else { NSApp.terminate(nil); return }
            let state = args[1], id = args[2], token = args[3]
            center.getNotificationSettings { settings in
                DispatchQueue.main.async {
                    guard settings.authorizationStatus == .authorized, settings.alertSetting == .enabled else {
                        receipt(state: state, token: token, ok: false, error: "alerts not authorized; use moshdrop notifications setup or pending")
                        NSApp.terminate(nil)
                        return
                    }
                    do {
                        let upload = try readUpload(state: state, id: id)
                        let content = UNMutableNotificationContent()
                        content.title = "moshdrop — upload ready"
                        content.subtitle = "\(upload.files.joined(separator: ", ")) → \(upload.target)"
                        content.body = "Not inserted automatically. Choose Copy path, then paste in the intended pane."
                        content.categoryIdentifier = readyCategory
                        content.userInfo = ["stateDir": state, "uploadID": id]
                        self.center.add(UNNotificationRequest(identifier: id, content: content, trigger: nil)) { error in
                            receipt(state: state, token: token, ok: error == nil, error: error?.localizedDescription ?? "")
                            DispatchQueue.main.async { NSApp.terminate(nil) }
                        }
                    } catch {
                        receipt(state: state, token: token, ok: false, error: String(describing: error))
                        NSApp.terminate(nil)
                    }
                }
            }
        case "--authorize":
            // Explicit manual setup only. Never called during an upload.
            center.requestAuthorization(options: [.alert]) { granted, error in
                print("notifications granted=\(granted) error=\(error?.localizedDescription ?? "")")
                DispatchQueue.main.async { NSApp.terminate(nil) }
            }
        case "--listen": break
        // macOS can supply its own launch arguments when handling an action.
        // Leave the delegate alive briefly instead of dropping the callback.
        default: break
        }
    }
    func userNotificationCenter(_ center: UNUserNotificationCenter, willPresent notification: UNNotification,
                                withCompletionHandler completionHandler: @escaping (UNNotificationPresentationOptions) -> Void) {
        completionHandler([.banner, .list])
    }
    func userNotificationCenter(_ center: UNUserNotificationCenter, didReceive response: UNNotificationResponse,
                                withCompletionHandler completionHandler: @escaping () -> Void) {
        DispatchQueue.main.async {
            guard response.actionIdentifier == copyAction else {
                completionHandler()
                NSApp.terminate(nil)
                return
            }
            let finish = { completionHandler(); NSApp.terminate(nil) }
            do {
                guard let state = response.notification.request.content.userInfo["stateDir"] as? String,
                      let id = response.notification.request.content.userInfo["uploadID"] as? String else {
                    throw HelperError.invalid("notification has no saved upload ID")
                }
                let upload = try readUpload(state: state, id: id)
                let board = NSPasteboard.general
                board.clearContents()
                guard board.setString(upload.clipboard!, forType: .string) else { throw HelperError.invalid("could not copy paths; use moshdrop pending --copy " + id) }
                self.feedback("Paths copied", "Use your terminal's normal Paste action.", done: finish)
            } catch {
                self.feedback("Could not copy upload paths", String(describing: error) + ". Use moshdrop pending to recover.", done: finish)
            }
        }
    }
    func feedback(_ title: String, _ body: String, done: @escaping () -> Void) {
        let content = UNMutableNotificationContent()
        content.title = "moshdrop — " + title
        content.body = body
        // No category: a feedback notification must not repeat Copy path.
        center.add(UNNotificationRequest(identifier: UUID().uuidString, content: content, trigger: nil)) { _ in
            DispatchQueue.main.async { done() }
        }
    }
}

@main
struct Main {
    static func main() {
        let args = Array(CommandLine.arguments.dropFirst())
        if args.first == "--selftest" {
            precondition(validID(String(repeating: "a", count: 32)))
            precondition(!validID("../unsafe"))
            precondition(!validID(String(repeating: "A", count: 32)))
            precondition(copyAction != UNNotificationDefaultActionIdentifier)
            precondition(copyAction != UNNotificationDismissActionIdentifier)
            precondition(escapedPaths(["/r/image A.png", "/r/图.png"]) == "/r/image\\ A.png /r/图.png")
            precondition(Bundle.main.bundleIdentifier == bundleID)
            print("PASS: 7 pure notifier checks; no permission, notification, or clipboard access")
            return
        }
        // A test-only mode validates a Go-produced private fixture without any UI.
        if args.first == "--check-record", args.count == 3 {
            do { let r = try readUpload(state: args[1], id: args[2]); print(r.clipboard!); return }
            catch { fputs("\(error)\n", stderr); exit(1) }
        }
        guard Bundle.main.bundleIdentifier == bundleID else {
            fputs("notification helper bundle identity is invalid\n", stderr)
            exit(1)
        }
        let app = NSApplication.shared
        app.setActivationPolicy(.accessory)
        let delegate = Notifier(args: args)
        app.delegate = delegate
        withExtendedLifetime(delegate) { app.run() }
    }
}
