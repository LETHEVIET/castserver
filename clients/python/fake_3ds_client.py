#!/usr/bin/env python3
"""Fake 3DS client: receives UDP datagrams, reassembles LZ4-compressed
YUV420p frames, decompresses, and displays with OpenCV.

Usage:
    uv run fake_3ds_client.py <listen_port>

Example:
    uv run fake_3ds_client.py 9001
"""

import socket
import struct
import sys
import threading
import lz4.block
import numpy as np
import cv2

MAGIC = 0x33445331
# Header: magic:u32, frame_id:u32, chunk_id:u16, total_chunks:u16, width:u16, height:u16
HEADER_FMT = ">IIHHHH"
HEADER_SIZE = 16

# ---------------------------------------------------------------------------
# Shared state
# ---------------------------------------------------------------------------
reassembly = {}          # frame_id -> {'chunks': list[bytes|None], 'w': int, 'h': int}
frame_queue = []         # list of (width, height, yuv420p_data)
running = threading.Event()
running.set()
lock = threading.Lock()

# ---------------------------------------------------------------------------
# UDP receiver thread
# ---------------------------------------------------------------------------
def udp_receiver(sock: socket.socket) -> None:
    """Receive chunks, reassemble LZ4-compressed YUV420p frames."""
    while running.is_set():
        try:
            data, addr = sock.recvfrom(1500)
        except socket.timeout:
            continue

        if len(data) < HEADER_SIZE:
            continue

        magic, frame_id, chunk_id, total_chunks, width, height = struct.unpack(
            HEADER_FMT, data[:HEADER_SIZE]
        )
        if magic != MAGIC:
            continue
        if total_chunks == 0 or chunk_id >= total_chunks:
            continue

        payload = data[HEADER_SIZE:]

        if frame_id not in reassembly:
            # Drop old frame
            reassembly.clear()
            reassembly[frame_id] = {
                'chunks': [None] * total_chunks,
                'w': width,
                'h': height,
            }

        buf = reassembly.get(frame_id)
        if buf is None:
            continue
        if chunk_id < len(buf['chunks']):
            buf['chunks'][chunk_id] = payload

        if all(c is not None for c in buf['chunks']):
            compressed = b"".join(buf['chunks'])
            expected = width * height * 3 // 2
            try:
                yuv420p = lz4.block.decompress(compressed, uncompressed_size=expected)
            except Exception as e:
                print(f"[warn] lz4 decompress failed for frame {frame_id}: {e}")
                del reassembly[frame_id]
                continue
            with lock:
                frame_queue.append((width, height, yuv420p))
            del reassembly[frame_id]

# ---------------------------------------------------------------------------
# Main
# ---------------------------------------------------------------------------
def main() -> int:
    if len(sys.argv) < 2:
        print(f"Usage: {sys.argv[0]} <listen_port>")
        return 1

    port = int(sys.argv[1])

    sock = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
    sock.bind(("0.0.0.0", port))
    sock.settimeout(0.5)
    print(f"Listening on UDP :{port} ...")

    t_recv = threading.Thread(target=udp_receiver, args=(sock,), daemon=True)
    t_recv.start()

    print("Waiting for stream ...  Press 'q' or Ctrl+C to quit.")
    frame_count = 0

    try:
        while True:
            with lock:
                if frame_queue:
                    w, h, yuv420p = frame_queue.pop()
                    # Drain old frames, keep only latest
                    frame_queue.clear()
                else:
                    yuv420p = None

            if yuv420p is not None:
                # COLOR_YUV2BGR_I420 expects single-channel planar layout
                # of shape (h*3/2, w): Y plane stacked over U then V planes.
                yuv = np.frombuffer(yuv420p, dtype=np.uint8).reshape((h * 3 // 2, w))
                bgr = cv2.cvtColor(yuv, cv2.COLOR_YUV2BGR_I420)

                cv2.imshow("3DS Stream", bgr)
                frame_count += 1

            if cv2.waitKey(1) & 0xFF == ord("q"):
                break
    except KeyboardInterrupt:
        pass
    finally:
        running.clear()
        cv2.destroyAllWindows()
        print(f"\nDisplayed {frame_count} frames. Done.")

    return 0

if __name__ == "__main__":
    sys.exit(main())
