import SwiftUI
import AimuxKit

struct ServerSettingsView: View {
    @Binding var serverAddress: String
    let isFirstRun: Bool
    @State private var draft = ""
    @Environment(\.dismiss) private var dismiss
    @Environment(DeviceLogin.self) private var login: DeviceLogin?

    private var parsed: URL? { ServerAddress.parse(draft) }

    var body: some View {
        Form {
            Section {
                TextField("your-mac.your-tailnet.ts.net:8443", text: $draft)
                    .textInputAutocapitalization(.never)
                    .autocorrectionDisabled()
                    .keyboardType(.URL)
                    .submitLabel(.go)
                    .onSubmit(connect)
                    .accessibilityIdentifier("serverField")
            } header: {
                Text("Mac address")
            } footer: {
                Text("The address tailscale serve gives aimux remote serve on your Mac. "
                     + "This iPhone must be signed in to the same Tailscale account.")
            }
            Section {
                Button("Connect", action: connect)
                .disabled(parsed == nil)
                .accessibilityIdentifier("connectButton")
            }
            if !isFirstRun, let login, login.state == .live {
                Section {
                    Button("Lock") {
                        dismiss()
                        Task { await login.lock() }
                    }
                    .accessibilityIdentifier("lockButton")
                } footer: {
                    Text("Ends this login. Unlocking needs Face ID.")
                }
            }
        }
        .navigationTitle(isFirstRun ? "Connect to your Mac" : "Server")
        .navigationBarTitleDisplayMode(.inline)
        .toolbar {
            if !isFirstRun {
                ToolbarItem(placement: .cancellationAction) { Button("Cancel") { dismiss() } }
            }
        }
        .onAppear { draft = serverAddress }
    }

    private func connect() {
        guard let parsed else { return }
        serverAddress = parsed.absoluteString
        if !isFirstRun { dismiss() }
    }
}
