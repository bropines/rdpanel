import { decodeConfigString, idUrlFromInput, isIpHost, isValidServer, relayUrlOf, serverFromConfig } from "./servers";

function encode(obj: object): string {
  const b64 = btoa(JSON.stringify(obj)).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");
  return b64.split("").reverse().join("");
}

describe("web client servers", () => {
  it("decodes RustDesk config strings", () => {
    const s = encode({ host: "rd.example.com", relay: "rd.example.com:21117", key: "MEbIUN8NZFq0Vu80BjNNIGDbQKdXxcg8QTbo69qrH+A=", api: "https://rd.example.com" });
    expect(decodeConfigString(s)).toEqual({
      host: "rd.example.com",
      relay: "rd.example.com:21117",
      key: "MEbIUN8NZFq0Vu80BjNNIGDbQKdXxcg8QTbo69qrH+A=",
      api: "https://rd.example.com",
    });
    expect(decodeConfigString("not a config")).toBeNull();
  });

  it("derives WebSocket URLs from what users type", () => {
    expect(idUrlFromInput("rd.example.com")).toBe("wss://rd.example.com/ws/id");
    expect(idUrlFromInput("rd.example.com:21116")).toBe("wss://rd.example.com/ws/id");
    expect(idUrlFromInput("https://rd.example.com/x")).toBe("wss://rd.example.com/ws/id");
    expect(idUrlFromInput("wss://rd.example.com:8443/ws/id")).toBe("wss://rd.example.com:8443/ws/id");
    expect(relayUrlOf({ idUrl: "wss://rd.example.com:8443/ws/id", relayUrl: "" })).toBe("wss://rd.example.com:8443/ws/relay");
  });

  it("validates servers", () => {
    const key = "MEbIUN8NZFq0Vu80BjNNIGDbQKdXxcg8QTbo69qrH+A=";
    expect(isValidServer({ idUrl: "wss://a.example/ws/id", relayUrl: "", key })).toBe(true);
    expect(isValidServer({ idUrl: "ws://a.example/ws/id", relayUrl: "", key })).toBe(false);
    expect(isValidServer({ idUrl: "wss://a.example/ws/id", relayUrl: "", key: "short" })).toBe(false);
  });

  it("takes the HTTPS API host from config strings: host and relay are TCP endpoints", () => {
    // RD Panel's "by IP" config string
    expect(serverFromConfig({ host: "203.0.113.5", relay: "desk.example.com:21117", key: "k", api: "https://panel.example.com" })).toEqual({
      name: "panel.example.com",
      host: "panel.example.com",
      relay: "",
    });
    expect(serverFromConfig({ host: "id.example.com:21116", relay: "relay.example.com:21117", key: "k", api: "" })).toEqual({
      name: "id.example.com",
      host: "id.example.com",
      relay: "relay.example.com",
    });
    expect(serverFromConfig({ host: "id.example.com", relay: "203.0.113.5:21117", key: "k", api: "https://203.0.113.5" }).relay).toBe("");
  });

  it("recognises IP addresses", () => {
    expect(isIpHost("203.0.113.5")).toBe(true);
    expect(isIpHost("[2001:db8::1]:8443")).toBe(true);
    expect(isIpHost("rd.example.com")).toBe(false);
  });
});
