import Foundation
import Security
import SwiftUI

/// A Cage server account: the HTTPS portal of a multi-tenant Cage server and
/// a portal user. Its virtual machines open in the same phone workbench as a
/// paired computer.
struct ServerAccount: Codable, Equatable, Hashable, Identifiable {
    let portal: String
    let user: String
    var id: String { portal + "|" + user }
    var host: String { URLComponents(string: portal)?.host ?? portal }
}

struct ServerVM: Decodable, Equatable, Identifiable {
    let id: String
    let name: String
    let system_label: String?
    let state: String
    let message: String?
    var stateLabel: String {
        switch state {
        case "ready": return "运行中"
        case "starting": return "正在启动"
        case "error": return "启动失败"
        default: return "已停止"
        }
    }
}

/// Saved server accounts, with the password when the user chose to keep it.
/// One device-only Keychain item, never synchronized or backed up.
enum ServerKeychain {
    private struct Record: Codable { let portal, user: String; let password: String? }
    private static var query: [String: Any] {
        [kSecClass as String: kSecClassGenericPassword, kSecAttrService as String: "io.github.sweetcornna.cage-tenant.mobile.servers",
         kSecAttrAccount as String: "v1", kSecAttrSynchronizable as String: false]
    }
    private static func records() throws -> [Record] {
        var q = query
        q[kSecReturnData as String] = true
        q[kSecMatchLimit as String] = kSecMatchLimitOne
        var found: CFTypeRef?
        let status = SecItemCopyMatching(q as CFDictionary, &found)
        if status == errSecItemNotFound {return []}
        guard status == errSecSuccess, let data = found as? Data else {throw InputError.unavailable}
        return try JSONDecoder().decode([Record].self, from: data)
    }
    private static func store(_ records: [Record]) throws {
        let values: [String: Any] = [kSecValueData as String: try JSONEncoder().encode(records),
                                     kSecAttrAccessible as String: kSecAttrAccessibleWhenUnlockedThisDeviceOnly]
        var status = SecItemUpdate(query as CFDictionary, values as CFDictionary)
        if status == errSecItemNotFound {
            status = SecItemAdd(query.merging(values) {_, value in value} as CFDictionary, nil)
        }
        guard status == errSecSuccess else {throw InputError.unavailable}
    }
    static func accounts() throws -> [ServerAccount] {
        try records().map {ServerAccount(portal: $0.portal, user: $0.user)}
    }
    static func password(_ account: ServerAccount) throws -> String? {
        try records().first {$0.portal == account.portal && $0.user == account.user}?.password
    }
    static func save(_ account: ServerAccount, password: String?) throws {
        var list = try records()
        list.removeAll {$0.portal == account.portal && $0.user == account.user}
        list.append(Record(portal: account.portal, user: account.user, password: password))
        try store(list)
    }
    static func forget(_ account: ServerAccount) throws {
        var list = try records()
        list.removeAll {$0.portal == account.portal && $0.user == account.user}
        try store(list)
    }
}

final class ServerModel: ObservableObject {
    @Published var accounts: [ServerAccount] = []
    @Published var current: ServerAccount?
    @Published var vms: [ServerVM] = []
    @Published var busy = false
    @Published var opening: ServerVM?
    @Published var status = ""
    weak var connections: ConnectionModel?
    private var sessions: [String: CMServer] = [:]
    private var generation = 0
    private let queue = DispatchQueue(label: "CageMobile.server", attributes: .concurrent)

    init() {
        do { accounts = try ServerKeychain.accounts() }
        catch { status = "无法读取已保存的服务器，请解锁手机后重新打开。" }
    }
    var signedIn: Bool { current.map {sessions[$0.id] != nil} ?? false }
    func hasPassword(_ account: ServerAccount) -> Bool { ((try? ServerKeychain.password(account)) ?? nil) != nil }
    private func begin(_ text: String) -> Int {
        generation += 1; busy = true; status = text
        return generation
    }
    private func finish(_ attempt: Int) -> Bool {
        guard attempt == generation else {return false}
        busy = false
        return true
    }

