package dsnet

import (
	"fmt"
	"os"
	"text/template"
	"time"
)

const wgQuickPeerConf = `[Interface]
{{ if gt (.DsnetConfig.Network.IPNet.IP | len) 0 -}}
Address={{ .Peer.IP }}/{{ .CidrSize }}
{{ end -}}
{{ if gt (.DsnetConfig.Network6.IPNet.IP | len) 0 -}}
Address={{ .Peer.IP6 }}/{{ .CidrSize6 }}
{{ end -}}
PrivateKey={{ .Peer.PrivateKey.Key }}
{{- if .DsnetConfig.DNS }}
DNS={{ .DsnetConfig.DNS }}
{{ end }}

[Peer]
PublicKey={{ .DsnetConfig.PrivateKey.PublicKey.Key }}
PresharedKey={{ .Peer.PresharedKey.Key }}
Endpoint={{ .Endpoint }}:{{ .DsnetConfig.ListenPort }}
PersistentKeepalive={{ .Keepalive }}
{{ if gt (.DsnetConfig.Network.IPNet.IP | len) 0 -}}
AllowedIPs={{ .DsnetConfig.Network }}
{{ end -}}
{{ if gt (.DsnetConfig.Network6.IPNet.IP | len) 0 -}}
AllowedIPs={{ .DsnetConfig.Network6 }}
{{ end -}}
{{ range .DsnetConfig.Networks -}}
AllowedIPs={{ . }}
{{ end -}}
`

// TODO use random wg0-wg999 to hopefully avoid conflict by default?
const vyattaPeerConf = `configure
{{ if gt (.DsnetConfig.Network.IPNet.IP | len) 0 -}}
set interfaces wireguard {{ .Wgif }} address {{ .Peer.IP }}/{{ .CidrSize }}
{{ end -}}
{{ if gt (.DsnetConfig.Network6.IPNet.IP | len) 0 -}}
set interfaces wireguard {{ .Wgif }} address {{ .Peer.IP6 }}/{{ .CidrSize6 }}
{{ end -}}
set interfaces wireguard {{ .Wgif }} route-allowed-ips true
set interfaces wireguard {{ .Wgif }} private-key {{ .Peer.PrivateKey.Key }}
set interfaces wireguard {{ .Wgif }} description {{ .DsnetConfig.InterfaceName }}
{{- if .DsnetConfig.DNS }}
#set service dns forwarding name-server {{ .DsnetConfig.DNS }}
{{ end }}

set interfaces wireguard {{ .Wgif }} peer {{ .DsnetConfig.PrivateKey.PublicKey.Key }} endpoint {{ .Endpoint }}:{{ .DsnetConfig.ListenPort }}
set interfaces wireguard {{ .Wgif }} peer {{ .DsnetConfig.PrivateKey.PublicKey.Key }} persistent-keepalive {{ .Keepalive }}
set interfaces wireguard {{ .Wgif }} peer {{ .DsnetConfig.PrivateKey.PublicKey.Key }} preshared-key {{ .Peer.PresharedKey.Key }}
{{ if gt (.DsnetConfig.Network.IPNet.IP | len) 0 -}}
set interfaces wireguard {{ .Wgif }} peer {{ .DsnetConfig.PrivateKey.PublicKey.Key }} allowed-ips {{ .DsnetConfig.Network }}
{{ end -}}
{{ if gt (.DsnetConfig.Network6.IPNet.IP | len) 0 -}}
set interfaces wireguard {{ .Wgif }} peer {{ .DsnetConfig.PrivateKey.PublicKey.Key }} allowed-ips {{ .DsnetConfig.Network6 }}
{{ end -}}
{{ range .DsnetConfig.Networks -}}
set interfaces wireguard {{ .Wgif }} peer {{ .DsnetConfig.PrivateKey.PublicKey.Key }} allowed-ips {{ . }}
{{ end -}}
commit; save
`

func Add() {
	if len(os.Args) != 3 {
		// TODO non-red
		ExitFail("Hostname argument required: dsnet add <hostname>")
	}

	// TODO maybe accept flags to avoid prompt and allow programmatic use?
	// TODO accept existing pubkey
	conf := MustLoadDsnetConfig()

	hostname := os.Args[2]
	owner := MustPromptString("owner", true)
	description := MustPromptString("Description", true)
	//publicKey := MustPromptString("PublicKey (optional)", false)
	ConfirmOrAbort("\nDo you want to add the above configuration?")

	// newline (not on stdout) to separate config
	fmt.Fprintln(os.Stderr)

	privateKey := GenerateJSONPrivateKey()
	publicKey := privateKey.PublicKey()

	peer := PeerConfig{
		Owner:        owner,
		Hostname:     hostname,
		Description:  description,
		Added:        time.Now(),
		PublicKey:    publicKey,
		PrivateKey:   privateKey, // omitted from server config JSON!
		PresharedKey: GenerateJSONKey(),
		Networks:     []JSONIPNet{},
	}

	if len(conf.Network.IPNet.Mask) > 0 {
		peer.IP = conf.MustAllocateIP()
	}

	if len(conf.Network6.IPNet.Mask) > 0 {
		peer.IP6 = conf.MustAllocateIP6()
	}

	if len(conf.IP) == 0 && len(conf.IP6) == 0 {
		ExitFail("No IPv4 or IPv6 network defined in config")
	}

	conf.MustAddPeer(peer)
	PrintPeerCfg(peer, conf)
	conf.MustSave()
	ConfigureDevice(conf)
}

func PrintPeerCfg(peer PeerConfig, conf *DsnetConfig) {
	var peerConf string

	switch os.Getenv("DSNET_OUTPUT") {
	// https://manpages.debian.org/unstable/wireguard-tools/wg-quick.8.en.html
	case "", "wg-quick":
		peerConf = wgQuickPeerConf
	// https://github.com/WireGuard/wireguard-vyatta-ubnt/
	case "vyatta":
		peerConf = vyattaPeerConf
	default:
		ExitFail("Unrecognised DSNET_OUTPUT type")
	}

	cidrSize, _ := conf.Network.IPNet.Mask.Size()
	cidrSize6, _ := conf.Network6.IPNet.Mask.Size()

	// derive deterministic interface name
	wgifSeed := 0
	for _, b := range conf.IP {
		wgifSeed += int(b)
	}

	for _, b := range conf.IP6 {
		wgifSeed += int(b)
	}

	// See DsnetConfig type for explanation
	var endpoint string

	if conf.ExternalHostname != "" {
		endpoint = conf.ExternalHostname
	} else if len(conf.ExternalIP) > 0 {
		endpoint = conf.ExternalIP.String()
	} else if len(conf.ExternalIP6) > 0 {
		endpoint = conf.ExternalIP6.String()
	} else {
		ExitFail("Config does not contain ExternalIP, ExternalIP6 or ExternalHostname")
	}

	t := template.Must(template.New("peerConf").Parse(peerConf))
	err := t.Execute(os.Stdout, map[string]interface{}{
		"Peer":        peer,
		"DsnetConfig": conf,
		"Keepalive":   time.Duration(KEEPALIVE).Seconds(),
		"CidrSize":    cidrSize,
		"CidrSize6":   cidrSize6,
		// vyatta requires an interface in range/format wg0-wg999
		// deterministically choosing one in this range will probably allow use
		// of the config without a colliding interface name
		"Wgif":     fmt.Sprintf("wg%d", wgifSeed%999),
		"Endpoint": endpoint,
	})
	check(err)
}
