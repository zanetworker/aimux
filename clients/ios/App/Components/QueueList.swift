import SwiftUI
import AimuxKit

/// The chat queue under the composer: a heading, the paused reason or waiting note with Resume, the last error,
/// and one row per message (ChatQueue says what each shows and allows). Drag a row to reorder it, swipe or tap
/// its bin to delete it, tap a waiting message to edit it in place.
///
/// The open edit lives here, keyed by its message id, and a queue update never rewrites it: only when its message
/// can no longer be edited (sent meanwhile, or deleted) does it close, handing changed text to an empty composer
/// (`draft`) or to the chat's lost edits.
struct QueueList: View {
    let model: ChatModel
    @Binding var draft: String
    /// At accessibility text sizes, the list's tallest (about a third of the chat); more rows scroll inside.
    var maxListHeight: CGFloat = .infinity
    /// A lost edit's text went into the composer: VoiceOver focus follows it there.
    var focusComposer: () -> Void = {}

    @State private var edit: QueueEdit?
    /// `-openQueueEdit` was applied (once, when its message first shows).
    @State private var openedDebugEdit = false
    /// Another row was tapped while this edit had unsaved changes.
    @State private var saveFirst = false
    @FocusState private var editFocused: Bool
    @Environment(\.dynamicTypeSize) private var typeSize
    @ScaledMetric(relativeTo: .body) private var rowHeight: CGFloat = 58
    @ScaledMetric(relativeTo: .body) private var editHeight: CGFloat = 132

    private var rows: [QueueRow] { ChatQueue.rows(model.queue) }
    private var section: QueueSection { ChatQueue.section(model.queue) }

    var body: some View {
        VStack(alignment: .leading, spacing: 6) {
            if section.visible {
                heading
                if section.line != nil || section.resume { status }
            }
            if let error = model.queueError {
                Text(error)
                    .font(.footnote)
                    .foregroundStyle(.secondary)
                    .fixedSize(horizontal: false, vertical: true)
                    .accessibilityIdentifier("queueError")
            }
            if section.visible { list }
        }
        .padding(.horizontal, section.visible ? 14 : 0)
        .padding(.vertical, section.visible ? 10 : 0)
        .background(section.visible ? Color(.secondarySystemBackground) : .clear,
                    in: RoundedRectangle(cornerRadius: 20, style: .continuous))
        .onChange(of: model.queue) {
            closeLostEdit()
            openDebugEdit()
        }
        .onAppear(perform: openDebugEdit)
        .onChange(of: model.queueError) { _, error in
            if let error { AccessibilityNotification.Announcement(error).post() }
        }
    }

    private var heading: some View {
        HStack(spacing: 6) {
            Text("Queue").font(.subheadline.weight(.semibold))
            Text(rows.count == 1 ? "1 message" : "\(rows.count) messages")
                .font(.subheadline)
                .foregroundStyle(.secondary)
            Spacer()
        }
        .accessibilityElement(children: .combine)
        .accessibilityAddTraits(.isHeader)
    }

    /// The reason (or note) and Resume side by side; stacked at accessibility text sizes so neither is cut.
    private var status: some View {
        let layout = typeSize.isAccessibilitySize ? AnyLayout(VStackLayout(alignment: .leading, spacing: 8))
            : AnyLayout(HStackLayout(alignment: .firstTextBaseline, spacing: 10))
        return layout {
            if let line = section.line {
                Text(line)
                    .font(.footnote)
                    .foregroundStyle(.secondary)
                    .fixedSize(horizontal: false, vertical: true)
                    .frame(maxWidth: .infinity, alignment: .leading)
                    .accessibilityIdentifier("queueLine")
            } else if !typeSize.isAccessibilitySize {
                Spacer()
            }
            if section.resume {
                Button("Resume") { Task { await model.resumeQueue() } }
                    .font(.subheadline.weight(.semibold))
                    .buttonStyle(.borderedProminent)
                    .buttonBorderShape(.capsule)
                    .controlSize(.large)
                    .tint(.primary)
                    .foregroundStyle(Color(.systemBackground))
                    .accessibilityHint("Sends the queued messages again, one at a time")
                    .accessibilityIdentifier("queueResume")
            }
        }
    }

    // MARK: Rows