    func signIn(portal raw: String, user: String, password: String, remember: Bool) {
        guard !busy else {return}
        let attempt = begin("正在登录服务器…")
        queue.async {
            let result = Result { () throws -> CMServer in
                let portal = try CMServer.canonicalPortal(raw)
                return try CMServer.signIn(portal: portal, user: user, password: password)
            }
            DispatchQueue.main.async {
                guard self.finish(attempt) else {
                    if case .success(let late) = result { self.queue.async { late.close() } }
                    return
                }
                switch result {
                case .success(let server):
                    let account = ServerAccount(portal: server.portal, user: server.user)
                    let saved = (try? ServerKeychain.save(account, password: remember ? password : nil)) != nil
                    self.accounts.removeAll {$0 == account}; self.accounts.append(account)
                    self.signedIn(account, server)
                    if !saved { self.status = "已登录，但无法在这台手机上保存服务器。" }
                case .failure(let error): self.status = error.localizedDescription
                }
            }
        }
    }
    /// Signs in to a saved account with its remembered password. Returns false
    /// when the password was not kept, so the caller asks for it.
    @discardableResult func signIn(saved account: ServerAccount) -> Bool {
        if sessions[account.id] != nil { current = account; refresh(); return true }
        guard let password = (try? ServerKeychain.password(account)) ?? nil else { return false }
        signIn(portal: account.portal, user: account.user, password: password, remember: true)
        return true
    }
    private func signedIn(_ account: ServerAccount, _ server: CMServer) {
        if let old = sessions[account.id], old !== server { queue.async { old.close() } }
        sessions[account.id] = server
        current = account; vms = []; status = ""
        refresh()
    }
    func refresh() {
        guard let account = current, let server = sessions[account.id], !busy else {return}
        let attempt = begin("正在读取虚拟机…")
        queue.async {
            let result = Result { () throws -> [ServerVM] in
                let list = try server.virtualMachines()
                return try JSONDecoder().decode([ServerVM].self, from: Data(list.utf8))
            }
            DispatchQueue.main.async {
                guard self.finish(attempt), self.current == account else {return}
                switch result {
                case .success(let list): self.vms = list; self.status = list.isEmpty ? "这个账户还没有虚拟机。" : ""
                case .failure(let error): self.status = error.localizedDescription
                }
            }
        }
    }
    func create(name: String) {
        let name = name.trimmingCharacters(in: .whitespacesAndNewlines)
        guard let account = current, let server = sessions[account.id], !busy, !name.isEmpty else {return}
        let attempt = begin("正在新建虚拟机…")
        queue.async {
            let result = Result { () throws -> ServerVM in
                try JSONDecoder().decode(ServerVM.self, from: Data(try server.createVirtualMachine(name: name).utf8))
            }
            DispatchQueue.main.async {
                guard self.finish(attempt), self.current == account else {return}
                switch result {
                case .success(let vm): self.vms.append(vm); self.status = ""; self.open(vm)
                case .failure(let error): self.status = error.localizedDescription
                }
            }
        }
    }
    func deviceID(_ account: ServerAccount, _ vm: ServerVM) -> String { "server|" + account.id + "|" + vm.id }
    func open(_ vm: ServerVM) {
        guard let account = current, let server = sessions[account.id], let connections, !busy else {return}
        let id = deviceID(account, vm)
        if connections.devices.contains(where: {$0.id == id}) { connections.selectDevice(id); return }
        guard let directory = Bundle.main.url(forResource: "mobile-ui", withExtension: nil) else { status = "安装包缺少工作台资源。"; return }
        let attempt = begin(vm.state == "ready" ? "正在打开虚拟机…" : "正在启动虚拟机，首次启动可能需要几分钟…")
        opening = vm
        queue.async {
            let result = Result { try server.open(virtualMachine: vm.id, assets: directory.path) }
            DispatchQueue.main.async {
                guard self.finish(attempt) else {
                    if case .success(let late) = result { self.queue.async { late.close() } }
                    return
                }
                self.opening = nil
                switch result {
                case .success(let connection):
                    self.status = ""
                    connections.addServerDevice(connection, id: id, name: vm.name, account: account)
                case .failure(let error): self.status = error.localizedDescription; self.refresh()
                }
            }
        }
    }
    func cancelOpening() {
        guard opening != nil else {return}
        generation += 1; busy = false; opening = nil; status = "已取消打开。"
    }
    func leave() { current = nil; vms = []; status = "" }
    /// Signing out revokes the server's VM sessions, so their workbenches close too.
    func signOut() {
        guard let account = current else {return}
        generation += 1; busy = false; opening = nil
        connections?.disconnectServer(account)
        if let server = sessions.removeValue(forKey: account.id) { queue.async { server.close() } }
        leave()
    }
    func forget(_ account: ServerAccount) {
        do { try ServerKeychain.forget(account) } catch { status = "移除未完成，请解锁手机后重试。"; return }
        if current == account { signOut() }
        else if let server = sessions.removeValue(forKey: account.id) {
            connections?.disconnectServer(account)
            queue.async { server.close() }
        }
        accounts.removeAll {$0 == account}
        status = "已忘记 " + account.user + " @ " + account.host + "。"
    }
}


