import Foundation
import LocalAuthentication

/// What LocalAuthentication says about Face ID now: the enrolled faces' state hash, or why there is none.
public struct BiometryState: Equatable, Sendable {
    /// Changes when Face ID enrollment changes, which invalidates the key.
    public var stateHash: Data?
    /// canEvaluatePolicy's error when there is no hash: not enrolled, locked out, off for the app, ...
    public var error: LAError.Code?

    public init(stateHash: Data?, error: LAError.Code?) {
        self.stateHash = stateHash
        self.error = error
    }
}
