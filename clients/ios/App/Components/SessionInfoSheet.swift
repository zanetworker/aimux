import SwiftUI
import AimuxKit

/// The session's title and description (Edit, Update summary, Pin), then cost, tokens, model and timing from
/// `SessionDetail.stats`. Edits and Update summary go through Now's model, so the lists show them at once.
struct SessionInfoSheet: View {
    let session: Session
    let detail: SessionDetail?
    let now: NowModel
    @Environment(\.dismiss) private var dismiss

    /// The field being edited in place; nil when none.
    @State private var edit: MetaEdit?
    @State private var saving = false
    @State private var saveError: String?
    @FocusState private var fieldFocused: Bool

    private static let missing = "Not available"

    private struct Shown {
        let title: String
        let description: String?
        let pinned: Bool
    }

    /// What the sheet shows: the session as listed now (pins and edits taken), else the detail, else the row.
    private var shown: Shown {
        if let listed = now.listedSession(session.id) {
            return Shown(title: listed.displayTitle, description: listed.descriptionLine, pinned: listed.pinned)
        }
        if let detail {
            let description = detail.description?.trimmingCharacters(in: .whitespacesAndNewlines)
            return Shown(title: detail.displayTitle, description: description?.isEmpty == false ? description : nil,
                         pinned: detail.pinned)
        }
        return Shown(title: session.displayTitle, description: session.descriptionLine, pinned: session.pinned)
    }

