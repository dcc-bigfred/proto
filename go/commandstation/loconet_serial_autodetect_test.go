package commandstation

import "testing"

func TestResolveSerialDevice(t *testing.T) {
	origList := listSerialPorts
	origExists := serialDeviceExists
	t.Cleanup(func() {
		listSerialPorts = origList
		serialDeviceExists = origExists
	})

	listSerialPorts = func() ([]string, error) {
		return []string{"/dev/ttyS0", "/dev/ttyUSB1", "/dev/ttyACM0"}, nil
	}
	serialDeviceExists = func(path string) bool { return path == "/dev/loconet-63120" }
	got, err := resolveSerialDevice()
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got != "/dev/loconet-63120" {
		t.Fatalf("got %q, want /dev/loconet-63120 (udev symlink preferred)", got)
	}

	serialDeviceExists = func(string) bool { return false }
	got, err = resolveSerialDevice()
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got != "/dev/ttyUSB1" {
		t.Fatalf("got %q, want /dev/ttyUSB1 (USB preferred)", got)
	}

	listSerialPorts = func() ([]string, error) { return nil, nil }
	if _, err := resolveSerialDevice(); err == nil {
		t.Fatalf("expected error on empty port list")
	}
}

func TestResolveSerialDevicePrefersLoconetSymlinks(t *testing.T) {
	origList := listSerialPorts
	origExists := serialDeviceExists
	t.Cleanup(func() {
		listSerialPorts = origList
		serialDeviceExists = origExists
	})

	listSerialPorts = func() ([]string, error) {
		return []string{"/dev/ttyUSB0", "/dev/ttyACM0"}, nil
	}
	serialDeviceExists = func(path string) bool {
		return path == "/dev/loconet-lb-usb" || path == "/dev/loconet-ch340"
	}
	got, err := resolveSerialDevice()
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	// Lexicographic among priority-0 symlinks: ch340 before lb-usb.
	if got != "/dev/loconet-ch340" {
		t.Fatalf("got %q, want /dev/loconet-ch340", got)
	}
}

func TestResolveSerialDeviceExcludesOnboardUART(t *testing.T) {
	origList := listSerialPorts
	origExists := serialDeviceExists
	t.Cleanup(func() {
		listSerialPorts = origList
		serialDeviceExists = origExists
	})

	listSerialPorts = func() ([]string, error) {
		return []string{"/dev/ttyAMA0", "/dev/ttyS0", "/dev/ttyACM0"}, nil
	}
	serialDeviceExists = func(string) bool { return false }
	got, err := resolveSerialDevice()
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got != "/dev/ttyACM0" {
		t.Fatalf("got %q, want /dev/ttyACM0 (ACM over onboard UART)", got)
	}

	listSerialPorts = func() ([]string, error) {
		return []string{"/dev/ttyAMA0", "/dev/ttyS1"}, nil
	}
	if _, err := resolveSerialDevice(); err == nil {
		t.Fatalf("expected error when only onboard UARTs are present")
	}
}

func TestIsOnboardUART(t *testing.T) {
	cases := []struct {
		path string
		want bool
	}{
		{"/dev/ttyAMA0", true},
		{"/dev/ttyS0", true},
		{"/dev/ttymxc0", true},
		{"/dev/ttyUSB0", false},
		{"/dev/ttyACM0", false},
		{"/dev/loconet-63120", false},
	}
	for _, tc := range cases {
		if got := isOnboardUART(tc.path); got != tc.want {
			t.Errorf("isOnboardUART(%q)=%v, want %v", tc.path, got, tc.want)
		}
	}
}