struct ServerSheet: View {
    @ObservedObject var model: ServerModel
    @ObservedObject var connections: ConnectionModel
    @State private var portal = ""
    @State private var user = ""
    @State private var password = ""
    @State private var remember = true
    @State private var naming = false
    @State private var newName = ""
    @State private var confirmingSignOut = false
    @State private var forgetting: ServerAccount?

    var body: some View {
        NavigationStack {
            Group {
                if model.signedIn, let account = model.current { machines(account) } else { signIn }
            }
            .navigationBarTitleDisplayMode(.inline)
        }
        .interactiveDismissDisabled(model.opening != nil)
        .presentationDetents([.large]).presentationDragIndicator(.visible)
    }

    private var signIn: some View {
        Form {
            Section {
                VStack(spacing: 8) {
                    Image(systemName: "server.rack").font(.system(size: 40, weight: .light))
                    Text("连接服务器").font(.title2.bold())
                    Text("登录多租户 Cage 服务器的门户账户，打开你的虚拟机。").font(.subheadline)
                        .foregroundStyle(.secondary).multilineTextAlignment(.center)
                }.frame(maxWidth: .infinity).padding(.vertical, 8)
            }.listRowBackground(Color.clear)
            if !model.accounts.isEmpty {
                Section("已保存的服务器") {
                    ForEach(model.accounts) {account in
                        Button {
                            if !model.signIn(saved: account) {
                                portal = account.portal; user = account.user; password = ""
                                model.status = "请输入 " + account.user + " 的密码。"
                            }
                        } label: {
                            HStack {
                                Image(systemName: "server.rack")
                                VStack(alignment: .leading) {Text(account.user); Text(account.host).font(.caption).foregroundStyle(.secondary)}
                                Spacer(); Image(systemName: "chevron.right").foregroundStyle(.tertiary)
                            }.contentShape(Rectangle())
                        }.disabled(model.busy)
                        .swipeActions {Button("忘记", role: .destructive) {forgetting = account}}
                        .contextMenu {Button("忘记这台服务器", role: .destructive) {forgetting = account}}
                    }
                }
            }
            Section(model.accounts.isEmpty ? "服务器账户" : "登录其他账户") {
                TextField("服务器地址，例如 https://cage.example.org", text: $portal)
                    .keyboardType(.URL).textInputAutocapitalization(.never).autocorrectionDisabled()
                    .textContentType(.URL)
                TextField("账户", text: $user).textInputAutocapitalization(.never).autocorrectionDisabled()
                    .textContentType(.username)
                SecureField("密码", text: $password).textContentType(.password)
                Toggle("在这台手机上记住密码", isOn: $remember)
                Button {
                    model.signIn(portal: portal, user: user, password: password, remember: remember)
                    password = ""
                } label: {
                    HStack {Spacer(); if model.busy {ProgressView()} else {Text("登录").bold()}; Spacer()}
                }.disabled(model.busy || portal.trimmingCharacters(in: .whitespaces).isEmpty || user.isEmpty || password.isEmpty)
            }
            if !model.status.isEmpty {Section {Text(model.status).font(.footnote).foregroundStyle(.secondary)}}
        }
        .navigationTitle("服务器")
        .alert("忘记这台服务器？", isPresented: Binding(get: {forgetting != nil}, set: {if !$0 {forgetting = nil}}), presenting: forgetting) {account in
            Button("取消", role: .cancel) {}
            Button("忘记", role: .destructive) {model.forget(account)}
        } message: {account in Text("将从这台手机移除 " + account.user + " @ " + account.host + " 和保存的密码。")}
    }

