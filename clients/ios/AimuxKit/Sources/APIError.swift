import Foundation

public enum APIErrorCode: Equatable, Hashable, Sendable {
    case unauthorized, forbidden, badRequest, unknownSession, noSuchApproval, notSupported
    case openInTerminal, busy, inUse, notRunning, invalidOption, ownershipUnknown, tooManyRuns, `internal`
    // Typing into a terminal on the Mac.
    case disabled, badText, notInTerminal, notClaude, macInUse, noTerminalDriver, automationDenied, notAtInput
    case noPrompt, promptChanged, notSubmitted
    // The chat queue.
    case queueFull, itemSending, unknownItem
    // Device login.
    case loginRequired, pairingFailed, pairingConflict, unknownDevice, loginFailed
    // Titles, descriptions and Update summary.
    case ownerSet, describeRunning, noContent
    case namingTimeout, namingAuth, namingModel, namingUnreadable, namingFailed, namingUnavailable
    case unknown(String)

    private static let known: [String: APIErrorCode] = [
        "unauthorized": .unauthorized, "forbidden": .forbidden, "bad_request": .badRequest,
        "unknown_session": .unknownSession, "no_such_approval": .noSuchApproval, "not_supported": .notSupported,
        "open_in_terminal": .openInTerminal, "busy": .busy, "in_use": .inUse, "not_running": .notRunning,
        "invalid_option": .invalidOption, "ownership_unknown": .ownershipUnknown, "too_many_runs": .tooManyRuns,
        "internal": .internal, "disabled": .disabled, "bad_text": .badText, "not_in_terminal": .notInTerminal,
        "not_claude": .notClaude, "mac_in_use": .macInUse, "no_terminal_driver": .noTerminalDriver,
        "automation_denied": .automationDenied, "not_at_input": .notAtInput, "no_prompt": .noPrompt,
        "prompt_changed": .promptChanged, "not_submitted": .notSubmitted,
        "queue_full": .queueFull, "item_sending": .itemSending, "unknown_item": .unknownItem,
        "login_required": .loginRequired, "pairing_failed": .pairingFailed, "pairing_conflict": .pairingConflict,
        "unknown_device": .unknownDevice, "login_failed": .loginFailed,
        "owner_set": .ownerSet, "describe_running": .describeRunning, "no_content": .noContent,
        "naming_timeout": .namingTimeout, "naming_auth": .namingAuth, "naming_model": .namingModel,
        "naming_unreadable": .namingUnreadable, "naming_failed": .namingFailed,
        "naming_unavailable": .namingUnavailable
    ]

    public init(rawValue: String) {
        self = Self.known[rawValue] ?? .unknown(rawValue)
    }
}

public enum APIError: Error, Equatable, Sendable {
    /// The server answered with its ErrorBody.
    case server(status: Int, code: APIErrorCode, message: String)
    /// A non-2xx answer without an ErrorBody (tailscale serve's 502 when aimux is down).
    case http(status: Int)
    /// No answer: offline, not on the tailnet, DNS, TLS.
    case transport(URLError.Code)
    /// A 2xx answer the app could not read.
    case decoding(String)

    private struct Body: Decodable { let error: String; let code: String }

    public static func from(status: Int, body: Data) -> APIError {
        guard let parsed = try? JSONDecoder().decode(Body.self, from: body) else { return .http(status: status) }
        return .server(status: status, code: APIErrorCode(rawValue: parsed.code), message: parsed.error)
    }
}
