import 'dart:io';

bool get runtimeFontFetchingEnabled =>
    !Platform.environment.containsKey('FLUTTER_TEST');
