import SwiftUI
import VisionKit

/// The pair screen's QR reader: the board's ◉ Devices panel shows a code
/// encoding `http://<dns-name>:<port>`, and scanning it fills the seed field.
/// VisionKit's live scanner does the camera work; it is unavailable on the
/// simulator and on devices without a supported camera, which is why the
/// pair screen keeps the typed field as the always-working path.
struct PairScannerView: UIViewControllerRepresentable {
    /// Called once with the first QR payload that reads as an http(s) URL.
    let onScan: (String) -> Void

    static var isAvailable: Bool {
        DataScannerViewController.isSupported && DataScannerViewController.isAvailable
    }

    func makeUIViewController(context: Context) -> DataScannerViewController {
        let scanner = DataScannerViewController(
            recognizedDataTypes: [.barcode(symbologies: [.qr])],
            qualityLevel: .balanced,
            isHighlightingEnabled: true
        )
        scanner.delegate = context.coordinator
        try? scanner.startScanning()
        return scanner
    }

    func updateUIViewController(_ scanner: DataScannerViewController, context: Context) {}

    static func dismantleUIViewController(_ scanner: DataScannerViewController, coordinator: Coordinator) {
        scanner.stopScanning()
    }

    func makeCoordinator() -> Coordinator { Coordinator(onScan: onScan) }

    final class Coordinator: NSObject, DataScannerViewControllerDelegate {
        let onScan: (String) -> Void
        private var delivered = false

        init(onScan: @escaping (String) -> Void) { self.onScan = onScan }

        func dataScanner(_ dataScanner: DataScannerViewController, didAdd addedItems: [RecognizedItem],
                         allItems: [RecognizedItem]) {
            guard !delivered else { return }
            for item in addedItems {
                guard case .barcode(let code) = item, let payload = code.payloadStringValue,
                      let url = URL(string: payload.trimmingCharacters(in: .whitespacesAndNewlines)),
                      let scheme = url.scheme?.lowercased(), scheme == "http" || scheme == "https",
                      url.host != nil else { continue }
                delivered = true
                onScan(url.absoluteString)
                return
            }
        }
    }
}

/// The scanner in a sheet with a cancel button and one line saying what to
/// point it at.
struct PairScannerSheet: View {
    let onScan: (String) -> Void
    @Environment(\.dismiss) private var dismiss

    var body: some View {
        NavigationStack {
            PairScannerView(onScan: onScan)
                .ignoresSafeArea(edges: .bottom)
                .overlay(alignment: .bottom) {
                    Text("Point at the pairing code on the board's ◉ Devices panel")
                        .font(.voice(12))
                        .foregroundStyle(KTheme.text)
                        .padding(.horizontal, 14).padding(.vertical, 8)
                        .background(.ultraThinMaterial, in: Capsule())
                        .padding(.bottom, 30)
                }
                .navigationTitle("Scan pairing code")
                .navigationBarTitleDisplayMode(.inline)
                .toolbar {
                    ToolbarItem(placement: .cancellationAction) {
                        SheetCloseButton { dismiss() }
                    }
                }
        }
    }
}
