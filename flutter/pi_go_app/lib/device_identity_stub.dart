import 'dart:typed_data';

import 'push_identity.dart';

bool get softwareDeviceIdentitySupported => false;
Future<DeviceIdentity?> getSoftwareDeviceIdentity() async => null;
Future<String> softwareIdentityRandomNonce() =>
    Future.error(UnsupportedError('Software device identity is unavailable'));
Future<String> signSoftwareDeviceIdentityPayload(String payload) =>
    Future.error(UnsupportedError('Software device identity is unavailable'));
Future<bool> verifySoftwareDeviceIdentitySignature({
  required Map<String, dynamic> publicKey,
  required String payload,
  required String signature,
}) async => false;
Future<String> softwareDeviceIdentitySHA256(Uint8List bytes) =>
    Future.error(UnsupportedError('Software device identity is unavailable'));
