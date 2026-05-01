#!/usr/bin/env python3
"""Fake 3DS client: receives UDP datagrams, reassembles H.264 NALs,
decodes with ffmpeg, and displays frames with OpenCV.

Usage:
    uv run fake_3ds_client.py <listen_port>

Example:
    uv run fake_3ds_client.py 9001
"""

import socket
import struct
import sys
import subprocess
import threading
import queue
import numpy as np
import cv2

MAGIC = 0x33445331
HEADER_FMT = ">IIHH"   # magic:u32, frame_id:u32, chunk_id:u16, total_chunks:u16
HEADER_SIZE = 12

# ---------------------------------------------------------------------------
# Shared state
# ---------------------------------------------------------------------------
reassembly = {}          # frame_id -> list[bytes | None]
nal_queue = queue.Queue(maxsize=128)
running = threading.Event()
running.set()

# ---------------------------------------------------------------------------
# UDP receiver thread
# ---------------------------------------------------------------------------
def udp_receiver(sock: socket.socket) -> None:
    """Receive chunks, reassemble NALs, push complete ones to nal_queue."""
    while running.is_set():
        try:
            data, addr = sock.recvfrom(1500)
        except socket.timeout:
            continue

        if len(data) < HEADER_SIZE:
            continue

        magic, frame_id, chunk_id, total_chunks = struct.unpack(
            HEADER_FMT, data[:HEADER_SIZE]
        )
        if magic != MAGIC:
            continue

        payload = data[HEADER_SIZE:]

        # initialise reassembly buffer for this NAL
        if frame_id not in reassembly:
            reassembly[frame_id] = [None] * total_chunks

        if chunk_id < len(reassembly[frame_id]):
            reassembly[frame_id][chunk_id] = payload

        # emit immediately once every chunk has arrived
        if all(c is not None for c in reassembly[frame_id]):
            nal_bytes = b"".join(reassembly[frame_id])
            annexb = b"\x00\x00\x00\x01" + nal_bytes
            try:
                nal_queue.put(annexb, timeout=0.5)
            except queue.Full:
                print("[warn] NAL queue full, dropping frame", frame_id)
            del reassembly[frame_id]


# ---------------------------------------------------------------------------
# Decoder writer thread
# ---------------------------------------------------------------------------
def decoder_writer(stdin) -> None:
    """Pull complete NALs from queue and write to ffmpeg stdin."""
    while running.is_set() or not nal_queue.empty():
        try:
            nal = nal_queue.get(timeout=0.1)
        except queue.Empty:
            continue
        try:
            stdin.write(nal)
        except BrokenPipeError:
            break


# ---------------------------------------------------------------------------
# Stderr drain (prevents ffmpeg from blocking on a full stderr pipe)
# ---------------------------------------------------------------------------
def drain_stderr(pipe) -> None:
    while running.is_set():
        try:
            pipe.read(1024)
        except Exception:
            break


# ---------------------------------------------------------------------------
# Main
# ---------------------------------------------------------------------------
def main() -> int:
    if len(sys.argv) < 2:
        print(f"Usage: {sys.argv[0]} <listen_port>")
        print(f"Example: {sys.argv[0]} 9001")
        return 1

    port = int(sys.argv[1])

    # ------------------------------------------------------------------
    # UDP socket
    # ------------------------------------------------------------------
    sock = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
    sock.bind(("0.0.0.0", port))
    sock.settimeout(0.5)
    print(f"Listening on UDP :{port} ...")

    # ------------------------------------------------------------------
    # Spawn ffmpeg decoder
    #   Input : H.264 Annex-B byte stream from pipe
    #   Output: raw BGR24 frames to pipe
    # ------------------------------------------------------------------
    ffmpeg_cmd = [
        "ffmpeg",
        "-hide_banner", "-loglevel", "warning",
        "-fflags", "nobuffer",
        "-flags", "low_delay",
        "-f", "h264", "-i", "pipe:0",
        "-vf", "scale=800:-1:flags=neighbor",
        "-f", "rawvideo", "-pix_fmt", "bgr24", "-s", "400x240",
        "pipe:1",
    ]

    ffmpeg = subprocess.Popen(
        ffmpeg_cmd,
        stdin=subprocess.PIPE,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
    )

    # drain stderr so ffmpeg never blocks on it
    threading.Thread(target=drain_stderr, args=(ffmpeg.stderr,), daemon=True).start()

    # ------------------------------------------------------------------
    # Start network & decoder threads
    # ------------------------------------------------------------------
    t_recv = threading.Thread(target=udp_receiver, args=(sock,))
    t_recv.start()

    t_write = threading.Thread(target=decoder_writer, args=(ffmpeg.stdin,))
    t_write.start()

    # ------------------------------------------------------------------
    # Main thread: read decoded frames from ffmpeg and display
    # ------------------------------------------------------------------
    W, H = 400, 240
    frame_bytes = W * H * 3          # BGR24 = 3 bytes per pixel
    frame_count = 0

    print("Waiting for stream ...  Press 'q' or Ctrl+C to quit.")

    try:
        while True:
            raw = ffmpeg.stdout.read(frame_bytes)
            if len(raw) != frame_bytes:
                break

            frame = np.frombuffer(raw, dtype=np.uint8).reshape((H, W, 3))
            cv2.imshow("3DS Stream", frame)
            frame_count += 1

            if cv2.waitKey(1) & 0xFF == ord("q"):
                break
    except KeyboardInterrupt:
        pass
    finally:
        running.clear()
        t_recv.join(timeout=2)
        t_write.join(timeout=2)
        ffmpeg.stdin.close()
        ffmpeg.terminate()
        ffmpeg.wait(timeout=3)
        cv2.destroyAllWindows()
        print(f"\nDisplayed {frame_count} frames. Done.")

    return 0


if __name__ == "__main__":
    sys.exit(main())
