import SwiftUI
import AimuxKit

/// The chat itself: header, timeline, Next bar and composer.
struct ChatContent: View {
    let session: Session
    let context: ChatContext
    let model: ChatModel
    let goTo: (Session) -> Void

    /// Kept per session in `context.drafts`, so closing the chat does not lose it.
    @State private var draft = ""
    @State private var showingInfo = DebugHooks.openInfo
    @State private var openSteps: Set<Int> = []
    @State private var openThoughts: Set<Int> = []
    /// Per steps item, the steps whose detail is open.
    @State private var openStepDetails: [Int: Set<Int>] = [:]
    /// The option tapped for each approval, to name the outcome once it is answered.
    @State private var chosen: [String: PermissionOption] = [:]
    /// VoiceOver focus on the message field.
    @AccessibilityFocusState private var composerFocused: Bool
    /// The chat's height, so the queue list takes at most a third of it.
    @State private var height: CGFloat = 0
    /// The media card playing in this chat: one at a time.
    @State private var playback = MediaPlayback()
    /// The newest content is out of view: the Latest button shows.
    @State private var awayFromLatest = false
    /// Where the timeline is scrolled; the Latest button moves it to the end.
    @State private var position = ScrollPosition()

    private var result: TimelineResult { model.result }
    private var title: String {
        context.now.listedSession(session.id)?.displayTitle ?? model.session?.displayTitle ?? session.displayTitle
    }
    private var project: String { SessionRowFormat().projectName(model.session?.project ?? session.project) }
    private var next: NowItem? { nextSession(in: context.now.view, excluding: session.id) }

    var body: some View {
        ScrollView {
            // Not lazy: a lazy stack guesses the heights of rows it has not drawn, and when a tall reply grows
            // while scrolled up (a read every 2 s while Claude works) it can scroll into empty space, a black
            // chat. The chat holds at most `ChatModel.historyTurns` turns, so drawing them all stays cheap.
            VStack(alignment: .leading, spacing: 16) {
                ForEach(timelineRows(result.items)) { row in
                    self.row(row.item, at: row.index)
                }
                if let pending = model.pending, model.state != .readOnly {
                    PendingMessageRow(pending: pending, model: model)
                }
                if let prompt = terminalPrompt { terminalPromptRow(prompt) }
                if terminalPrompt == nil, let line = terminalWorkingLine(model.session) { WorkingLine(text: line) }
                // The end of the timeline: out of view, the Latest button shows.
                Color.clear.frame(height: 1)
                    .onScrollVisibilityChange(threshold: 0.01) { visible in awayFromLatest = !visible }
            }
            .padding(.horizontal, 16)
            .padding(.vertical, 14)
        }
        .defaultScrollAnchor(DebugHooks.chatAnchor)
        .scrollPosition($position)
        .environment(\.referenceContext, ReferenceContext(session: model.session, links: context.links))
        .environment(playback)
        .scrollDismissesKeyboard(.interactively)
        .overlay { emptyState }
        .background(Color(.systemBackground))
        .safeAreaInset(edge: .top, spacing: 0) { ChatTopLine(model: model) }
        .safeAreaInset(edge: .bottom, spacing: 0) {
            // The Latest button takes its own row above the composer, so it never covers the timeline.
            VStack(spacing: 0) {
                latestButton
                bottomBar
            }
        }
        .onGeometryChange(for: CGFloat.self, of: { $0.size.height }, action: { height = $0 })
        .navigationBarTitleDisplayMode(.inline)
        .toolbar(.hidden, for: .tabBar)
        .toolbar {
            ToolbarItem(placement: .principal) {
                Button { showingInfo = true } label: { header }
                    .buttonStyle(.plain)
            }
            .sharedBackgroundVisibility(.hidden)
            ToolbarItem(placement: .topBarTrailing) {
                Button { showingInfo = true } label: { Image(systemName: "info.circle") }
                    .accessibilityLabel("Session info")
                    .accessibilityIdentifier("infoButton")
            }
        }
        .sheet(isPresented: $showingInfo) {
            SessionInfoSheet(session: session, detail: model.session, now: context.now)
        }
        .onAppear { draft = context.drafts.draft(for: session.id) }
        .onChange(of: draft) {
            context.drafts.setDraft(draft, for: session.id)
            if model.addNotice != nil { model.clearAddNotice() }  // it was about the text before this change
        }
        .onChange(of: model.pending) { reclaimRefused() }
        .onChange(of: model.addNotice) { _, notice in
            if let notice { AccessibilityNotification.Announcement(notice).post() }
        }
    }

