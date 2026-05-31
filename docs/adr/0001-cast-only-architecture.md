# Cast-only architecture — remove 3DS transport and file/URL ingest

The server was originally built to stream video to a Nintendo 3DS homebrew client via UDP/LZ4-compressed YUV420p frames, with secondary support for web viewers via MJPEG-over-WebSocket. The 3DS client has been deleted and the project is pivoting to a general-purpose screen-casting tool.

We decided to:
- Delete the entire 3DS transport layer: UDP sender, LZ4 compression, binary packet protocol with magic `0x33445331`
- Delete the file/URL ingest pipeline (ffmpeg reading from disk/RTSP/YouTube → raw YUV or MJPEG)
- Make browser screen-cast via `getDisplayMedia()` + WebSocket the sole input path
- Make MJPEG-over-WebSocket the sole output — every viewer is a web browser
- Remove the LZ4 dependency from go.mod

The server is now a pure screen-cast relay: browser captures screen → WebM over `/ws/cast` → ffmpeg transcodes to MJPEG → Hub fans out to `/ws/web` viewers.

## Considered options

**Option A: Keep ingest alongside cast.** The server could accept both file/URL sources and browser casts. Rejected because it adds code paths that see zero use in practice and complicate the admin UI.

**Option B: Keep the UDP transport as a generic low-level streaming option.** Rename packet headers to be non-3DS-specific. Rejected because no current or planned consumers exist for a custom UDP/LZ4 video protocol when WebSocket+MJPEG works in every browser.

## Consequences

- The server is simpler: ~50% fewer Go files, one dependency dropped
- Zero client-side software needed — both sharer and viewer use existing browsers
- Lost: ability to stream a video file to a viewer without a sharer's browser running
