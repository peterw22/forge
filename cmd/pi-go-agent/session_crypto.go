package main

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/hkdf"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
)

const sessionCipher = "P256-HKDF-SHA256-AES-256-GCM"

type encryptedEnvelope struct {
	Type       string `json:"type"`
	Version    int    `json:"version"`
	Sequence   uint64 `json:"sequence"`
	Ciphertext string `json:"ciphertext"`
}

type secureSession struct {
	connectionID  string
	sendDirection string
	recvDirection string
	sendAEAD      cipher.AEAD
	recvAEAD      cipher.AEAD
	sendPrefix    [4]byte
	recvPrefix    [4]byte
	sendSequence  uint64
	recvSequence  uint64
	sendMu        sync.Mutex
	recvMu        sync.Mutex
}

func newServerSecureSession(command backendCommand, serverPrivate *ecdh.PrivateKey, identity *serverIdentity) (*secureSession, error) {
	clientPublic, err := ecdhPublicFromJWK(command.ClientEphemeralKey)
	if err != nil {
		return nil, err
	}
	secret, err := serverPrivate.ECDH(clientPublic)
	if err != nil {
		return nil, err
	}
	return deriveSecureSession(command, identity, secret, false)
}

func deriveSecureSession(command backendCommand, identity *serverIdentity, secret []byte, client bool) (*secureSession, error) {
	saltText := strings.Join([]string{"FORGE-SESSION-SALT-V1", command.ConnectionID, command.ClientNonce, command.ServerNonce, command.DeviceFingerprint, identity.Fingerprint}, "\n")
	salt := sha256.Sum256([]byte(saltText))
	prk, err := hkdf.Extract(sha256.New, secret, salt[:])
	if err != nil {
		return nil, err
	}
	expand := func(label string, size int) ([]byte, error) {
		return hkdf.Expand(sha256.New, prk, label, size)
	}
	clientKey, err := expand("forge-v1 client-to-server key", 32)
	if err != nil {
		return nil, err
	}
	serverKey, err := expand("forge-v1 server-to-client key", 32)
	if err != nil {
		return nil, err
	}
	clientPrefix, err := expand("forge-v1 client nonce", 4)
	if err != nil {
		return nil, err
	}
	serverPrefix, err := expand("forge-v1 server nonce", 4)
	if err != nil {
		return nil, err
	}
	makeAEAD := func(key []byte) (cipher.AEAD, error) {
		block, err := aes.NewCipher(key)
		if err != nil {
			return nil, err
		}
		return cipher.NewGCM(block)
	}
	clientAEAD, err := makeAEAD(clientKey)
	if err != nil {
		return nil, err
	}
	serverAEAD, err := makeAEAD(serverKey)
	if err != nil {
		return nil, err
	}
	session := &secureSession{connectionID: command.ConnectionID}
	if client {
		session.sendAEAD, session.recvAEAD = clientAEAD, serverAEAD
		session.sendDirection, session.recvDirection = "client-to-server", "server-to-client"
		copy(session.sendPrefix[:], clientPrefix)
		copy(session.recvPrefix[:], serverPrefix)
	} else {
		session.sendAEAD, session.recvAEAD = serverAEAD, clientAEAD
		session.sendDirection, session.recvDirection = "server-to-client", "client-to-server"
		copy(session.sendPrefix[:], serverPrefix)
		copy(session.recvPrefix[:], clientPrefix)
	}
	return session, nil
}

func ephemeralJWK(key *ecdh.PublicKey) (pushJWK, error) {
	raw := key.Bytes()
	if len(raw) != 65 || raw[0] != 4 {
		return pushJWK{}, errors.New("invalid P-256 ephemeral public key")
	}
	return pushJWK{KTY: "EC", CRV: "P-256", X: base64URL(raw[1:33]), Y: base64URL(raw[33:65])}, nil
}

func ecdhPublicFromJWK(jwk pushJWK) (*ecdh.PublicKey, error) {
	if jwk.KTY != "EC" || jwk.CRV != "P-256" {
		return nil, errors.New("ephemeral key must be P-256")
	}
	x, err := decodeBase64URL(jwk.X)
	if err != nil || len(x) != 32 {
		return nil, errors.New("invalid ephemeral x")
	}
	y, err := decodeBase64URL(jwk.Y)
	if err != nil || len(y) != 32 {
		return nil, errors.New("invalid ephemeral y")
	}
	raw := append([]byte{4}, append(x, y...)...)
	return ecdh.P256().NewPublicKey(raw)
}

func (session *secureSession) encrypt(value any) (backendResponse, error) {
	session.sendMu.Lock()
	defer session.sendMu.Unlock()
	plain, err := json.Marshal(value)
	if err != nil {
		return backendResponse{}, err
	}
	sequence := session.sendSequence
	nonce := make([]byte, 12)
	copy(nonce[:4], session.sendPrefix[:])
	binary.BigEndian.PutUint64(nonce[4:], sequence)
	aad := []byte(fmt.Sprintf("FORGE-ENCRYPTED-V1\n%s\n%s\n%d", session.connectionID, session.sendDirection, sequence))
	ciphertext := session.sendAEAD.Seal(nil, nonce, plain, aad)
	session.sendSequence++
	return backendResponse{Type: "encrypted", EncryptedVersion: 1, EncryptedSequence: sequence, Ciphertext: base64URL(ciphertext)}, nil
}

func (session *secureSession) decrypt(response backendCommand, destination any) error {
	session.recvMu.Lock()
	defer session.recvMu.Unlock()
	if response.Type != "encrypted" || response.EncryptedVersion != 1 || response.EncryptedSequence != session.recvSequence {
		return errors.New("invalid encrypted sequence")
	}
	ciphertext, err := decodeBase64URL(response.Ciphertext)
	if err != nil {
		return err
	}
	nonce := make([]byte, 12)
	copy(nonce[:4], session.recvPrefix[:])
	binary.BigEndian.PutUint64(nonce[4:], response.EncryptedSequence)
	aad := []byte(fmt.Sprintf("FORGE-ENCRYPTED-V1\n%s\n%s\n%d", session.connectionID, session.recvDirection, response.EncryptedSequence))
	plain, err := session.recvAEAD.Open(nil, nonce, ciphertext, aad)
	if err != nil {
		return errors.New("encrypted message authentication failed")
	}
	if err := json.Unmarshal(plain, destination); err != nil {
		return err
	}
	session.recvSequence++
	return nil
}
