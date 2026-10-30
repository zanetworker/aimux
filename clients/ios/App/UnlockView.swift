import SwiftUI
import AimuxKit

/// Shown when this iPhone is paired but has no session (launch, an hour without use, Lock). After a failed
/// unlock it offers pairing again; another Mac address is always reachable.
struct UnlockView: View {
    let login: DeviceLogin
    @Binding var serverAddress: String
    @State private var showingSettings = false
    @State private var confirmingPairAgain = false
    @Environment(\.scenePhase) private var scenePhase

    var body: some View {
        LoginScreen(title: "aimux is locked") {
            Text("Unlock to pick up where you left off.")
                .foregroundStyle(.secondary)
            LoginButton(title: "Unlock with Face ID", systemImage: "faceid") { Task { await login.unlock() } }
                .disabled(login.busy)
                .accessibilityIdentifier("unlockButton")
            LoginMessage(text: login.message)
            if login.canPairAgain {
                Button("Pair this device again") { confirmingPairAgain = true }
                    .font(.subheadline.weight(.semibold))
                    .tint(.primary)
                    .accessibilityIdentifier("pairAgainButton")
            }
            Button("Change Mac address") { showingSettings = true }
                .font(.subheadline)
                .tint(.secondary)
                .padding(.top, 8)
                .accessibilityIdentifier("changeServer")
        }
        .confirmationDialog("Pair this device again?", isPresented: $confirmingPairAgain, titleVisibility: .visible) {
            Button("Pair Again", role: .destructive) { login.pairAgain() }
        } message: {
            Text("The old pairing stops working. Revoke its entry on your Mac with aimux remote revoke.")
        }
        .sheet(isPresented: $showingSettings) {
            NavigationStack { ServerSettingsView(serverAddress: $serverAddress, isFirstRun: false) }
        }
        // Face ID right away when the lock came by itself; after Lock, only on a tap.
        .task { await promptIfDue() }
        .onChange(of: scenePhase) { Task { await promptIfDue() } }
    }

    private func promptIfDue() async {
        guard scenePhase == .active, login.promptOnAppear else { return }
        await login.unlock()
    }
}