    @ViewBuilder private var latestButton: some View {
        if awayFromLatest {
            LatestButton { withAnimation { position.scrollTo(edge: .bottom) } }
                .frame(maxWidth: .infinity, alignment: .trailing)
                .padding(.horizontal, 12)
                .padding(.top, 4)
                .background(Color(.systemBackground))
        }
    }

    // MARK: Header

    private var header: some View {
        VStack(spacing: 1) {
            Text(bidi: isolated(title))
                .font(.headline)
                .lineLimit(2)
                .multilineTextAlignment(.center)
            if let status {
                HStack(spacing: 5) {
                    StatusDot(status: status.dot)
                    Text(bidi: "\(status.label) · \(isolated(project))").lineLimit(1)
                }
                .font(.caption)
                .foregroundStyle(.secondary)
            }
        }
        .accessibilityElement(children: .combine)
        .accessibilityIdentifier("chatHeader")
    }

    /// "Working", "Your turn", "Failed", "Did not start", or "Open on your Mac · 14h ago".
    private var status: (dot: DotStatus, label: String)? {
        guard let status = chatStatus(detail: model.session, readOnly: model.state == .readOnly, result: result,
                                      now: .now) else { return nil }
        let dot: DotStatus = switch status.tone {
        case .idle: .idle
        case .needsYou: .needsYou
        case .working: .working
        case .failed: .failed
        }
        return (dot, status.label)
    }

    // MARK: Timeline

    @ViewBuilder private func row(_ item: TimelineItem, at index: Int) -> some View {
        switch item {
        case .user(let text):
            SavedUserMessage(text: text)
        case .thought(let text, let seconds):
            ThoughtRow(text: text, seconds: seconds, live: result.working && isLastTurn(index),
                       isOpen: toggle($openThoughts, index, initially: DebugHooks.openSteps))
        case .steps(let group):
            stepsCard(group, at: index)
        case .plan(let plan):
            PlanCard(plan: plan)
        case .reply(let text):
            ReplyText(text: text)
        case .approval(let approval):
            approvalRow(approval)
        case .error(let text):
            TimelineErrorLine(text: text)
        case .stopped:
            Text("Stopped").font(.subheadline).foregroundStyle(.secondary)
        }
    }

    /// Whether the item belongs to the last turn (after the last message you sent).
    private func isLastTurn(_ index: Int) -> Bool {
        let lastUser = result.items.lastIndex { if case .user = $0 { true } else { false } } ?? -1
        return index > lastUser
    }

    private func toggle(_ set: Binding<Set<Int>>, _ index: Int, initially: Bool = false) -> Binding<Bool> {
        Binding(
            get: { set.wrappedValue.contains(index) != initially },
            set: { isOn in
                if isOn != initially { set.wrappedValue.insert(index) } else { set.wrappedValue.remove(index) }
            }
        )
    }

    @ViewBuilder private func approvalRow(_ item: ApprovalItem) -> some View {
        if let outcome = outcome(item) {
            ResolvedApprovalLine(outcome: outcome, detail: item.command ?? item.title)
        } else {
            ChatApprovalCard(item: item, isAnswering: model.answering.contains(item.approvalId),
                             error: model.answerErrors[item.approvalId]) { option in
                chosen[item.approvalId] = option
                Task {
                    await model.answer(item.approval, option: option)
                    // The answered session may leave Needs you: update the Next bar now, not at the next poll.
                    await context.now.refresh()
                }
            }
        }
    }

    /// "Allowed", "Denied", "Expired" once resolved; nil while it waits for an answer.
    private func outcome(_ item: ApprovalItem) -> String? {
        if let outcome = item.outcome {
            switch outcome {
            case "allowed": return "Allowed"
            case "denied": return "Denied"
            case "expired": return "Expired"
            default: return outcome.capitalized
            }
        }
        guard model.answered.contains(item.approvalId) else { return nil }
        guard let option = chosen[item.approvalId] else { return "Answered" }
        return option.kind.hasPrefix("reject") ? "Denied" : "Allowed"
    }

    /// The permission prompt showing in the terminal on the Mac, until it is answered here.
    private var terminalPrompt: TerminalPrompt? {
        guard model.state == .terminal, let prompt = model.session?.terminal?.prompt,
              !model.answeredPrompts.contains(prompt.promptId) else { return nil }
        return prompt
    }

    private func terminalPromptRow(_ prompt: TerminalPrompt) -> some View {
        ChatApprovalCard(item: prompt.approvalItem, isAnswering: model.answering.contains(prompt.promptId),
                         error: model.answerErrors[prompt.promptId]) { choice in
            guard let option = prompt.options.first(where: { $0.key == choice.optionId }) else { return }
            Task {
                await model.answer(prompt, option: option)
                await context.now.refresh()
            }
        }
    }