    private var list: some View {
        List {
            ForEach(rows) { row in
                Group {
                    if edit?.id == row.id { editor(row) } else { item(row) }
                }
                .listRowInsets(EdgeInsets(top: 4, leading: 0, bottom: 4, trailing: 0))
                .listRowBackground(Color.clear)
                .listRowSeparatorTint(Color(.separator))
                .moveDisabled(!row.canMove || edit != nil)
                .swipeActions(edge: .trailing, allowsFullSwipe: true) {
                    if row.canDelete {
                        Button("Delete", role: .destructive) { delete(row) }
                            .accessibilityIdentifier("queueSwipeDelete")
                    }
                }
            }
            .onMove { from, destination in
                guard let source = from.first else { return }
                Task { await model.moveQueueItem(from: source, to: destination) }
            }
        }
        .listStyle(.plain)
        .scrollContentBackground(.hidden)
        .environment(\.defaultMinListRowHeight, 44)
        .frame(height: listHeight)
        .scrollDisabled(listHeight >= CGFloat(rows.count) * rowHeight && edit == nil)
        .accessibilityIdentifier("queueList")
    }

    /// About three rows; more scroll inside.
    private var listHeight: CGFloat {
        let shown = CGFloat(min(rows.count, 3)) * rowHeight
        let wanted = edit == nil ? shown : max(shown, editHeight + rowHeight * CGFloat(min(rows.count - 1, 1)))
        return typeSize.isAccessibilitySize ? min(wanted, max(maxListHeight, rowHeight)) : wanted
    }

    private func item(_ row: QueueRow) -> some View {
        HStack(spacing: 4) {
            Button { open(row) } label: {
                VStack(alignment: .leading, spacing: 2) {
                    Text(bidi: isolated(row.text))
                        .font(.subheadline)
                        .foregroundStyle(.primary)
                        .lineLimit(1)
                    Text(row.label)
                        .font(.caption)
                        .foregroundStyle(.secondary)
                }
                .frame(maxWidth: .infinity, minHeight: 44, alignment: .leading)
                .contentShape(Rectangle())
            }
            .buttonStyle(.plain)
            .disabled(!row.canEdit)
            if row.canDelete {
                Button { delete(row) } label: {
                    Image(systemName: "trash").font(.footnote).foregroundStyle(.secondary)
                        .frame(width: 44, height: 44)
                        .contentShape(Rectangle())
                }
                .buttonStyle(.borderless)
                .accessibilityIdentifier("queueDelete-\(row.id)")
            }
            if row.canMove {
                Image(systemName: "line.3.horizontal")
                    .font(.footnote)
                    .foregroundStyle(.tertiary)
                    .frame(width: 24, height: 44)
            }
        }
        .accessibilityElement(children: .ignore)
        .accessibilityLabel(Text(bidi: isolated(row.text)))
        .accessibilityValue(row.label)
        .accessibilityAddTraits(row.canEdit ? .isButton : [])
        .editHint(row.canEdit)
        .accessibilityAction { open(row) }
        .accessibilityActions { actions(row) }
        .accessibilityIdentifier("queueItem-\(row.id)")
    }

    @ViewBuilder private func actions(_ row: QueueRow) -> some View {
        if row.canEdit { Button("Edit") { open(row) } }
        if row.canMove && ChatQueue.position(in: model.queue, moving: row.id, by: -1) != nil {
            Button("Move up") { Task { await model.moveQueueItem(row.id, by: -1) } }
        }
        if row.canMove && ChatQueue.position(in: model.queue, moving: row.id, by: 1) != nil {
            Button("Move down") { Task { await model.moveQueueItem(row.id, by: 1) } }
        }
        if row.canDelete { Button("Delete") { delete(row) } }
    }

    private func editor(_ row: QueueRow) -> some View {
        VStack(alignment: .trailing, spacing: 6) {
            TextField("Queued message", text: editText, axis: .vertical)
                .lineLimit(1...4)
                .font(.subheadline)
                .padding(.horizontal, 12)
                .padding(.vertical, 9)
                .background(Color(.systemBackground), in: RoundedRectangle(cornerRadius: 14, style: .continuous))
                .focused($editFocused)
                .accessibilityLabel("Edit queued message")
                .accessibilityIdentifier("queueEditField")
            HStack(spacing: 8) {
                if saveFirst {
                    Text(Self.saveFirstText)
                        .font(.footnote)
                        .foregroundStyle(.secondary)
                        .frame(maxWidth: .infinity, alignment: .leading)
                }
                Button("Cancel") { close() }
                    .buttonStyle(.bordered)
                    .accessibilityIdentifier("queueEditCancel")
                Button("Save") { save(row.id) }
                    .buttonStyle(.borderedProminent)
                    .disabled(model.isChangingQueue)
                    .accessibilityIdentifier("queueEditSave")
            }
            .font(.subheadline.weight(.semibold))
            .buttonBorderShape(.capsule)
            .tint(.primary)
            .controlSize(.large)
        }
    }

