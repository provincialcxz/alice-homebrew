//go:build airplay

// AirPlay audio service. MIT, Alice Homebrew contributors. The embedded
// Shairport/FFmpeg/ALSA/OpenSSL receiver retains its respective licenses.
package main

import (
	"alicehomebrew/plugins/internal/sharedaudio"
	"bytes"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/hashicorp/mdns"
	"log"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
)

//go:embed receiver-arm64
var receiver []byte

type configuration struct {
	Name      string  `json:"name"`
	Password  string  `json:"password"`
	Device    string  `json:"device"`
	MaxVolume float64 `json:"max-volume"`
	Port      int     `json:"port"`
	AudioMode string  `json:"audio-mode"`
}

func configText(c configuration) (string, error) {
	if c.Name == "" || len([]byte(c.Name)) > 48 || strings.ContainsAny(c.Name+c.Password, "\x00\r\n") {
		return "", errors.New("name must be 1-48 UTF-8 bytes; newlines are not allowed")
	}
	if c.Port < 1024 || c.Port > 65535 || c.MaxVolume < -60 || c.MaxVolume > 0 {
		return "", errors.New("invalid port or volume cap")
	}
	switch c.Device {
	case "plughw:0,0", "plughw:0,1":
	default:
		return "", errors.New("choose an available Mini 1 playback PCM")
	}
	backend := "stdout"
	if c.AudioMode == "direct" {
		backend = "alsa"
	} else if c.AudioMode != "" && c.AudioMode != "shared" {
		return "", errors.New("choose shared or direct audio mode")
	}
	// JSON quoted strings are compatible with libconfig string escaping here.
	q := func(s string) string { b, _ := json.Marshal(s); return string(b) }
	return fmt.Sprintf(`general = {
 name = %s;
 password = %s;
 port = %d;
 output_backend = %s;
 mdns_backend = "external-avahi";
 alac_decoder = "ffmpeg";
 volume_max_db = %.1f;
 volume_range_db = 60.0;
 ignore_volume_control = "no";
};
alsa = { output_device = %s; output_rate = 48000; output_format = "S32"; disable_standby_mode = "never"; };
stdout = { output_rate = 44100; output_format = "S16"; output_channels = 2; };
`, q(c.Name), q(c.Password), c.Port, q(backend), c.MaxVolume, q("alice_out")), nil
}
func descriptor(b []byte) (string, string, int, []string, error) {
	if len(b) > 16384 {
		return "", "", 0, nil, errors.New("discovery descriptor too long")
	}
	lines := strings.Split(strings.TrimSuffix(string(b), "\n"), "\n")
	if len(lines) < 4 || len(lines) > 43 {
		return "", "", 0, nil, errors.New("invalid discovery descriptor")
	}
	port, e := strconv.Atoi(lines[0])
	if e != nil || port < 1024 || port > 65535 {
		return "", "", 0, nil, errors.New("invalid discovery port")
	}
	if lines[2] != "_raop._tcp" || len([]byte(lines[1])) > 63 {
		return "", "", 0, nil, errors.New("invalid RAOP service")
	}
	for _, txt := range lines[3:] {
		if len(txt) > 255 {
			return "", "", 0, nil, errors.New("invalid TXT record")
		}
	}
	return lines[1], lines[2], port, lines[3:], nil
}
func main() {
	if e := run(); e != nil {
		log.Fatal(e)
	}
}
func run() error {
	b, e := os.ReadFile(os.Getenv("ALICE_CONFIG_PATH"))
	if e != nil {
		return e
	}
	var c configuration
	if e = json.Unmarshal(b, &c); e != nil {
		return e
	}
	text, e := configText(c)
	if e != nil {
		return e
	}
	root := os.Getenv("ALICE_PLUGIN_DATA")
	if root == "" {
		return errors.New("ALICE_PLUGIN_DATA missing")
	}
	// Runtime files stay private to this plugin and are recreated on each start.
	binary := filepath.Join(root, "shairport-sync")
	cfg := filepath.Join(root, "shairport-sync.conf")
	discovery := filepath.Join(root, "discovery.txt")
	if e = os.WriteFile(binary, receiver, 0700); e != nil {
		return e
	}
	if e = os.Chmod(binary, 0700); e != nil {
		return e
	}
	if e = os.WriteFile(cfg, []byte(text), 0600); e != nil {
		return e
	}
	alsaConfig := filepath.Join(root, "alsa.conf")
	device := strings.TrimPrefix(c.Device, "plughw:0,")
	alsa := fmt.Sprintf("pcm.alice_out { type plug slave.pcm { type hw card 0 device %s } }\n", device)
	if e = os.WriteFile(alsaConfig, []byte(alsa), 0600); e != nil {
		return e
	}
	_ = os.Remove(discovery)
	cmd := exec.Command(binary, "-c", cfg, "-v")
	cmd.Stderr = os.Stderr
	cmd.Env = append(os.Environ(), "ALICE_MDNS_PATH="+discovery, "ALSA_CONFIG_PATH="+alsaConfig)
	if c.AudioMode == "direct" {
		cmd.Stdout = os.Stdout
	} else {
		shared, err := sharedaudio.New(root)
		if err != nil {
			return err
		}
		defer shared.Close()
		cmd.Stdout = shared
		log.Print("AirPlay shared audio: stock ALSA mixer; Alice services are left running")
	}
	if e = cmd.Start(); e != nil {
		return e
	}
	finished := make(chan error, 1)
	go func() { finished <- cmd.Wait() }()
	defer func() {
		_ = cmd.Process.Signal(syscall.SIGTERM)
		select {
		case <-finished:
		case <-time.After(time.Second):
			_ = cmd.Process.Kill()
		}
	}()
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGTERM, os.Interrupt)
	defer signal.Stop(signals)
	tick := time.NewTicker(250 * time.Millisecond)
	defer tick.Stop()
	var server *mdns.Server
	var last []byte
	defer func() {
		if server != nil {
			server.Shutdown()
		}
	}()
	for {
		select {
		case <-signals:
			return nil
		case e := <-finished:
			if e == nil {
				return errors.New("receiver exited")
			}
			return e
		case <-tick.C:
			current, e := os.ReadFile(discovery)
			if e != nil || bytes.Equal(current, last) {
				continue
			}
			name, service, port, txt, e := descriptor(current)
			if e != nil {
				return e
			}
			ips := []net.IP{}
			interfaces, _ := net.Interfaces()
			// Prefer the speaker's Wi-Fi over USB/RNDIS addresses that an iPhone
			// cannot reach. Advertise one interface and its matching addresses.
			sort.SliceStable(interfaces, func(i, j int) bool {
				return strings.HasPrefix(interfaces[i].Name, "wl") && !strings.HasPrefix(interfaces[j].Name, "wl")
			})
			var selectedInterface *net.Interface
			for _, iface := range interfaces {
				if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 || iface.Flags&net.FlagMulticast == 0 {
					continue
				}
				addresses, _ := iface.Addrs()
				for _, a := range addresses {
					ip, _, err := net.ParseCIDR(a.String())
					if err == nil && ip.To4() != nil && !ip.IsLoopback() {
						ips = append(ips, ip.To4())
					}
				}
				if len(ips) > 0 {
					copy := iface
					selectedInterface = &copy
					break
				}
			}
			if len(ips) == 0 {
				return errors.New("no LAN address available for AirPlay")
			}
			hostname := "alice-" + strings.ToLower(strings.Split(name, "@")[0]) + ".local."
			zone, e := mdns.NewMDNSService(name, service, "local.", hostname, port, ips, txt)
			if e != nil {
				return e
			}
			if server != nil {
				server.Shutdown()
			}
			server, e = mdns.NewServer(&mdns.Config{Zone: zone, Iface: selectedInterface})
			if e != nil {
				return e
			}
			last = append(last[:0], current...)
			log.Printf("AirPlay audio ready: %s, TCP %d, output %s", c.Name, port, c.Device)
		}
	}
}
