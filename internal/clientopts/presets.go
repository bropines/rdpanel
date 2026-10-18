package clientopts

// Preset represents a pre-configured ICE / STUN server list.
type Preset struct {
	ID      string `json:"id"`
	Servers string `json:"servers"`
}

// Built-in presets for WebRTC ICE servers.
var Presets = []Preset{
	{
		ID: "global",
		Servers: "stun:stun.cloudflare.com:3478\n" +
			"stun:stun.l.google.com:19302\n" +
			"stun:stun1.l.google.com:19302",
	},
	{
		ID: "ru",
		Servers: "stun:stun.cloudflare.com:3478\n" +
			"stun:stun.qq.com:3478\n" +
			"stun:stun.syncthing.net:3478",
	},
	{
		ID:      "cloudflare",
		Servers: "stun:stun.cloudflare.com:3478",
	},
	{
		ID: "google",
		Servers: "stun:stun.l.google.com:19302\n" +
			"stun:stun1.l.google.com:19302",
	},
	{
		ID:      "default",
		Servers: "",
	},
}