    private var editText: Binding<String> {
        Binding(get: { edit?.text ?? "" }, set: { edit?.text = $0 })
    }

    // MARK: Actions

    static let saveFirstText = "Save or cancel this edit first."

    private func open(_ row: QueueRow) {
        guard row.canEdit, edit?.id != row.id else { return }
        // An open edit with unsaved changes stays open, and says so.
        if let edit, trimmed(edit.text) != trimmed(edit.original) {
            saveFirst = true
            AccessibilityNotification.Announcement(Self.saveFirstText).post()
            return
        }
        saveFirst = false
        edit = QueueEdit(id: row.id, original: row.text, text: row.text)
        editFocused = true
    }

    private func close() {
        edit = nil
        saveFirst = false
    }

    private func trimmed(_ text: String) -> String { text.trimmingCharacters(in: .whitespacesAndNewlines) }

    private func save(_ id: String) {
        guard let text = edit?.text else { return }
        Task { if await model.editQueueItem(id, text: text), edit?.id == id { close() } }
    }

    private func delete(_ row: QueueRow) {
        if edit?.id == row.id { close() }
        Task { await model.deleteQueueItem(row.id) }
    }

    /// The queue changed: an edit whose message is no longer editable closes, its changed text kept.
    private func closeLostEdit() {
        guard let open = edit else { return }
        switch ChatQueue.editOutcome(open, in: model.queue) {
        case .keep: return
        case .closed: close()
        case .lost(let text):
            close()
            let back = model.editLost(text, draftIsEmpty: trimmed(draft).isEmpty)
            AccessibilityNotification.Announcement(ChatQueue.lostEditAnnouncement(inComposer: back != nil)).post()
            if let back {
                draft = back
                focusComposer()
            }
        }
    }

    private func openDebugEdit() {
        guard !openedDebugEdit, let id = DebugHooks.openQueueEdit,
              let row = rows.first(where: { $0.id == id }) else { return }
        openedDebugEdit = true
        open(row)
    }
}

/// Edits whose message was sent before they were saved, while the composer held other text: each keeps its
/// text with Add to queue and Dismiss until one of them is used. VoiceOver focus moves to a new one.
struct LostEditOffers: View {
    let model: ChatModel
    @AccessibilityFocusState private var focused: UUID?

    var body: some View {
        VStack(spacing: 8) {
            ForEach(model.lostEdits) { lost in
                VStack(alignment: .leading, spacing: 6) {
                    Text("This message was sent before your edit was saved.")
                        .font(.footnote)
                        .foregroundStyle(.secondary)
                        .accessibilityFocused($focused, equals: lost.id)
                    Text(bidi: isolated(lost.text))
                        .font(.subheadline)
                        .lineLimit(3)
                    HStack(spacing: 8) {
                        Spacer()
                        Button("Dismiss") { model.dismissLostEdit(lost.id) }
                            .buttonStyle(.bordered)
                        Button(ComposerAction.queue.label) { Task { await model.queueLostEdit(lost.id) } }
                            .buttonStyle(.borderedProminent)
                    }
                    .font(.subheadline.weight(.semibold))
                    .buttonBorderShape(.capsule)
                    .tint(.primary)
                    .controlSize(.large)
                }
                .padding(12)
                .background(Color(.secondarySystemBackground),
                            in: RoundedRectangle(cornerRadius: 16, style: .continuous))
                .accessibilityElement(children: .contain)
                .accessibilityIdentifier("lostEdit-\(lost.id)")
            }
        }
        .onAppear { focused = model.lostEdits.last?.id }
        .onChange(of: model.lostEdits.count) { old, new in
            if new > old { focused = model.lostEdits.last?.id }
        }
    }
}

extension View {
    /// The row's hint, only for a message that can be edited.
    @ViewBuilder fileprivate func editHint(_ canEdit: Bool) -> some View {
        if canEdit { accessibilityHint("Edits this queued message") } else { self }
    }
}
