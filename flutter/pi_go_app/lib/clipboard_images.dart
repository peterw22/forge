import 'dart:async';
import 'dart:typed_data';

import 'package:super_clipboard/super_clipboard.dart';

const supportedClipboardImageMimeTypes = <String>[
  'image/png',
  'image/jpeg',
  'image/gif',
  'image/webp',
];

class ClipboardImage {
  const ClipboardImage(this.name, this.mimeType, this.bytes);

  final String name;
  final String mimeType;
  final Uint8List bytes;
}

const _formats = <(FileFormat, String, String)>[
  (Formats.png, 'image/png', 'png'),
  (Formats.jpeg, 'image/jpeg', 'jpg'),
  (Formats.gif, 'image/gif', 'gif'),
  (Formats.webp, 'image/webp', 'webp'),
];

Future<List<ClipboardImage>> readClipboardImages(ClipboardReader reader) async {
  final images = <ClipboardImage>[];
  for (final item in reader.items) {
    final image = await _readClipboardImage(item, images.length + 1);
    if (image != null) images.add(image);
  }
  return images;
}

Future<ClipboardImage?> _readClipboardImage(
  ClipboardDataReader item,
  int index,
) async {
  final suggestedName = await item.getSuggestedName();
  for (final (format, mimeType, extension) in _formats) {
    if (!item.canProvide(format)) continue;

    final result = Completer<ClipboardImage?>();
    final progress = item.getFile(format, (file) async {
      try {
        final bytes = await file.readAll();
        final name =
            file.fileName ?? suggestedName ?? 'pasted-image-$index.$extension';
        result.complete(ClipboardImage(name, mimeType, bytes));
      } catch (error, stackTrace) {
        result.completeError(error, stackTrace);
      }
    }, onError: (error) => result.completeError(error));
    if (progress != null) return result.future;
  }
  return null;
}
