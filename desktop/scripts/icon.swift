import AppKit

let output = URL(fileURLWithPath: CommandLine.arguments[1]).appendingPathComponent("icon.iconset")
try FileManager.default.createDirectory(at: output, withIntermediateDirectories: true)

func render(_ size: Int, _ name: String) throws {
    let bitmap = NSBitmapImageRep(bitmapDataPlanes: nil, pixelsWide: size, pixelsHigh: size,
        bitsPerSample: 8, samplesPerPixel: 4, hasAlpha: true, isPlanar: false,
        colorSpaceName: .deviceRGB, bytesPerRow: 0, bitsPerPixel: 0)!
    let context = NSGraphicsContext(bitmapImageRep: bitmap)!
    NSGraphicsContext.saveGraphicsState()
    NSGraphicsContext.current = context
    let scale = CGFloat(size) / 1024
    context.cgContext.scaleBy(x: scale, y: scale)
    NSColor(calibratedRed: 0.08, green: 0.48, blue: 0.42, alpha: 1).setFill()
    NSBezierPath(roundedRect: NSRect(x: 64, y: 64, width: 896, height: 896), xRadius: 200, yRadius: 200).fill()
    NSColor.white.setStroke()
    let arrows = NSBezierPath()
    arrows.lineWidth = 62
    arrows.lineCapStyle = .round
    arrows.lineJoinStyle = .round
    arrows.move(to: NSPoint(x: 270, y: 640))
    arrows.line(to: NSPoint(x: 740, y: 640))
    arrows.move(to: NSPoint(x: 610, y: 775))
    arrows.line(to: NSPoint(x: 745, y: 640))
    arrows.line(to: NSPoint(x: 610, y: 505))
    arrows.move(to: NSPoint(x: 754, y: 384))
    arrows.line(to: NSPoint(x: 284, y: 384))
    arrows.move(to: NSPoint(x: 414, y: 519))
    arrows.line(to: NSPoint(x: 279, y: 384))
    arrows.line(to: NSPoint(x: 414, y: 249))
    arrows.stroke()
    NSGraphicsContext.restoreGraphicsState()
    try bitmap.representation(using: .png, properties: [:])!.write(to: output.appendingPathComponent(name))
}

for size in [16, 32, 128, 256, 512] {
    try render(size, "icon_\(size)x\(size).png")
    try render(size * 2, "icon_\(size)x\(size)@2x.png")
}
