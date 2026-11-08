import SwiftUI
import AimuxKit

/// Shown when the Mac has device login and this iPhone holds no paired key: the code from
/// `aimux remote pair`, then Face ID makes the key and signs the pairing.
struct PairingView: View {
    let login: DeviceLogin
    let reason: UnpairedReason?
    @Binding var serverAddress: String
    @State private var code = ""
    @State private var showingSettings = false

    var body: some View {
        LoginScreen(title: "Pair this device") {
            if let note {
                Text(note)
                    .font(.body.weight(.semibold))
                    .accessibilityIdentifier("pairNote")
            }
            // The command on its own line, so it never breaks in the middle on a small iPhone.
            VStack(alignment: .leading, spacing: 4) {
                Text("On your Mac, run")
                Text(verbatim: "aimux remote pair --name <name>")
                    .font(.body.monospaced())
                    .foregroundStyle(.primary)
                    .lineLimit(1)
                    .minimumScaleFactor(0.6)
                    .textSelection(.enabled)
                Text("and type the code it shows.")
            }
            .foregroundStyle(.secondary)
            .accessibilityElement(children: .combine)
            VStack(alignment: .leading, spacing: 6) {
                Text("Pairing code")
                    .font(.subheadline)
                    .foregroundStyle(.secondary)
                    .accessibilityHidden(true)
                TextField("XXXX-XXXX", text: $code)
                    .font(.title3.monospaced())
                    .textInputAutocapitalization(.characters)
                    .autocorrectionDisabled()
                    .textContentType(.oneTimeCode)
                    .submitLabel(.go)
                    .onSubmit(pair)
                    .padding(12)
                    .background(Color(.secondarySystemGroupedBackground), in: .rect(cornerRadius: 12))
                    .overlay(RoundedRectangle(cornerRadius: 12).stroke(Color(.separator)))
                    .accessibilityLabel("Pairing code")
                    .accessibilityIdentifier("pairCode")
            }
            LoginButton(title: login.busy ? "Pairing…" : "Pair this device", systemImage: nil, action: pair)
                .disabled(login.busy || code.trimmingCharacters(in: .whitespaces).isEmpty)
                .accessibilityIdentifier("pairButton")
            LoginMessage(text: login.message)
            Button("Change Mac address") { showingSettings = true }
                .font(.subheadline)
                .tint(.secondary)
                .padding(.top, 8)
                .accessibilityIdentifier("changeServer")
        }
        .sheet(isPresented: $showingSettings) {
            NavigationStack { ServerSettingsView(serverAddress: $serverAddress, isFirstRun: false) }
        }
    }

    private var note: String? {
        switch reason {
        case .revoked: "This device is no longer paired."
        case .keyChanged: "This iPhone needs pairing again."
        case nil: nil
        }
    }

    private func pair() {
        Task { await login.pair(code: code) }
    }
}

/// The pairing and unlock screens' layout: a large title, then the screen's content, on the grouped background.
struct LoginScreen<Content: View>: View {
    let title: String
    @ViewBuilder var content: Content
    @ScaledMetric(relativeTo: .largeTitle) private var titleSize: CGFloat = 32

    var body: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 16) {
                Text(title)
                    .font(.system(size: titleSize, weight: .bold))
                    .accessibilityAddTraits(.isHeader)
                content
            }
            .frame(maxWidth: 520, alignment: .leading)
            .padding(.horizontal, 20)
            .padding(.top, 48)
            .frame(maxWidth: .infinity, alignment: .leading)
        }
        .scrollBounceBehavior(.basedOnSize)
        .background(Color(.systemGroupedBackground).ignoresSafeArea())
    }
}

/// Full-width, black in light mode and white in dark, like the web client's. Disabled, it fades to a grey
/// that keeps its text above 3:1 contrast in both modes.
struct LoginButton: View {
    let title: String
    let systemImage: String?
    let action: () -> Void

    var body: some View {
        Button(action: action) {
            Group {
                if let systemImage {
                    Label(title, systemImage: systemImage)
                } else {
                    Text(title)
                }
            }
            .font(.headline)
            .foregroundStyle(Color(.systemBackground))
            .frame(maxWidth: .infinity, minHeight: 50)
        }
        .buttonStyle(LoginButtonStyle())
        .padding(.top, 4)
    }
}

private struct LoginButtonStyle: ButtonStyle {
    @Environment(\.isEnabled) private var isEnabled

    func makeBody(configuration: Configuration) -> some View {
        configuration.label
            .background(Color.primary.opacity(isEnabled ? (configuration.isPressed ? 0.8 : 1) : 0.45),
                        in: .rect(cornerRadius: 14))
    }
}

/// The screen's one error line, read out by VoiceOver when it appears.
struct LoginMessage: View {
    let text: String?

    var body: some View {
        if let text {
            Text(text)
                .font(.subheadline)
                .foregroundStyle(.red)
                .fixedSize(horizontal: false, vertical: true)
                .accessibilityIdentifier("loginMessage")
                .onAppear { AccessibilityNotification.Announcement(text).post() }
                .onChange(of: text) { _, new in AccessibilityNotification.Announcement(new).post() }
        }
    }
}
