// Package bleprinter talks to the printer over Bluetooth Low Energy
// using tinygo.org/x/bluetooth (Linux/BlueZ, macOS, Windows).
//
// It handles the FF00 service discovery and the credit-based flow
// control on FF03 used by the Quin / Print Master protocol: the
// printer grants credits with notifications [0x01, n]; each granted
// credit allows one chunk to be written to FF02. If the flow-control
// characteristic is missing or credits stall, it falls back to a
// paced write (like the Dart print_master_ble fallback mode).
package bleprinter

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"tinygo.org/x/bluetooth"

	"github.com/tsuzu/phomemo-p12pro/internal/quin"
)

var adapter = bluetooth.DefaultAdapter

// FoundDevice is one scanned BLE device.
type FoundDevice struct {
	Name           string
	Address        string
	RSSI           int16
	HasPrinterSvc  bool
	result         bluetooth.ScanResult
}

// Scan listens for BLE advertisements for the given duration and
// returns the unique devices seen.
func Scan(timeout time.Duration) ([]FoundDevice, error) {
	if err := adapter.Enable(); err != nil {
		return nil, fmt.Errorf("enable bluetooth adapter: %w", err)
	}
	svcUUID := bluetooth.New16BitUUID(quin.ServiceUUID16)
	var mu sync.Mutex
	seen := map[string]*FoundDevice{}
	done := make(chan error, 1)
	go func() {
		done <- adapter.Scan(func(a *bluetooth.Adapter, r bluetooth.ScanResult) {
			addr := r.Address.String()
			mu.Lock()
			defer mu.Unlock()
			d, ok := seen[addr]
			if !ok {
				d = &FoundDevice{Address: addr, result: r}
				seen[addr] = d
			}
			d.Name = r.LocalName()
			d.RSSI = r.RSSI
			if r.HasServiceUUID(svcUUID) {
				d.HasPrinterSvc = true
			}
		})
	}()
	time.Sleep(timeout)
	_ = adapter.StopScan()
	if err := <-done; err != nil {
		return nil, fmt.Errorf("scan: %w", err)
	}
	out := make([]FoundDevice, 0, len(seen))
	for _, d := range seen {
		out = append(out, *d)
	}
	return out, nil
}

// find scans until a device matching address (exact, case-insensitive)
// or name (substring, case-insensitive) shows up.
func find(address, name string, timeout time.Duration) (*FoundDevice, error) {
	devices, err := Scan(timeout)
	if err != nil {
		return nil, err
	}
	for i := range devices {
		d := &devices[i]
		if address != "" && strings.EqualFold(d.Address, address) {
			return d, nil
		}
		if name != "" && strings.Contains(strings.ToLower(d.Name), strings.ToLower(name)) {
			return d, nil
		}
	}
	return nil, fmt.Errorf("printer not found (address=%q name=%q); try `scan` to list devices", address, name)
}

// CharInfo describes one GATT characteristic for probe output.
type CharInfo struct {
	UUID string
}

// ServiceInfo describes one GATT service for probe output.
type ServiceInfo struct {
	UUID  string
	Chars []CharInfo
}

// connectResult bundles a connected device and its services.
type connectResult struct {
	device   bluetooth.Device
	services []bluetooth.DeviceService
}

func connect(found *FoundDevice) (*connectResult, error) {
	if err := adapter.Enable(); err != nil {
		return nil, fmt.Errorf("enable bluetooth adapter: %w", err)
	}
	dev, err := adapter.Connect(found.result.Address, bluetooth.ConnectionParams{})
	if err != nil {
		return nil, fmt.Errorf("connect %s: %w", found.Address, err)
	}
	svcs, err := dev.DiscoverServices(nil)
	if err != nil {
		_ = dev.Disconnect()
		return nil, fmt.Errorf("discover services: %w", err)
	}
	return &connectResult{device: dev, services: svcs}, nil
}

// Probe connects to the printer and dumps its full GATT tree. This is
// the decisive check for the protocol family: a Quin / Print Master
// printer exposes service FF00 with characteristics FF02 and FF03.
func Probe(address, name string, timeout time.Duration) ([]ServiceInfo, error) {
	found, err := find(address, name, timeout)
	if err != nil {
		return nil, err
	}
	conn, err := connect(found)
	if err != nil {
		return nil, err
	}
	defer conn.device.Disconnect()
	out := make([]ServiceInfo, 0, len(conn.services))
	for _, svc := range conn.services {
		si := ServiceInfo{UUID: svc.UUID().String()}
		chars, err := svc.DiscoverCharacteristics(nil)
		if err != nil {
			return nil, fmt.Errorf("discover characteristics of %s: %w", si.UUID, err)
		}
		for _, c := range chars {
			si.Chars = append(si.Chars, CharInfo{UUID: c.UUID().String()})
		}
		out = append(out, si)
	}
	return out, nil
}