    @ViewBuilder private var emptyState: some View {
        switch model.state {
        case .loading where result.items.isEmpty:
            ProgressView()
        case .failed(let message):
            VStack(spacing: 12) {
                Text(message)
                    .font(.subheadline)
                    .foregroundStyle(.secondary)
                    .multilineTextAlignment(.center)
                Button("Retry") { Task { await Interaction.perform { await model.load() } } }
                    .buttonStyle(.bordered)
                    .tint(.primary)
                    .accessibilityIdentifier("chatRetry")
            }
            .padding(32)
        default:
            EmptyView()
        }
    }

    private func send() {
        let text = draft.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !text.isEmpty else { return }
        draft = ""
        withAnimation { position.scrollTo(edge: .bottom) }  // to the message just sent
        Task { await model.send(text) }
    }
}

extension ChatContent {
    // MARK: Bottom

    @ViewBuilder fileprivate var bottomBar: some View {
        VStack(spacing: 8) {
            if let next, result.pendingApproval == nil {
                NextBar(item: next) { goTo(next.session) }
            }
            if let stopError = model.stopError {
                Text("Could not stop · " + stopError.userMessage.title)
                    .font(.footnote)
                    .foregroundStyle(.secondary)
            }
            if model.queueEnabled && !model.lostEdits.isEmpty { LostEditOffers(model: model) }
            switch model.state {
            case .readOnly:
                ReadOnlyLine(text: readOnlyText(model.session))
            case .terminal where model.session?.terminal?.canType == true:
                // Typed into the terminal on the Mac: Stop there is the Mac's, not this chat's.
                composer(isWorking: false, canSend: true, stop: {})
            case .terminal where model.queueEnabled:
                // It cannot take typing now; the Mac sends queued messages when it can.
                if terminalPrompt == nil,
                   let line = terminalReasonLine(model.session, queue: true, queueStatus: model.queue) {
                    ReadOnlyLine(text: line)
                }
                composer(isWorking: false, canSend: true, stop: {})
            case .terminal where terminalPrompt != nil:
                // The prompt's card says what to do.
                EmptyView()
            case .terminal:
                ReadOnlyLine(text: readOnlyText(model.session))
            case .failed:
                EmptyView()
            default:
                composer(isWorking: result.working, canSend: model.session != nil,
                         stop: { Task { await model.stop() } })
            }
            if let notice = model.addNotice {
                Text(notice)
                    .font(.footnote)
                    .foregroundStyle(.secondary)
                    .fixedSize(horizontal: false, vertical: true)
                    .frame(maxWidth: .infinity, alignment: .leading)
                    .accessibilityIdentifier("queueNotice")
            }
            if model.queueEnabled && !isFailed {
                QueueList(model: model, draft: $draft, maxListHeight: height > 0 ? height / 3 : .infinity,
                          focusComposer: { composerFocused = true })
            }
        }
        .padding(.horizontal, 12)
        .padding(.top, 8)
        .padding(.bottom, 8)
        .background(Color(.systemBackground))
    }

    fileprivate var isFailed: Bool {
        if case .failed = model.state { true } else { false }
    }

    fileprivate func composer(isWorking: Bool, canSend: Bool, stop: @escaping () -> Void) -> some View {
        Composer(text: $draft, isWorking: isWorking, canSend: canSend, action: model.composerAction,
                 queueEnabled: model.queueEnabled, fieldFocus: $composerFocused, send: send, queue: addToQueue,
                 stop: stop)
    }

    /// Queues the composer's text; it leaves the box once queued, and stays (with why) when refused.
    fileprivate func addToQueue() {
        let text = draft.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !text.isEmpty else { return }
        Task {
            guard await model.addToQueue(text) else { return }
            if draft.trimmingCharacters(in: .whitespacesAndNewlines) == text { draft = "" }
        }
    }

    /// A message refused busy goes back into an empty composer, offered to the queue; with other text in the box
    /// it stays as the failed message with its own Add to queue.
    fileprivate func reclaimRefused() {
        guard draft.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty,
              let text = model.reclaimRefusedMessage() else { return }
        draft = text
    }

    /// A steps item; `openStepDetails` keeps which of its steps have their detail open.
    fileprivate func stepsCard(_ group: StepGroup, at index: Int) -> some View {
        StepsCard(group: group, live: result.working && isLastTurn(index),
                  isOpen: toggle($openSteps, index, initially: DebugHooks.openSteps),
                  openDetails: Binding(
                      get: { openStepDetails[index] ?? DebugHooks.openStepDetails },
                      set: { openStepDetails[index] = $0 }
                  ))
    }
}
