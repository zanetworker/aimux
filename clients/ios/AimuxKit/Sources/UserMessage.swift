import Foundation

public struct UserMessage: Equatable, Sendable {
    public let title: String
    public let detail: String
}

extension APIError {
    /// What the app tells a person when a request fails.
    public var userMessage: UserMessage {
        switch self {
        case .transport(.notConnectedToInternet):
            UserMessage(title: "You're offline", detail: "Connect to the internet, then pull to refresh.")
        case .transport:
            UserMessage(title: "Can't reach your Mac",
                        detail: "Check that Tailscale is on, on this iPhone and on the Mac, and that the Mac is awake.")
        case .http(502), .http(503), .http(504):
            UserMessage(title: "aimux isn't running",
                        detail: "Tailscale reached your Mac, but nothing answers there. "
                            + "Run aimux remote serve on the Mac.")
        case .server(_, .forbidden, let message):
            UserMessage(title: "This login isn't allowed",
                        detail: "\(message). Allow your Tailscale login on the Mac with aimux remote serve --allow.")
        case .server(_, .unauthorized, _):
            UserMessage(title: "No Tailscale identity",
                        detail: "Open the server through tailscale serve, signed in to the same Tailscale account.")
        case .server(_, .loginRequired, _):
            UserMessage(title: "This device is no longer paired",
                        detail: "Pair it again with the code aimux remote pair shows on your Mac.")
        case .server(_, let code, let message) where code.terminalReason != nil:
            UserMessage(title: terminalReasonText(code.terminalReason), detail: message)
        case .server(_, .badText, let message):
            UserMessage(title: "The terminal can't take that text", detail: message)
        case .server(_, .notSubmitted, let message):
            UserMessage(title: "Your message is in the box but not sent; a prompt appeared", detail: message)
        case .server(let status, _, let message):
            UserMessage(title: "Something went wrong", detail: "\(message) (HTTP \(status))")
        case .http(401):
            UserMessage(title: "Something went wrong", detail: "Try again.")
        case .http(let status):
            UserMessage(title: "Something went wrong", detail: "The server answered HTTP \(status).")
        case .decoding:
            UserMessage(title: "Unexpected answer",
                        detail: "The server's reply could not be read. Is the address an aimux server?")
        }
    }
}

extension APIErrorCode {
    /// The `terminal.reason` this refusal stands for; nil for codes that are not about reaching the terminal.
    var terminalReason: String? {
        switch self {
        case .disabled: "disabled"
        case .notInTerminal: "not_in_terminal"
        case .notClaude: "not_claude"
        case .macInUse: "mac_in_use"
        case .noTerminalDriver: "no_terminal_driver"
        case .automationDenied: "automation_denied"
        case .notAtInput: "not_at_input"
        default: nil
        }
    }
}