// Printer is a connected printer ready to accept payload writes.
type Printer struct {
	device   bluetooth.Device
	write    bluetooth.DeviceCharacteristic
	hasWrite bool
	hasFlow  bool

	mu      sync.Mutex
	credits int
	notify  chan struct{} // signaled when credits increase
	// OnNotify, if set, receives non-flow-control notifications.
	OnNotify func(data []byte)
}

// Connect finds, connects and prepares the printer for printing.
func Connect(address, name string, timeout time.Duration) (*Printer, error) {
	found, err := find(address, name, timeout)
	if err != nil {
		return nil, err
	}
	conn, err := connect(found)
	if err != nil {
		return nil, err
	}
	p := &Printer{device: conn.device, notify: make(chan struct{}, 1)}
	svcUUID := bluetooth.New16BitUUID(quin.ServiceUUID16)
	writeUUID := bluetooth.New16BitUUID(quin.WriteCharUUID16)
	flowUUID := bluetooth.New16BitUUID(quin.FlowControlCharUUID16)

	var printerSvc *bluetooth.DeviceService
	for i := range conn.services {
		if conn.services[i].UUID() == svcUUID {
			printerSvc = &conn.services[i]
			break
		}
	}
	if printerSvc == nil {
		_ = conn.device.Disconnect()
		return nil, fmt.Errorf("service FF00 not found on %s — this is not a Quin/Print Master protocol printer (run `probe` to see its GATT tree)", found.Address)
	}
	chars, err := printerSvc.DiscoverCharacteristics(nil)
	if err != nil {
		_ = conn.device.Disconnect()
		return nil, fmt.Errorf("discover characteristics: %w", err)
	}
	var flow *bluetooth.DeviceCharacteristic
	for i := range chars {
		switch chars[i].UUID() {
		case writeUUID:
			p.write = chars[i]
			p.hasWrite = true
		case flowUUID:
			flow = &chars[i]
		}
	}
	if !p.hasWrite {
		_ = conn.device.Disconnect()
		return nil, fmt.Errorf("write characteristic FF02 not found on %s", found.Address)
	}
	if flow != nil {
		err := flow.EnableNotifications(func(buf []byte) {
			if len(buf) >= 2 && buf[0] == 0x01 {
				p.mu.Lock()
				p.credits += int(buf[1])
				p.mu.Unlock()
				select {
				case p.notify <- struct{}{}:
				default:
				}
				return
			}
			if p.OnNotify != nil {
				p.OnNotify(buf)
			}
		})
		if err == nil {
			p.hasFlow = true
		}
	}
	return p, nil
}

// Close disconnects from the printer.
func (p *Printer) Close() error { return p.device.Disconnect() }

// WritePayload sends payload to FF02 in chunks, honoring FF03 credits
// when available. Writes use WriteWithoutResponse: tinygo's Linux
// backend does not offer write-with-response on characteristics, and
// reliability here comes from the credit flow control plus pacing
// (the Dart reference library likewise falls back to paced writes).
// The withResponse argument is accepted for API stability and is
// currently ignored.
func (p *Printer) WritePayload(payload []byte, chunkSize int, withResponse bool) error {
	if chunkSize <= 0 {
		chunkSize = 200
	}
	useFlow := p.hasFlow
	for off := 0; off < len(payload); {
		if useFlow {
			ok := p.waitCredit(5 * time.Second)
			if !ok {
				// Printer stopped granting credits: fall back to
				// paced writes instead of deadlocking the job.
				useFlow = false
			} else {
				p.mu.Lock()
				p.credits--
				p.mu.Unlock()
			}
		}
		end := off + chunkSize
		if end > len(payload) {
			end = len(payload)
		}
		chunk := payload[off:end]
		_, err := p.write.WriteWithoutResponse(chunk)
		if err != nil {
			return fmt.Errorf("write chunk at %d: %w", off, err)
		}
		off = end
		if useFlow {
			time.Sleep(1 * time.Millisecond)
		} else {
			time.Sleep(2 * time.Millisecond)
		}
	}
	return nil
}

func (p *Printer) waitCredit(timeout time.Duration) bool {
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	for {
		p.mu.Lock()
		c := p.credits
		p.mu.Unlock()
		if c > 0 {
			return true
		}
		select {
		case <-p.notify:
		case <-deadline.C:
			return false
		}
	}
}