    var body: some View {
        let stats = detail?.stats
        NavigationStack {
            List {
                metaSection
                if now.canDescribe { actionsSection }
                Section {
                    row("Cost", stats.map { StatsFormat.cost($0.costUsd) })
                    row("Tokens in", stats.map { StatsFormat.tokens($0.tokensIn) })
                    row("Tokens out", stats.map { StatsFormat.tokens($0.tokensOut) })
                    row("Cache read", stats.map { StatsFormat.tokens($0.cacheReadTokens) })
                    row("Cache write", stats.map { StatsFormat.tokens($0.cacheWriteTokens) })
                }
                Section {
                    row("Model", stats?.model)
                    row("Turns", stats.map { "\($0.turns)" })
                    row("Duration", stats.map { StatsFormat.duration(minutes: $0.durationMin) })
                    row("Started", StatsFormat.time(stats?.startTime))
                    row("Last active", StatsFormat.time(detail?.lastActive ?? session.lastActive))
                }
                Section {
                    row("Branch", stats?.gitBranch, mono: true)
                    row("Folder", detail?.project ?? session.project, mono: true)
                }
            }
            .listStyle(.insetGrouped)
            .navigationTitle(shown.title)
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .confirmationAction) {
                    Button("Done") { dismiss() }
                }
            }
        }
        .task {
            // Today's naming count and the capability, read now (they change during the day).
            await now.readHealth()
            if let field = DebugHooks.editField, now.canDescribe { begin(field) }
        }
        // A result that arrived while no sheet of this session was open is no longer news.
        .onAppear { now.clearDescribeNote(session.id) }
        .onDisappear { now.clearDescribeNote(session.id) }
        .onChange(of: now.describeNotes[session.id]) { _, note in
            if let note { AccessibilityNotification.Announcement(note).post() }
        }
    }

    // MARK: Title and description

    private var metaSection: some View {
        Section {
            field(.title, value: shown.title)
            field(.description, value: shown.description ?? "")
        }
    }

    @ViewBuilder private func field(_ field: MetaField, value: String) -> some View {
        if let current = edit, current.field == field {
            editor(current)
        } else {
            VStack(alignment: .leading, spacing: 4) {
                HStack(alignment: .firstTextBaseline) {
                    Text(field.label).font(.subheadline).foregroundStyle(.secondary)
                    Spacer()
                    if now.canDescribe {
                        Button { begin(field) } label: { TargetLabel("Edit") }
                            .buttonStyle(.borderless)
                            .disabled(edit != nil)
                            .accessibilityLabel("Edit \(field.label.lowercased())")
                            .accessibilityIdentifier("edit-\(field.rawValue)")
                    }
                }
                Text(bidi: value.isEmpty ? "None yet" : isolated(value))
                    .foregroundStyle(value.isEmpty ? .secondary : .primary)
                    .fixedSize(horizontal: false, vertical: true)
                    .textSelection(.enabled)
                    .accessibilityIdentifier("meta-\(field.rawValue)")
            }
            .padding(.vertical, 2)
        }
    }

    private func editor(_ current: MetaEdit) -> some View {
        let draft = Binding(get: { edit?.draft ?? "" }, set: { text in
            saveError = nil
            // One line each: a typed Return saves; pasted lines stay and the field says why it can't be saved.
            if edit?.type(text) == true { save() }
        })
        let problem = current.problem ?? saveError
        return VStack(alignment: .leading, spacing: 8) {
            Text(current.field.label).font(.subheadline).foregroundStyle(.secondary)
            TextField(current.field.label, text: draft, axis: current.field == .title ? .horizontal : .vertical)
                .lineLimit(current.field == .title ? 1...1 : 2...6)
                .focused($fieldFocused)
                .submitLabel(.done)
                .onSubmit { save() }
                .accessibilityHint(problem ?? "")
                .accessibilityIdentifier("metaInput")
                .padding(8)
                .background(Color(.tertiarySystemFill), in: .rect(cornerRadius: 8))
            Text("Empty uses the automatic \(current.field.label.lowercased()).")
                .font(.footnote)
                .foregroundStyle(.secondary)
            if let problem {
                Text(problem).font(.footnote).foregroundStyle(.red)
            }
            HStack {
                Text("\(current.count)/\(current.field.limit)")
                    .font(.footnote)
                    .foregroundStyle(.secondary)
                    .monospacedDigit()
                    .accessibilityLabel("\(current.count) of \(current.field.limit) characters")
                Spacer()
                Button { cancel() } label: { TargetLabel("Cancel") }
                    .buttonStyle(.borderless)
                    .accessibilityIdentifier("metaCancel")
                Button { save() } label: { TargetLabel(saving ? "Saving…" : "Save").fontWeight(.semibold) }
                    .buttonStyle(.borderless)
                    .disabled(current.problem != nil || saving)
                    .accessibilityIdentifier("metaSave")
            }
        }
        .padding(.vertical, 4)
    }

    private func begin(_ field: MetaField) {
        edit = MetaEdit(field: field, original: field == .title ? shown.title : shown.description ?? "")
        saveError = nil
        fieldFocused = true
    }

    private func cancel() {
        edit = nil
        saveError = nil
    }

    private func save() {
        guard let current = edit, !saving, current.problem == nil else { return }
        saving = true
        Task {
            let outcome = await now.save(current, sessionID: session.id)
            saving = false
            switch outcome {
            case .failed(let failure):
                saveError = failure
                AccessibilityNotification.Announcement(failure).post()
            case .saved(let note):
                edit = nil
                AccessibilityNotification.Announcement(note).post()
            case .unchanged:
                edit = nil  // nothing was sent; the field just closes
            }
        }
    }

    // MARK: Update summary and Pin

    private var actionsSection: some View {
        let describing = now.describing.contains(session.id)
        let pinning = now.pinning.contains(session.id)
        let note = now.describeNotes[session.id] ?? now.pinErrors[session.id]
        return Section {
            Button {
                // One at a time per session: a tap while it runs does nothing.
                if !describing { Task { await now.describe(session.id) } }
            } label: {
                HStack {
                    Text(describing ? "Updating summary…" : "Update summary")
                    Spacer()
                    if describing { ProgressView() }
                }
            }
            .accessibilityValue(describing ? "In progress" : "")
            .accessibilityIdentifier("updateSummary")
            Button(shown.pinned ? "Unpin" : "Pin") { pin() }
                .disabled(pinning)
                .accessibilityIdentifier("pinButton")
        } footer: {
            VStack(alignment: .leading, spacing: 6) {
                if let note {
                    Text(note)
                        .foregroundStyle(.primary)
                        .accessibilityIdentifier("metaNote")
                }
                Text(SessionMetaText.hint(now.health?.naming))
            }
        }
        .tint(.primary)
    }

    private func pin() {
        let pinned = !shown.pinned
        let target = now.listedSession(session.id) ?? session
        Task {
            // A pin already on its way: nothing was sent, so nothing to announce.
            guard await now.setPinned(target, pinned) else { return }
            let text = now.pinErrors[session.id] ?? (pinned ? "Pinned" : "Unpinned")
            AccessibilityNotification.Announcement(text).post()
        }
    }

    // MARK: Stats

    private func row(_ label: String, _ value: String?, mono: Bool = false) -> some View {
        let shown = value.flatMap { $0.isEmpty ? nil : $0 }
        return HStack(alignment: .firstTextBaseline, spacing: 12) {
            Text(label)
            Spacer(minLength: 8)
            Text(shown ?? Self.missing)
                .font(mono && shown != nil ? .system(.subheadline, design: .monospaced) : .body)
                .foregroundStyle(.secondary)
                .multilineTextAlignment(.trailing)
                .fixedSize(horizontal: false, vertical: true)
                .layoutPriority(1)
                .monospacedDigit()
                .textSelection(.enabled)
        }
    }
}

/// A text button's label with a 44 pt hit target around the text.
private struct TargetLabel: View {
    let title: String

    init(_ title: String) { self.title = title }

    var body: some View {
        Text(title)
            .frame(minWidth: 44, minHeight: 44)
            .contentShape(Rectangle())
    }
}
