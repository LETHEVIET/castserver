# CastServer

A LAN screen-casting tool. A browser captures its screen via `getDisplayMedia()` and streams it to the server over WebSocket. The server transcodes the WebM stream to MJPEG and fans out each JPEG frame to viewer browsers connected via WebSocket.

## Language

**Cast Session**:
An active pipeline: browser → WebM over `/ws/cast` → ffmpeg transcoder → MJPEG → Hub → viewer subscribers.
Only one cast session runs at a time.
_Avoid_: Stream, ingest, encode session

**Hub**:
A fan-out broadcaster that delivers a copy of the latest JPEG frame to every connected viewer. Slow subscribers get their oldest queued frame dropped to avoid back-pressure.
_Avoid_: Router, dispatcher, multiplexer

**Viewer**:
A browser connected to `/ws/web` that receives and displays MJPEG frames. Viewers are anonymous; anyone with the URL can watch.
_Avoid_: Client, subscriber, watcher

**Sharer**:
The browser on the admin page that initiates a cast session via `getDisplayMedia()` and pushes the captured WebM stream to `/ws/cast`.
_Avoid_: Sender, publisher, caster

**Preset**:
A named set of encoding parameters (resolution, FPS, bitrate, JPEG quality, scaler). The server owns preset definitions; the sharer selects one by name. Custom mode allows individual overrides.
_Avoid_: Profile, template, mode

**Admin Page** (`/`):
The web UI where the sharer selects a preset, starts/stops a cast session, sees a live MJPEG preview, and gets a viewer URL with QR code.
_Avoid_: Control panel, dashboard

**Viewer Page** (`/web`):
The lightweight page that connects to the Hub and displays the MJPEG stream. Designed for phones, tablets, and other browsers.
_Avoid_: Client page, display page

## Example dialogue

**Dev**: "If the sharer starts a cast with the Balanced preset, how do viewers see it?"
**Expert**: "The cast session starts running even with zero viewers. The Hub publishes frames immediately. When a viewer loads `/web`, it subscribes to the Hub and receives frames from the next publish cycle. There's no buffered last-frame — the viewer just gets the next live frame."

**Dev**: "Can two sharers cast simultaneously?"
**Expert**: "No. Only one cast session is allowed. AcquireSession rejects the second attempt until the first session is released."

**Dev**: "What if a viewer connects but no cast session is running?"
**Expert**: "The viewer gets `{\"status\":\"idle\"}` and waits. The page shows 'no active session' until a sharer starts casting."

**Dev**: "How does the sharer know what quality viewers are getting?"
**Expert**: "The admin page shows a live MJPEG preview — the actual frames after transcoding. So the sharer sees exactly what viewers see, including any quality degradation from the preset."
