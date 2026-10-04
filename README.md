# bhttp

HTTP over a binary framing protocol: a file server (`bserve`) and a client (`bcurl`) that share nothing but the spec below.

```
./bserve ./www 9000
./bcurl -v localhost:9000/index.html
```

## Spec: bhttp/1

This section is enough to write a bhttp/1 client or server without reading our code.


### 1. Model

- One TCP connection. The client sends a request, reads the complete response, then may send the next. At most one request is in flight; responses come back in request order. A client never opens a second connection.
- The server keeps the connection open after every response except a 400 (§7). Either side may close it; the server closes it after 30 s idle.
- All integers are unsigned, big-endian.

### 2. Frame

Every byte on the wire belongs to a frame: an 8-byte header, then `Length` bytes of payload.

```
 byte   0          1          2          3          4    5    6    7
     +----------+----------+----------+----------+---------------------+
     | Version  |   Type   |  Flags   | Reserved |   Length (uint32)   |
     +----------+----------+----------+----------+---------------------+
     |  Payload: Length bytes ...
```

| Field    | Bits | Rule |
|----------|------|------|
| Version  | 8    | `0x01`. Any other value is malformed. Version stays at byte 0 in every future version. |
| Type     | 8    | §3. An unknown type is skipped, never an error. |
| Flags    | 8    | `0x01` = END. Undefined bits: send 0, ignore on receive. |
| Reserved | 8    | Send `0x00`, ignore on receive. |
| Length   | 32   | Payload size in bytes. MUST be ≤ 16384; larger is malformed. |

**Why 8/8/8/8/32 and not HTTP/2's 24/8/8/31.** HTTP/2 spends 31 bits on a stream id because it multiplexes many requests over one connection, and gets back a byte by squeezing Length to 24 bits. Our constraints are different:

- **No stream id.** One request in flight, responses in order: a stream id would always hold the same number. Dropping it saves 4 bytes per frame and a whole class of errors (unknown, closed or reused streams) we would otherwise have to specify. Multiplexing is a version 2 feature.
- **A version byte instead.** HTTP/2 agrees on its version before the first frame (TLS ALPN, or a 24-byte preface). We have no handshake, so every frame carries the version and a receiver can reject an incompatible peer at byte 0, with no connection state.
- **Length is 32 bits, capped at 16384.** The cap, not the width, bounds what a receiver must buffer; 16 KiB is HTTP/2's own default `SETTINGS_MAX_FRAME_SIZE`. Without a stream id, 1+1+1+1+4 = 8 bytes: Length sits on a 4-byte boundary, and one header is exactly half a hexdump row. Version 2 can raise the cap without changing the layout.
- **Type and Flags are 8 bits each**, as in HTTP/2. We use 2 of 256 types and 1 of 8 flags; the rest is room for version 2.
- **Reserved** is the byte that rounds the header to 8. It is zero today, like HTTP/2's R bit, but a whole byte.

### 3. Frame types, and the skip rule

| Type   | Name    | Payload |
|--------|---------|---------|
| `0x00` | DATA    | Body bytes, 0–16384 of them. |
| `0x01` | HEADERS | One header block (§5). It must fit in one frame: there is no CONTINUATION. |

**A receiver that reads a frame whose Type it does not know MUST read and discard its `Length` payload bytes, ignore its Flags, and continue as if the frame had never been sent.** Length means the same thing for every type, so a receiver can always find the next frame boundary, even for frame types invented after it was written. This rule is what leaves room for version 2 (§9).

### 4. Messages

```
message = HEADERS DATA*        the last frame of the message has END set
```

A HEADERS frame with END means the message has no body. Unknown frames may appear anywhere and are skipped. The following are malformed: a DATA frame when no message is open, and a HEADERS frame while one is open (no trailers).

### 5. Header block

A HEADERS payload is a list of fields, back to back, up to the end of the payload:

