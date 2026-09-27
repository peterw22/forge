package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// A device entry is a few hundred bytes. The limit only stops a wrong file
// from being read whole.
const maxDeviceEntryBytes = 64 << 10

// authorizeDevice adds the device entry read from input, as Forge copies it,
// to the whitelist at path. A missing whitelist is created. It reports false
// when the device was already listed with the same key.
func authorizeDevice(path string, input io.Reader) (authorizedDevice, bool, error) {
	encoded, err := io.ReadAll(io.LimitReader(input, maxDeviceEntryBytes+1))
	if err != nil {
		return authorizedDevice{}, false, err
	}
	if len(encoded) > maxDeviceEntryBytes {
		return authorizedDevice{}, false, errors.New("device entry is too large")
	}
	var device authorizedDevice
	decoder := json.NewDecoder(strings.NewReader(string(encoded)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&device); err != nil {
		return authorizedDevice{}, false, fmt.Errorf("device entry is not the JSON that Forge copies: %w", err)
	}
	if decoder.More() {
		return authorizedDevice{}, false, errors.New("device entry must be one JSON object")
	}
	device.Name = strings.TrimSpace(device.Name)
	device.DeviceID = strings.TrimSpace(device.DeviceID)
	if device.DeviceID == "" {
		return authorizedDevice{}, false, errors.New("device entry has no deviceId")
	}
	if _, err := publicKeyFromJWK(device.PublicKey); err != nil {
		return authorizedDevice{}, false, err
	}
	if device.Fingerprint != fingerprintJWK(device.PublicKey) {
		return authorizedDevice{}, false, errors.New("device entry fingerprint does not match its public key")
	}

	config := authorizedDeviceFile{Version: 1, Devices: []authorizedDevice{}}
	existing, err := os.ReadFile(path)
	switch {
	case err == nil:
		if err := json.Unmarshal(existing, &config); err != nil {
			return authorizedDevice{}, false, fmt.Errorf("decode authorized devices: %w", err)
		}
		if config.Version != 1 {
			return authorizedDevice{}, false, errors.New("authorized devices version must be 1")
		}
	case !errors.Is(err, os.ErrNotExist):
		return authorizedDevice{}, false, err
	}
	for _, listed := range config.Devices {
		same := listed.Fingerprint == device.Fingerprint
		if listed.DeviceID == device.DeviceID && same {
			return listed, false, nil
		}
		if listed.DeviceID == device.DeviceID {
			return authorizedDevice{}, false, fmt.Errorf("device %q is listed with another key; remove it from %s first", device.DeviceID, path)
		}
		if same {
			return authorizedDevice{}, false, fmt.Errorf("the key is listed for device %q; remove it from %s first", listed.DeviceID, path)
		}
	}
	config.Devices = append(config.Devices, device)
	if len(config.Devices) > 1024 {
		return authorizedDevice{}, false, errors.New("authorized device count exceeds 1024")
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return authorizedDevice{}, false, err
	}
	payload, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return authorizedDevice{}, false, err
	}
	payload = append(payload, '\n')
	temporary := path + ".tmp"
	if err := os.WriteFile(temporary, payload, 0o600); err != nil {
		return authorizedDevice{}, false, err
	}
	if err := os.Chmod(temporary, 0o600); err != nil {
		_ = os.Remove(temporary)
		return authorizedDevice{}, false, err
	}
	if err := os.Rename(temporary, path); err != nil {
		_ = os.Remove(temporary)
		return authorizedDevice{}, false, err
	}
	return device, true, nil
}

// runAuthorizeDevice is the --authorize-device command.
func runAuthorizeDevice(path string, input io.Reader, output io.Writer) error {
	if strings.TrimSpace(path) == "" {
		defaultPath, err := defaultAuthorizedDevicesPath()
		if err != nil {
			return err
		}
		path = defaultPath
	}
	device, added, err := authorizeDevice(path, input)
	if err != nil {
		return err
	}
	name := device.Name
	if name == "" {
		name = device.DeviceID
	}
	if !added {
		fmt.Fprintf(output, "%s is already authorized\n", name)
		return nil
	}
	fmt.Fprintf(output, "Authorized %s\nP-256 fingerprint: %s\nRestart a running agent to let the device in.\n", name, device.Fingerprint)
	return nil
}
