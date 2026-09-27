// What a browser does with a Web Push message (RFC 8291), written apart from
// the code under test.
const bytes = (value: Uint8Array): ArrayBuffer => Uint8Array.from(value).buffer;

async function hkdf(material: Uint8Array, salt: Uint8Array, info: Uint8Array, length: number): Promise<Uint8Array> {
  const key = await crypto.subtle.importKey("raw", bytes(material), "HKDF", false, ["deriveBits"]);
  return new Uint8Array(await crypto.subtle.deriveBits(
    { name: "HKDF", hash: "SHA-256", salt: bytes(salt), info: bytes(info) }, key, length * 8,
  ));
}

export async function decryptAsReceiver(
  body: Uint8Array,
  receiver: CryptoKeyPair,
  auth: Uint8Array,
): Promise<string> {
  const salt = body.slice(0, 16);
  const keyLength = body[20]!;
  const senderPublic = body.slice(21, 21 + keyLength);
  const receiverPublic = new Uint8Array(await crypto.subtle.exportKey("raw", receiver.publicKey) as ArrayBuffer);
  const senderKey = await crypto.subtle.importKey(
    "raw", bytes(senderPublic), { name: "ECDH", namedCurve: "P-256" }, false, [],
  );
  const shared = new Uint8Array(await crypto.subtle.deriveBits(
    { name: "ECDH", public: senderKey }, receiver.privateKey, 256,
  ));
  const encoder = new TextEncoder();
  const info = new Uint8Array([...encoder.encode("WebPush: info\0"), ...receiverPublic, ...senderPublic]);
  const material = await hkdf(shared, auth, info, 32);
  const contentKey = await hkdf(material, salt, encoder.encode("Content-Encoding: aes128gcm\0"), 16);
  const nonce = await hkdf(material, salt, encoder.encode("Content-Encoding: nonce\0"), 12);
  const key = await crypto.subtle.importKey("raw", bytes(contentKey), "AES-GCM", false, ["decrypt"]);
  const plaintext = new Uint8Array(await crypto.subtle.decrypt(
    { name: "AES-GCM", iv: bytes(nonce) }, key, bytes(body.slice(21 + keyLength)),
  ));
  if (plaintext[plaintext.length - 1] !== 2) throw new Error("the record has no end");
  return new TextDecoder().decode(plaintext.slice(0, -1));
}
