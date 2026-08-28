export 'device_identity_stub.dart'
    if (dart.library.io) 'device_identity_io.dart'
    if (dart.library.js_interop) 'device_identity_web.dart';
