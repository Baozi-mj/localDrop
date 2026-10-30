// Decode QR codes from image files using Apple's Vision framework
// (the same decoder family iOS uses for camera scanning).
//
// Usage:  swift tools/qrcheck.swift <image.png> [more.png ...]
//
// Used to verify LocalDrop's QR output independently of Go QR libraries.
import Foundation
import Vision
import AppKit

for path in CommandLine.arguments.dropFirst() {
    guard let img = NSImage(contentsOfFile: path),
          let cg = img.cgImage(forProposedRect: nil, context: nil, hints: nil) else {
        print("\(path): LOAD-FAIL")
        continue
    }
    let request = VNDetectBarcodesRequest()
    request.symbologies = [.qr]
    let handler = VNImageRequestHandler(cgImage: cg, options: [:])
    do {
        try handler.perform([request])
        if let results = request.results, !results.isEmpty {
            for r in results {
                print("\(path): \(r.payloadStringValue ?? "<nil>")")
            }
        } else {
            print("\(path): NO-QR-FOUND")
        }
    } catch {
        print("\(path): ERROR \(error)")
    }
}
