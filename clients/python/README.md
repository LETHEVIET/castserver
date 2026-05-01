# Fake 3DS Client

Receives H.264 NAL units over UDP from the stream server, decodes them with ffmpeg, and displays the video with OpenCV.

## Quick start

```bash
cd clients/python
uv sync
uv run fake_3ds_client.py 9001
```

Then start the server stream:

```bash
cd server
curl -X POST http://localhost:1108/play \
  -d '{"source":"file:///path/to/video.mp4","client_addr":"127.0.0.1:9001"}'
```

A window titled **"3DS Stream"** will appear showing the 400×240 video upscaled to 800×480 with nearest-neighbor interpolation (preserves the retro pixel look).

Press `q` or `Ctrl+C` to quit.

## Headless / remote servers

If you are running on a machine without a display (e.g. over SSH), use `xvfb-run`:

```bash
xvfb-run uv run fake_3ds_client.py 9001
```

Or capture the raw video to a file instead of displaying:

```bash
# Replace the ffmpeg command in fake_3ds_client.py with:
# ffmpeg ... pipe:1 > output.yuv
```

## Architecture

1. **UDP receiver thread** — receives datagrams, reassembles chunks by `frame_id`, pushes complete NALs to a queue
2. **Decoder writer thread** — pulls NALs from queue, prepends Annex-B start code (`00 00 00 01`), writes to ffmpeg stdin
3. **Main thread** — reads raw BGR24 frames from ffmpeg stdout and displays with `cv2.imshow`

The queue decouples the network thread from the decoder so brief network jitter doesn't stall the display.
