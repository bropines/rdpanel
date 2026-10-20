# RustDesk HttpProxyRequest Protocol & RD Panel Integration

## 1. Overview
RustDesk clients (starting from v1.4.7+) support an encrypted TCP proxy fallback (`HttpProxyRequest`) over port 21116 to `hbbs`. When standard HTTP/HTTPS API calls fail (connection refused, network unreachable, timeouts, or HTTP 5xx errors), the client falls back to encapsulating raw API requests as Protobuf messages over its established rendezvous TCP connection.

## 2. Protobuf Wire Specification (`rendezvous.proto`)
```protobuf
message HeaderEntry {
  string name = 1;
  string value = 2;
}

message HttpProxyRequest {
  string method = 1;
  string path = 2;
  repeated HeaderEntry headers = 3;
  bytes body = 4;
}

message HttpProxyResponse {
  int32 status = 1;
  repeated HeaderEntry headers = 2;
  bytes body = 3;
  string error = 4;
}

message RendezvousMessage {
  oneof union {
    ...
    HttpProxyRequest http_proxy_request = 27;
    HttpProxyResponse http_proxy_response = 28;
    ...
  }
}
```

## 3. Implementation in `bropines/rustdesk-server`
In `src/rendezvous_server.rs`, `hbbs` handles incoming `HttpProxyRequest` messages:
- Targets local RD Panel on `http://127.0.0.1:21114` (configurable via `--http-proxy-target` or `HTTP_PROXY_TARGET` env var).
- SSRF and Path Traversal protection: validates path format, disallows `://` and `..`.
- Preserves client IP by injecting `X-Real-IP` and `X-Forwarded-For` from the incoming rendezvous socket address.
- Strips hop-by-hop headers (`Connection`, `Keep-Alive`, `Transfer-Encoding`, etc.).
- Converts upstream failures into appropriate HTTP error responses (502 Bad Gateway / 504 Gateway Timeout).
