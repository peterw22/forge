/// Only transports established by our own helper may accept plaintext state:
/// either an owned child or a Unix peer verified by kernel credentials.
abstract interface class TrustedLocalTransport implements AgentTransport {}

abstract class AgentTransport {
  Stream<String> get messages;
  void send(String message);
  Future<void> close();
}
