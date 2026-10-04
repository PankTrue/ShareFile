import AppKit

let directory = URL(fileURLWithPath: CommandLine.arguments[1], isDirectory: true)
try FileManager.default.createDirectory(at: directory, withIntermediateDirectories: true)
for size in [16, 32, 128, 256, 512] {
    for scale in [1, 2] {
        let pixels = size * scale
        let bitmap = NSBitmapImageRep(bitmapDataPlanes: nil, pixelsWide: pixels, pixelsHigh: pixels,
            bitsPerSample: 8, samplesPerPixel: 4, hasAlpha: true, isPlanar: false,
            colorSpaceName: .deviceRGB, bytesPerRow: pixels * 4, bitsPerPixel: 32)!
        NSGraphicsContext.saveGraphicsState()
        NSGraphicsContext.current = NSGraphicsContext(bitmapImageRep: bitmap)
        let p = CGFloat(pixels)
        NSColor(red: 0.15, green: 0.39, blue: 0.32, alpha: 1).setFill()
        NSBezierPath(roundedRect: NSRect(x: p * 0.06, y: p * 0.06, width: p * 0.88, height: p * 0.88), xRadius: p * 0.20, yRadius: p * 0.20).fill()
        NSColor.white.setStroke()
        let path = NSBezierPath()
        path.lineWidth = p * 0.064; path.lineCapStyle = .round; path.lineJoinStyle = .round
        path.move(to: NSPoint(x: p * 0.30, y: p * 0.30)); path.line(to: NSPoint(x: p * 0.30, y: p * 0.23))
        path.line(to: NSPoint(x: p * 0.70, y: p * 0.23)); path.line(to: NSPoint(x: p * 0.70, y: p * 0.30))
        path.move(to: NSPoint(x: p * 0.50, y: p * 0.38)); path.line(to: NSPoint(x: p * 0.50, y: p * 0.73))
        path.move(to: NSPoint(x: p * 0.34, y: p * 0.57)); path.line(to: NSPoint(x: p * 0.50, y: p * 0.73)); path.line(to: NSPoint(x: p * 0.66, y: p * 0.57))
        path.stroke(); NSGraphicsContext.restoreGraphicsState()
        let suffix = scale == 2 ? "@2x" : ""
        try bitmap.representation(using: .png, properties: [:])!.write(to: directory.appendingPathComponent("icon_\(size)x\(size)\(suffix).png"))
    }
}
