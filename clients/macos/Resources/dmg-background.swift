import AppKit

// Finder uses points; provide both resolutions so the artwork stays crisp on Retina.
let outputDirectory = URL(fileURLWithPath: CommandLine.arguments[1], isDirectory: true)
try FileManager.default.createDirectory(at: outputDirectory, withIntermediateDirectories: true)
let width: CGFloat = 720
let height: CGFloat = 450
let green = NSColor(srgbRed: 0.15, green: 0.39, blue: 0.32, alpha: 1)
let secondary = NSColor(srgbRed: 0.38, green: 0.46, blue: 0.41, alpha: 1)

func rectangle(_ x: CGFloat, _ top: CGFloat, _ w: CGFloat, _ h: CGFloat) -> NSRect {
    NSRect(x: x, y: height - top - h, width: w, height: h)
}

func text(_ value: String, top: CGFloat, size: CGFloat, weight: NSFont.Weight,
          color: NSColor, tracking: CGFloat = 0) {
    let paragraph = NSMutableParagraphStyle()
    paragraph.alignment = .center
    (value as NSString).draw(in: rectangle(30, top, width - 60, size * 1.6), withAttributes: [
        .font: NSFont.systemFont(ofSize: size, weight: weight),
        .foregroundColor: color,
        .paragraphStyle: paragraph,
        .kern: tracking
    ])
}

for scale in [1, 2] {
    let bitmap = NSBitmapImageRep(bitmapDataPlanes: nil,
        pixelsWide: Int(width) * scale, pixelsHigh: Int(height) * scale,
        bitsPerSample: 8, samplesPerPixel: 4, hasAlpha: true, isPlanar: false,
        colorSpaceName: .deviceRGB, bytesPerRow: 0, bitsPerPixel: 32)!
    NSGraphicsContext.saveGraphicsState()
    NSGraphicsContext.current = NSGraphicsContext(bitmapImageRep: bitmap)
    NSGraphicsContext.current!.cgContext.scaleBy(x: CGFloat(scale), y: CGFloat(scale))

    let gradient = NSGradient(starting: NSColor(srgbRed: 0.985, green: 0.99, blue: 0.98, alpha: 1),
        ending: NSColor(srgbRed: 0.91, green: 0.95, blue: 0.92, alpha: 1))!
    gradient.draw(in: NSRect(x: 0, y: 0, width: width, height: height), angle: -90)

    text("ДЛЯ MACOS", top: 27, size: 10, weight: .semibold, color: secondary, tracking: 2)
    text("ShareFile", top: 48, size: 38, weight: .semibold, color: green, tracking: -0.8)
    text("Перетащите приложение в Applications", top: 105, size: 15, weight: .regular, color: secondary)

    for x: CGFloat in [88, 440] {
        NSGraphicsContext.saveGraphicsState()
        let shadow = NSShadow()
        shadow.shadowColor = green.withAlphaComponent(0.07)
        shadow.shadowBlurRadius = 18
        shadow.shadowOffset = NSSize(width: 0, height: -6)
        shadow.set()
        NSColor.white.withAlphaComponent(0.86).setFill()
        let card = NSBezierPath(roundedRect: rectangle(x, 166, 192, 176), xRadius: 25, yRadius: 25)
        card.fill()
        NSGraphicsContext.restoreGraphicsState()
        NSColor.white.setStroke()
        card.lineWidth = 1
        card.stroke()
    }

    NSColor(srgbRed: 0.84, green: 0.91, blue: 0.86, alpha: 1).setFill()
    NSBezierPath(ovalIn: rectangle(330, 208, 60, 60)).fill()
    let arrow = NSBezierPath()
    arrow.lineWidth = 3
    arrow.lineCapStyle = .round
    arrow.lineJoinStyle = .round
    arrow.move(to: NSPoint(x: 343, y: height - 238))
    arrow.line(to: NSPoint(x: 377, y: height - 238))
    arrow.move(to: NSPoint(x: 366, y: height - 227))
    arrow.line(to: NSPoint(x: 377, y: height - 238))
    arrow.line(to: NSPoint(x: 366, y: height - 249))
    green.setStroke()
    arrow.stroke()

    text("Затем откройте ShareFile из Applications", top: 375, size: 13, weight: .regular, color: secondary)
    text("Apple Silicon и Intel  ·  macOS 13+", top: 412, size: 10, weight: .medium,
         color: secondary.withAlphaComponent(0.8), tracking: 0.3)

    NSGraphicsContext.restoreGraphicsState()
    bitmap.size = NSSize(width: width, height: height)
    let suffix = scale == 2 ? "@2x" : ""
    let output = outputDirectory.appendingPathComponent("dmg-background\(suffix).png")
    try bitmap.representation(using: .png, properties: [:])!.write(to: output)
}
