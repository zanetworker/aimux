/// Decodes an array item by item, dropping items that fail, so one bad row
/// from a newer or buggy server does not fail the whole list.
struct LossyList<Element: Decodable>: Decodable {
    var elements: [Element] = []

    init(from decoder: Decoder) throws {
        var container = try decoder.unkeyedContainer()
        while !container.isAtEnd {
            if let element = try? container.decode(Element.self) {
                elements.append(element)
            } else {
                _ = try container.decode(Skip.self)
            }
        }
    }

    private struct Skip: Decodable {}
}
