#pragma once

// Display — top screen only, v1
#define TOP_SCREEN_WIDTH   400
#define TOP_SCREEN_HEIGHT  240

// Texture atlas — next power-of-2 >= screen dimensions
#define TEX_WIDTH   512
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

// Frame reassembly limits (YUV420p 400x240 = 144000 bytes, plus headroom)
#define NAL_CHUNK_MAX         256
#define NAL_REASSEMBLY_BUF_SIZE  (256 * 1024)
