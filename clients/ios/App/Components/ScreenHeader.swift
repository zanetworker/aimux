import SwiftUI

/// The top of Now and All: the 32 pt title (scaled with Dynamic Type) with its button on the same line,
/// then what sits under it. Used as the first list row, with the navigation bar hidden on that screen.
struct ScreenHeader<Trailing: View, Below: View>: View {
    let title: String
    @ViewBuilder var trailing: Trailing
    @ViewBuilder var below: Below
    @ScaledMetric(relativeTo: .largeTitle) private var titleSize: CGFloat = 32

    var body: some View {
        VStack(alignment: .leading, spacing: 6) {
            HStack(alignment: .center) {
                Text(title)
                    .font(.system(size: titleSize, weight: .bold))
                    .accessibilityAddTraits(.isHeader)
                Spacer()
                trailing
            }
            below
        }
        .listRowBackground(Color.clear)
        .listRowInsets(EdgeInsets(top: 4, leading: 4, bottom: 2, trailing: 0))
    }
}

/// A round glass button for the header, like the mockups' compose button.
struct HeaderButton: View {
    let label: String
    let systemImage: String
    let action: () -> Void

    var body: some View {
        Button(action: action) {
            Image(systemName: systemImage)
                .font(.body.weight(.medium))
                .frame(width: 30, height: 30)
        }
        .buttonStyle(.glass)
        .buttonBorderShape(.circle)
        .accessibilityLabel(label)
    }
}

extension View {
    /// Row padding 10 pt above and below.
    func compactRow() -> some View {
        listRowInsets(.vertical, 10)
    }
}
