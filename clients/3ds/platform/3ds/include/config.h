#pragma once

// Display — top screen (always 400x240, stream is upscaled by GPU)
#define TOP_SCREEN_WIDTH   400
#define TOP_SCREEN_HEIGHT  240

// Stream dimensions — smaller to save bandwidth on Old 3DS WiFi.
// 256x192 = 1.5:1 aspect (close to 3DS top screen 5:3 = 1.67:1).
// YUV420p = 256*192*3/2 = 73728 bytes/frame (~54 chunks @ 1400 B payload).
#define STREAM_WIDTH   256
#define STREAM_HEIGHT  192

// YUV420p frame size at STREAM_WIDTH x STREAM_HEIGHT
#define YUV_FRAME_SIZE (STREAM_WIDTH * STREAM_HEIGHT * 3 / 2)

// Texture atlas — next power-of-2 >= stream dimensions
#define TEX_WIDTH   256
#define TEX_HEIGHT  256

// Logging
#define MAX_LOG_LINES 32

// UDP receiver port (must match server's client_addr port)
#define UDP_RECV_PORT  8001

// HTTP control server — override at compile time: -DSERVER_HOST='"x.x.x.x"'
#ifndef SERVER_HOST
#define SERVER_HOST "192.168.1.100"
#endif
#ifndef SERVER_HTTP_PORT
#define SERVER_HTTP_PORT 1108
#endif

// Stream source URL — override at compile time: -DSOURCE_URL='"rtsp://..."'
#ifndef SOURCE_URL
#define SOURCE_URL "rtsp://192.168.1.100:8554/test"
#endif

// This device's IP (reported to server so it knows where to send UDP)
// Override at compile time: -DMY_IP='"192.168.1.42"'
#ifndef MY_IP
#define MY_IP "192.168.1.42"
#endif

// Frame reassembly limits
// YUV420p 256x192 = 73728 bytes per frame; 256 KB gives ~3.5x headroom.
#define NAL_CHUNK_MAX            128
#define NAL_REASSEMBLY_BUF_SIZE  (256 * 1024)