```
+-----------+---------------------------------+----------------+-----------+
| Index (8) | NameLen (8) | Name  (if Index=0) | ValueLen (16)  |   Value   |
+-----------+---------------------------------+----------------+-----------+
```

Index 1–10 picks a name from the static table. Index 0 means a literal name follows. Index 11–255 is malformed.

| 1 `:method` | 2 `:path` | 3 `:status` | 4 `host` | 5 `user-agent` |
|---|---|---|---|---|
| **6 `accept`** | **7 `content-type`** | **8 `content-length`** | **9 `server`** | **10 `date`** |

Names are lowercase ASCII and compared byte for byte. If a name repeats, the first occurrence wins. Malformed: a field that runs past the end of the payload, or a literal name with NameLen 0.

**Why.** This is HPACK's first two mechanisms and nothing more. The static table holds exactly the ten names bcurl and bserve send, so every name we send costs one byte instead of about ten. Literal names keep the protocol open: a stranger's client can send any header and we still parse it. We leave out the dynamic table, HPACK's third mechanism, because it is compression state shared across messages: one desync corrupts every later header, and its memory has to be bounded. We leave out Huffman coding, the fourth, because it shaves bytes off values that are already short. NameLen is 8 bits because names are short tokens. ValueLen is 16 bits because paths and dates can exceed 255 bytes, and 65535 is already more than a frame can hold. We use fixed widths rather than HPACK's prefix varints so that a person can annotate the bytes by eye.

### 6. Requests and responses

- A request's HEADERS MUST contain `:method` and `:path` (starting with `/`) and SHOULD contain `host`. bcurl also sends `user-agent` and `accept`.
- A response's HEADERS MUST contain `:status`, as three ASCII digits. bserve also sends `content-type`, `content-length` (body size in decimal), `server` and `date` (IMF-fixdate, e.g. `Sun, 04 Oct 2026 12:00:00 GMT`).
- A body is sent as DATA frames of at most 16384 bytes each.

### 7. Server: `bserve ROOT PORT`

- `:path` names a file under ROOT: the leading `/` is dropped, and a path ending in `/` gets `index.html` appended. A path that resolves outside ROOT, through `..` or a symlink, is never served.
- **200**: the file exists. **404**: no such regular file under ROOT. **405**: any method other than GET. Request bodies are read and discarded.
- **400**: a malformed frame or header block, or a request without `:method` or a valid `:path`. The server sends the 400 and then **closes the connection**: after a malformed frame it cannot trust where the next frame begins.
- Error responses carry a short `text/plain` body.

### 8. Client: `bcurl [-v] HOST:PORT/PATH ...`

- All URLs must share HOST:PORT. They are fetched in order, over one connection.
- Body bytes go to stdout and nothing else does. With `-v`, every frame sent (`>`) and received (`<`), skipped ones included, is hexdumped to stderr.
- Exit 0 if every status is below 400, 1 if any status is 4xx or 5xx, 2 on a usage, connection or protocol error.

### 9. Leaving room for version 2

A new frame type is skipped by v1 peers. A new flag is ignored by them. A new header name travels as a literal. Anything that changes the 8-byte header or the static table needs Version 2: a v1 server answers a Version-2 frame with a v1 400 and closes, so a v2 client finds out on its first frame and can fall back.

### 10. Example

`bcurl localhost:9000/index.html` sends exactly these 61 bytes (an 8-byte header and a 53-byte payload):

```
01 01 01 00 00 00 00 35                             v1, HEADERS, END, reserved, Length 53
01 00 03 47 45 54                                   [1] :method        "GET"
02 00 0b 2f 69 6e 64 65 78 2e 68 74 6d 6c           [2] :path          "/index.html"
04 00 0e 6c 6f 63 61 6c 68 6f 73 74 3a 39 30 30 30  [4] host           "localhost:9000"
05 00 07 62 63 75 72 6c 2f 31                       [5] user-agent     "bcurl/1"
06 00 03 2a 2f 2a                                   [6] accept         "*/*"
```
