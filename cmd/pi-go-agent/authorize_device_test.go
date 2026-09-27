package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testDeviceEntry(t *testing.T, id string) (authorizedDevice, string) {
	t.Helper()
	_, jwk, fingerprint := testAuthIdentity(t, id)
	device := authorizedDevice{Name: "Phone of " + id, DeviceID: id, PublicKey: jwk, Fingerprint: fingerprint}
	// Forge copies the entry indented.
	encoded, err := json.MarshalIndent(device, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return device, string(encoded)
}

func TestAuthorizeDeviceCreatesWhitelistTheAgentAccepts(t *testing.T) {
	root := t.TempDir()
	t.Setenv("PI_GO_CONFIG_DIR", root)
	path := filepath.Join(root, "nested", "authorized-devices.json")
	device, entry := testDeviceEntry(t, "device-one")
	var output bytes.Buffer
	if err := runAuthorizeDevice(path, strings.NewReader(entry), &output); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), device.Fingerprint) {
		t.Fatalf("output=%q", output.String())
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode=%v", info.Mode().Perm())
	}
	policy, err := loadClientAuthPolicy(path)
	if err != nil {
		t.Fatal(err)
	}
	if listed, ok := policy.devices["device-one"]; !ok || listed.Name != device.Name {
		t.Fatalf("devices=%#v", policy.devices)
	}
}

func TestAuthorizeDeviceKeepsListedDevices(t *testing.T) {
	path := filepath.Join(t.TempDir(), "authorized-devices.json")
	_, first := testDeviceEntry(t, "device-one")
	_, second := testDeviceEntry(t, "device-two")
	for _, entry := range []string{first, second} {
		if _, added, err := authorizeDevice(path, strings.NewReader(entry)); err != nil || !added {
			t.Fatalf("added=%v err=%v", added, err)
		}
	}
	// The same entry again changes nothing.
	before, _ := os.ReadFile(path)
	if _, added, err := authorizeDevice(path, strings.NewReader(first)); err != nil || added {
		t.Fatalf("added=%v err=%v", added, err)
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(before, after) {
		t.Fatal("whitelist changed for a listed device")
	}
	var config authorizedDeviceFile
	if err := json.Unmarshal(after, &config); err != nil {
		t.Fatal(err)
	}
	if len(config.Devices) != 2 || config.Devices[0].DeviceID != "device-one" || config.Devices[1].DeviceID != "device-two" {
		t.Fatalf("devices=%#v", config.Devices)
	}
}

func TestAuthorizeDeviceRejectsBadEntries(t *testing.T) {
	device, entry := testDeviceEntry(t, "device-one")
	other, _ := testDeviceEntry(t, "device-two")
	encode := func(device authorizedDevice) string {
		encoded, _ := json.Marshal(device)
		return string(encoded)
	}
	wrongFingerprint := device
	wrongFingerprint.Fingerprint = other.Fingerprint
	noID := device
	noID.DeviceID = " "
	offCurve := device
	offCurve.PublicKey.Y = device.PublicKey.X
	cases := map[string]string{
		"empty":             "",
		"not JSON":          "device-one",
		"a list":            "[" + entry + "]",
		"two entries":       entry + entry,
		"unknown field":     `{"deviceId":"x","admin":true}`,
		"wrong fingerprint": encode(wrongFingerprint),
		"no deviceId":       encode(noID),
		"key off the curve": encode(offCurve),
		"too large":         entry + strings.Repeat(" ", maxDeviceEntryBytes),
	}
	for name, input := range cases {
		path := filepath.Join(t.TempDir(), "authorized-devices.json")
		if _, _, err := authorizeDevice(path, strings.NewReader(input)); err == nil {
			t.Errorf("%s: accepted", name)
		}
		if _, err := os.Stat(path); err == nil {
			t.Errorf("%s: whitelist written", name)
		}
	}
}

func TestAuthorizeDeviceRejectsConflictsAndKeepsTheFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "authorized-devices.json")
	device, entry := testDeviceEntry(t, "device-one")
	if _, _, err := authorizeDevice(path, strings.NewReader(entry)); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(path)
	other, _ := testDeviceEntry(t, "device-two")
	sameID := other
	sameID.DeviceID = device.DeviceID
	sameKey := device
	sameKey.DeviceID = "device-three"
	for name, conflict := range map[string]authorizedDevice{"same deviceId": sameID, "same key": sameKey} {
		encoded, _ := json.Marshal(conflict)
		if _, _, err := authorizeDevice(path, bytes.NewReader(encoded)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	// A whitelist that cannot be read is not replaced.
	broken := filepath.Join(t.TempDir(), "authorized-devices.json")
	if err := os.WriteFile(broken, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := authorizeDevice(broken, strings.NewReader(entry)); err == nil {
		t.Error("broken whitelist replaced")
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(before, after) {
		t.Fatal("whitelist changed by a rejected entry")
	}
}
