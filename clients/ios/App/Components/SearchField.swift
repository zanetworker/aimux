import SwiftUI
import AimuxKit

/// The one search box of Now and All: a magnifying glass, the text and a clear button, like the system's field.
struct SearchField: View {
    @Binding var text: String
    let prompt: String

    var body: some View {
        HStack(spacing: 6) {
            Image(systemName: "magnifyingglass")
                .foregroundStyle(.secondary)
                .accessibilityHidden(true)
            TextField(prompt, text: $text)
                .textInputAutocapitalization(.never)
                .autocorrectionDisabled()
                .submitLabel(.search)
                .accessibilityIdentifier("searchField")
            if !text.isEmpty {
                Button { text = "" } label: {
                    Image(systemName: "xmark.circle.fill").foregroundStyle(.tertiary)
                }
                .buttonStyle(.plain)
                .accessibilityLabel("Clear search")
                .accessibilityIdentifier("clearSearch")
            }
        }
        .font(.body)
        .padding(.horizontal, 10)
        .padding(.vertical, 8)
        .background(Color(.tertiarySystemFill), in: RoundedRectangle(cornerRadius: 10, style: .continuous))
    }
}

extension View {
    /// The server's conversation search, 400 ms after the last keystroke; a newer query cancels the wait.
    func conversationSearch(_ search: SearchModel, query: String) -> some View {
        task(id: query) {
            try? await Task.sleep(for: .milliseconds(400))
            guard !Task.isCancelled else { return }
            await Interaction.perform { await search.run(query) }
        }
    }
}

/// All's local results for a query: the open matches (in Mac order), then every other match newest first. Now shows
/// the same matches in place instead.
struct SearchResultSections<Row: View>: View {
    let found: (open: [Session], other: [Session])
    @ViewBuilder let row: (Session) -> Row

    var body: some View {
        if !found.open.isEmpty {
            Section {
                ForEach(found.open) { row($0) }
            } header: {
                SectionLabel("Open")
            }
        }
        if !found.other.isEmpty {
            Section {
                ForEach(found.other) { row($0) }
            } header: {
                SectionLabel("Other sessions")
            }
        }
    }
}

/// "In conversations": the server's hits for the query, without the sessions the screen already shows.
struct ConversationsSection<Row: View>: View {
    let search: SearchModel
    let query: String
    let shown: Set<String>
    let format: SessionRowFormat
    @ViewBuilder let row: (SearchHit) -> Row

    var body: some View {
        if search.isAvailable, !query.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty {
            let hits = search.hits.filter { !shown.contains($0.id) }
            Section {
                ForEach(hits) { hit in row(hit) }
                if hits.isEmpty {
                    if search.isSearching {
                        HStack { ProgressView(); Text("Searching your Mac…").foregroundStyle(.secondary) }
                    } else if let error = search.error {
                        Text(error.userMessage.title).foregroundStyle(.secondary)
                    } else {
                        Text("No other matches in conversations").foregroundStyle(.secondary)
                    }
                }
            } header: {
                SectionLabel("In conversations")
            }
        }
    }
}

/// The Working · N chip: a native toggle button that narrows Now to working sessions.
struct WorkingChip: View {
    @Binding var isOn: Bool
    let count: Int

    var body: some View {
        Toggle(isOn: $isOn) {
            Text("Working · \(count)").monospacedDigit()
        }
        .toggleStyle(.button)
        .buttonStyle(.bordered)
        .buttonBorderShape(.capsule)
        .controlSize(.small)
        .tint(.secondary)
        .accessibilityIdentifier("workingChip")
    }
}
