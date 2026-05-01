#include "udp_rx.hpp"
#include "config.h"
#include "logger.h"
#include <3ds.h>
#include <sys/socket.h>
#include <netinet/in.h>
#include <arpa/inet.h>
#include <fcntl.h>
#include <unistd.h>
#include <string.h>
#include <errno.h>

static int s_sock = -1;

// Diagnostic counters
static volatile uint32_t s_recv_calls = 0;
static volatile uint32_t s_recv_bytes = 0;
static volatile uint32_t s_recv_packets = 0;
static volatile uint32_t s_recv_errors = 0;
static volatile uint32_t s_complete_frames = 0;
static volatile uint32_t s_dropped_frames = 0;  // partial frames dropped

// Per-chunk payload storage
static uint8_t s_chunk_store[NAL_CHUNK_MAX][UDP_MAX_PAYLOAD];

static struct {
    uint16_t sizes[NAL_CHUNK_MAX];
    bool     received[NAL_CHUNK_MAX];
    uint32_t frame_id;
    uint16_t total;
    uint16_t got;
    bool     active;
} s_frame;

// Assembled frame output buffer
static uint8_t  s_complete_buf[NAL_REASSEMBLY_BUF_SIZE];
static uint32_t s_complete_size  = 0;
static bool     s_complete_ready = false;

static void reset_frame(void) {
    s_frame.active = false;
    s_frame.got    = 0;
    s_frame.total  = 0;
}

int udp_rx_init(int port) {
    s_sock = socket(AF_INET, SOCK_DGRAM, IPPROTO_UDP);
    if (s_sock < 0) {
        Log("udp_rx: socket() failed\n");
        return -1;
    }

    // Increase receive buffer to handle large YUV frames (144KB split into ~103 chunks)
    int rcvbuf = 256 * 1024;  // 256 KB
    setsockopt(s_sock, SOL_SOCKET, SO_RCVBUF, &rcvbuf, sizeof(rcvbuf));

    fcntl(s_sock, F_SETFL, O_NONBLOCK);

    struct sockaddr_in addr;
    memset(&addr, 0, sizeof(addr));
    addr.sin_family      = AF_INET;
    addr.sin_addr.s_addr = INADDR_ANY;
    addr.sin_port        = htons((uint16_t)port);

    if (bind(s_sock, (struct sockaddr *)&addr, sizeof(addr)) < 0) {
        Log("udp_rx: bind() failed port %d\n", port);
        close(s_sock);
        s_sock = -1;
        return -1;
    }

    // Verify the buffer was actually set
    int actual = 0;
    socklen_t len = sizeof(actual);
    getsockopt(s_sock, SOL_SOCKET, SO_RCVBUF, &actual, &len);
    Log("udp_rx: listening on :%d  rcvbuf=%d\n", port, actual);

    reset_frame();
    return 0;
}

void udp_rx_deinit(void) {
    if (s_sock >= 0) {
        close(s_sock);
        s_sock = -1;
    }
}

int udp_rx_drain(void) {
    if (s_sock < 0) return 0;

    static uint8_t dgram[UDP_MAX_DGRAM];
    int complete = 0;

    for (;;) {
        ssize_t n = recv(s_sock, dgram, sizeof(dgram), 0);
        s_recv_calls++;
        if (n < 0) {
            s_recv_errors++;
            break;
        }
        s_recv_bytes += (uint32_t)n;
        if ((size_t)n < UDP_HDR_SIZE) continue;
        s_recv_packets++;

        uint32_t magic        = ((uint32_t)dgram[0]<<24)|((uint32_t)dgram[1]<<16)|
                                ((uint32_t)dgram[2]<< 8)| (uint32_t)dgram[3];
        uint32_t frame_id     = ((uint32_t)dgram[4]<<24)|((uint32_t)dgram[5]<<16)|
                                ((uint32_t)dgram[6]<< 8)| (uint32_t)dgram[7];
        uint16_t chunk_id     = ((uint16_t)dgram[8]<<8)  | dgram[9];
        uint16_t total_chunks = ((uint16_t)dgram[10]<<8) | dgram[11];
        uint16_t payload_len  = (uint16_t)(n - UDP_HDR_SIZE);

        if (magic != UDP_MAGIC)               continue;
        if (total_chunks == 0)                continue;
        if (chunk_id >= total_chunks)         continue;
        if (total_chunks > NAL_CHUNK_MAX)     continue;

        // New frame_id → if old one was in progress, count it as dropped
        if (s_frame.active && frame_id != s_frame.frame_id) {
            s_dropped_frames++;
            Log("udp_rx: dropped partial frame %u (had %u/%u chunks)\n",
                s_frame.frame_id, s_frame.got, s_frame.total);
        }

        if (!s_frame.active || frame_id != s_frame.frame_id) {
            reset_frame();
            s_frame.frame_id = frame_id;
            s_frame.total    = total_chunks;
            s_frame.active   = true;
            for (int i = 0; i < total_chunks; i++)
                s_frame.received[i] = false;
        }

        if (s_frame.received[chunk_id]) continue; // duplicate

        s_frame.received[chunk_id] = true;
        s_frame.sizes[chunk_id]    = payload_len;
        memcpy(s_chunk_store[chunk_id], dgram + UDP_HDR_SIZE, payload_len);
        s_frame.got++;

        if (s_frame.got == s_frame.total) {
            uint32_t pos = 0;
            bool overflow = false;
            for (uint16_t i = 0; i < s_frame.total; i++) {
                if (pos + s_frame.sizes[i] > NAL_REASSEMBLY_BUF_SIZE) {
                    Log("udp_rx: frame overflows buf, dropping\n");
                    overflow = true;
                    break;
                }
                memcpy(s_complete_buf + pos, s_chunk_store[i], s_frame.sizes[i]);
                pos += s_frame.sizes[i];
            }
            if (!overflow) {
                s_complete_size  = pos;
                s_complete_ready = true;
                complete++;
                s_complete_frames++;
            }
            reset_frame();
        }
    }

    return complete;
}

const uint8_t *udp_rx_take_frame(uint32_t *out_size) {
    if (!s_complete_ready) return NULL;
    s_complete_ready = false;
    *out_size = s_complete_size;
    return s_complete_buf;
}

uint32_t udp_rx_recv_calls(void)   { return s_recv_calls; }
uint32_t udp_rx_recv_bytes(void)   { return s_recv_bytes; }
uint32_t udp_rx_recv_packets(void) { return s_recv_packets; }
uint32_t udp_rx_recv_errors(void)  { return s_recv_errors; }
uint32_t udp_rx_nal_frames(void)   { return s_complete_frames; }
uint32_t udp_rx_dropped_frames(void){ return s_dropped_frames; }