    private func machines(_ account: ServerAccount) -> some View {
        List {
            Section {
                ForEach(model.vms) {vm in
                    let open = connections.devices.contains {$0.id == model.deviceID(account, vm)}
                    Button {model.open(vm)} label: {
                        HStack(spacing: 12) {
                            Image(systemName: "shippingbox").font(.title3).frame(width: 28)
                            VStack(alignment: .leading, spacing: 2) {
                                Text(vm.name).font(.body.weight(.medium))
                                Text([vm.system_label, open ? "已打开" : vm.stateLabel].compactMap {$0}.joined(separator: " · "))
                                    .font(.caption).foregroundStyle(vm.state == "error" ? .red : .secondary)
                            }
                            Spacer()
                            if model.opening?.id == vm.id {ProgressView()} else {Image(systemName: "chevron.right").foregroundStyle(.tertiary)}
                        }.frame(minHeight: 48).contentShape(Rectangle())
                    }.disabled(model.busy)
                }
                Button {newName = ""; naming = true} label: {Label("新建虚拟机", systemImage: "plus")}.disabled(model.busy)
            } header: {
                Text(account.user + " @ " + account.host)
            } footer: {
                Text("打开的虚拟机显示 Claude Code 项目、会话和工具审批。远程浏览器与 Claude Desktop 需要扫码连接电脑。")
            }
            if !model.status.isEmpty {
                Section {
                    Text(model.status).font(.footnote).foregroundStyle(.secondary)
                    if model.opening != nil {Button("取消打开", role: .cancel, action: model.cancelOpening)}
                }
            }
        }
        .refreshable {model.refresh()}
        .navigationTitle("虚拟机")
        .toolbar {
            ToolbarItem(placement: .topBarLeading) {Button("返回") {model.leave()}.disabled(model.opening != nil)}
            ToolbarItem(placement: .topBarTrailing) {
                Menu {
                    Button {model.refresh()} label: {Label("刷新", systemImage: "arrow.clockwise")}
                    Button(role: .destructive) {confirmingSignOut = true} label: {Label("退出登录", systemImage: "rectangle.portrait.and.arrow.right")}
                } label: {Image(systemName: "ellipsis.circle")}.disabled(model.opening != nil)
            }
        }
        .alert("新建虚拟机", isPresented: $naming) {
            TextField("名称", text: $newName)
            Button("取消", role: .cancel) {}
            Button("新建并打开") {model.create(name: newName)}
        } message: {Text("服务器为新虚拟机准备空白环境，首次打开需要几分钟。")}
        .alert("退出登录？", isPresented: $confirmingSignOut) {
            Button("取消", role: .cancel) {}
            Button("退出登录", role: .destructive, action: model.signOut)
        } message: {Text("将关闭这个账户已打开的虚拟机工作台。虚拟机会继续在服务器上运行。")}
    }
}
