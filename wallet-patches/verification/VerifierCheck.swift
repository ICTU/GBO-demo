import Foundation

@main
struct VerifierCheck {
  static func main() throws {
    let folder = URL(fileURLWithPath: CommandLine.arguments[1])
    func data(_ name: String) throws -> Data { try Data(contentsOf: folder.appendingPathComponent(name)) }
    func jwt(_ name: String) throws -> String { String(decoding: try data(name + ".jwt"), as: UTF8.self) }
    let verifier = GboLoTEVerifier(root: try data("root.der"))
    let now = ISO8601DateFormatter().date(from: "2026-11-11T12:00:00Z")!
    try verifier.verify(jwt("valid"), now: now)
    for name in ["eu-territory", "wrong-role", "expired", "future", "wrong-alg", "bad-signature"] {
      do { try verifier.verify(jwt(name), now: now); fatalError("Accepted \(name)") }
      catch { print("Rejected \(name)") }
    }
    let wrong = GboLoTEVerifier(root: try data("wrong-root.der"))
    do { try wrong.verify(jwt("valid"), now: now); fatalError("Accepted unknown signer") }
    catch { print("Rejected unknown signer") }
    for malformed in ["", "a.b", "a.b.c", ".."] {
      do { try verifier.verify(malformed, now: now); fatalError("Accepted malformed JWT") } catch {}
    }
    print("Swift LoTE signature and GBO profile checks passed")
  }
}
