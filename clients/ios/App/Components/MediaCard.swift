import AVKit
import SwiftUI
import AimuxKit

// A media card (api/testdata/clients/README.md, "Media"): the kind's symbol, the title and the host, on the code
// block's surface. Nothing is fetched until a tap, so no thumbnail leaks the phone's address. A video or audio file
// plays in place (AVKit), one card at a time in a chat (`MediaPlayback`), and is stopped and released, with the
// audio session, when it scrolls away, another card starts or the view goes; YouTube, Vimeo and images open in
// Safari through the chat's http(s)-only openURL.
struct MediaCardView: View {
    let media: MDMedia
    @Environment(\.openURL) private var openURL
    /// Absent outside a chat; then cards play independently.
    @Environment(MediaPlayback.self) private var playback: MediaPlayback?
    @State private var player: AVPlayer?
    @State private var card = UUID()

    private var playsInPlace: Bool { media.kind == .video || media.kind == .audio }
    private var host: String { mediaHost(media.href) ?? "" }

    var body: some View {
        VStack(spacing: 0) {
            if let player {
                VideoPlayer(player: player)
                    .frame(height: media.kind == .audio ? 64 : nil)
                    .aspectRatio(media.kind == .audio ? nil : 16 / 9, contentMode: .fit)
                    .accessibilityIdentifier("media-player")
                Rectangle().fill(Color(.separator)).frame(height: 1 / 3)
            }
            Button(action: tap) { label }
                .buttonStyle(.plain)
                .accessibilityLabel("\(Self.kindName(media.kind)): \(media.title), \(host)")
                .accessibilityHint(playsInPlace ? (player == nil ? "Plays here" : "Stops") : "Opens in Safari")
                .accessibilityIdentifier("media-card")
        }
        .background(Color(.secondarySystemBackground), in: RoundedRectangle(cornerRadius: 12, style: .continuous))
        .clipShape(RoundedRectangle(cornerRadius: 12, style: .continuous))
        .overlay(RoundedRectangle(cornerRadius: 12, style: .continuous).strokeBorder(Color(.separator), lineWidth: 0.5))
        .frame(maxWidth: .infinity)
        .onChange(of: playback?.playing) { _, now in
            if player != nil && now != card { stop() }
        }
        .onScrollVisibilityChange(threshold: 0.1) { visible in
            if !visible { stop() }
        }
        .onDisappear(perform: stop)
    }

    private var label: some View {
        HStack(spacing: 12) {
            Image(systemName: player == nil ? Self.symbol(media.kind) : "stop.circle")
                .font(.title2)
                .foregroundStyle(.secondary)
                .frame(width: 32)
            VStack(alignment: .leading, spacing: 2) {
                // The title reads in its own direction (an Arabic title right to left); the host is always LTR.
                Text(media.title)
                    .font(.subheadline.weight(.semibold))
                    .foregroundStyle(.primary)
                    .lineLimit(2)
                    .multilineTextAlignment(.leading)
                    .frame(maxWidth: .infinity, alignment: .leading)
                    .environment(\.layoutDirection, isRightToLeft([.text(media.title)]) ? .rightToLeft : .leftToRight)
                Text(host)
                    .font(.footnote)
                    .foregroundStyle(.secondary)
                    .lineLimit(1)
                    .frame(maxWidth: .infinity, alignment: .leading)
                    .environment(\.layoutDirection, .leftToRight)
            }
            Spacer(minLength: 0)
        }
        .padding(.horizontal, 14)
        .padding(.vertical, 12)
        .contentShape(Rectangle())
    }

    private func tap() {
        guard let url = URL(string: media.href), isWebLink(media.href) else { return }
        guard playsInPlace else {
            openURL(url)
            return
        }
        if player != nil {
            stop()
            return
        }
        let session = AVAudioSession.sharedInstance()
        try? session.setCategory(.playback)
        try? session.setActive(true)
        playback?.start(card)
        let item = AVPlayer(url: url)
        player = item
        item.play()
    }

    /// Pauses and releases the player; when no other card took over, gives the audio back (Music, podcasts resume).
    private func stop() {
        guard let current = player else { return }
        current.pause()
        current.replaceCurrentItem(with: nil)
        player = nil
        if playback?.stop(card) ?? true {
            try? AVAudioSession.sharedInstance().setActive(false, options: .notifyOthersOnDeactivation)
        }
    }

    static func symbol(_ kind: MediaKind) -> String {
        switch kind {
        case .youtube, .vimeo, .video: "play.rectangle"
        case .image: "photo"
        case .audio: "waveform"
        }
    }

    static func kindName(_ kind: MediaKind) -> String {
        switch kind {
        case .youtube: "YouTube video"
        case .vimeo: "Vimeo video"
        case .video: "Video"
        case .image: "Image"
        case .audio: "Audio"
        }
    }
}
