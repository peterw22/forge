abstract class AgentTransport {
  Stream<String> get messages;
  void send(String message);
  Future<void> close();
}
